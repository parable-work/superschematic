//! What Comments' operations refuse (`comments-contract.md`, "Refusals"):
//! `invalid_argument` at a JSON pointer, `forbidden` for a permission the
//! config names, and vetoes with the declaration's codes and the contract's
//! details; and the checks the operations share, in the order their tables
//! list them.

use std::sync::LazyLock;

use serde_json::{json, Map, Value};
use superschematic_schema_ir::ajv::AjvSchema;

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::{EngineError, EngineErrorCode, Error, Result};

use super::storage::{message, Message};
use super::{CommentsConfig, NAME};

/// The largest `data` an action block holds, in bytes of compact JSON.
pub(super) const MAX_ACTION_DATA: usize = 8192;
/// The most edits a message has.
pub(super) const MAX_EDITS: i64 = 500;
/// The most distinct emoji a message holds reactions with.
pub(super) const MAX_EMOJI: i64 = 20;
/// The most bytes an appendDraft's `text` takes written as a JSON string,
/// its quotes included, so its draft signal fits a signal's 4096 bytes.
pub(super) const MAX_APPEND_BYTES: usize = 4000;

// The pattern post's body declares: a text has text when it matches.
static HAS_TEXT: LazyLock<AjvSchema> = LazyLock::new(|| {
    AjvSchema::compile(&json!({"type": "string", "pattern": "\\S"}))
        .expect("the pattern \\S compiles")
});

/// Whether a text has text: it holds a code point that is not white space,
/// decided as the engine decides post's body pattern `\S`.
pub(super) fn has_text(text: &str) -> bool {
    HAS_TEXT.is_valid(&Value::from(text))
}

/// A veto of one of Comments' operations, with its code and details.
pub(super) fn veto<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    code: &str,
    reason: String,
    details: Value,
) -> Error {
    let details = match details {
        Value::Object(map) => Some(map),
        _ => None,
    };
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details,
        },
    )
}

/// A refusal of an operation's parameters: one issue at `path`.
pub(super) fn invalid(
    operation: &str,
    path: impl Into<String>,
    message: impl Into<String>,
) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

/// The caller lacks a permission the config names.
pub(super) fn forbidden<C>(view: &InstanceView<'_, C>, operation: &str, permission: &str) -> Error {
    EngineError::new(
        EngineErrorCode::Forbidden,
        format!(
            "{} may not {operation} on {} {}: it needs permission {permission}",
            view.principal().subject,
            view.schema(),
            view.id()
        ),
    )
    .into()
}

/// Whether the caller holds `permission`, when the config names one.
pub(super) fn holds<C>(view: &InstanceView<'_, C>, permission: Option<&str>) -> Result<bool> {
    match permission {
        Some(permission) => view.can(permission),
        None => Ok(false),
    }
}

/// Whether the caller moderates: the config names `moderatePermission` and
/// the caller holds it.
pub(super) fn moderates(view: &InstanceView<'_, CommentsConfig>) -> Result<bool> {
    holds(view, view.config().moderate_permission.as_deref())
}

/// The present message `id` names, or `no_message`.
pub(super) fn present<C>(view: &InstanceView<'_, C>, operation: &str, id: i64) -> Result<Message> {
    message(view, id)?.ok_or_else(|| no_message(view, operation, id))
}

/// `no_message` for an id that names nothing present.
pub(super) fn no_message<C>(view: &InstanceView<'_, C>, operation: &str, id: i64) -> Error {
    veto(
        view,
        operation,
        "no_message",
        format!("{} {} has no message {id}", view.schema(), view.id()),
        json!({ "message": id }),
    )
}

/// `deleted` for a message that is.
pub(super) fn deleted<C>(view: &InstanceView<'_, C>, operation: &str, id: i64) -> Error {
    veto(
        view,
        operation,
        "deleted",
        format!("message {id} of {} {} is deleted", view.schema(), view.id()),
        json!({ "message": id }),
    )
}

/// `composing` for a composing message an operation names, or a post's or
/// a begin's thread or quotes.
pub(super) fn composing<C>(view: &InstanceView<'_, C>, operation: &str, id: i64) -> Error {
    veto(
        view,
        operation,
        "composing",
        format!(
            "message {id} of {} {} is composing: it is being written, and stands once finished",
            view.schema(),
            view.id()
        ),
        json!({ "message": id }),
    )
}

/// `not_composing` for an appendDraft, finish or interrupt of a message
/// that is posted, interrupted or deleted.
pub(super) fn not_composing<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    message: &Message,
) -> Error {
    veto(
        view,
        operation,
        "not_composing",
        format!(
            "message {} of {} {} is {}, not composing",
            message.id,
            view.schema(),
            view.id(),
            message.state
        ),
        json!({ "message": message.id, "state": message.state }),
    )
}

/// `not_a_thread` for a reply named as a thread.
pub(super) fn not_a_thread<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    reply: &Message,
) -> Error {
    let thread = reply.thread.unwrap_or_default();
    veto(
        view,
        operation,
        "not_a_thread",
        format!(
            "message {} of {} {} is a reply in thread {thread}, not a thread",
            reply.id,
            view.schema(),
            view.id()
        ),
        json!({ "message": reply.id, "thread": thread }),
    )
}

/// `not_author` for a caller who is not the message's author.
pub(super) fn not_author<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    message: &Message,
) -> Error {
    veto(
        view,
        operation,
        "not_author",
        format!(
            "{} is not the author of message {} of {} {}; {} is",
            view.principal().subject,
            message.id,
            view.schema(),
            view.id(),
            message.author
        ),
        json!({ "message": message.id, "author": message.author }),
    )
}

/// The body a post or an edit gives is at most `maxBodyLength` code points.
pub(super) fn check_body(
    view: &InstanceView<'_, CommentsConfig>,
    operation: &str,
    body: Option<&str>,
) -> Result<()> {
    let max = view.config().max_body_length;
    match body {
        Some(body) if body.chars().count() as i64 > max => Err(invalid(
            operation,
            "/body",
            format!("must NOT have more than {max} characters"),
        )),
        _ => Ok(()),
    }
}

/// Each action block's `data`, as compact JSON, is at most 8192 bytes.
pub(super) fn check_action_data(operation: &str, blocks: Option<&Value>) -> Result<()> {
    for (index, block) in blocks_of(blocks).iter().enumerate() {
        if block.get("kind").and_then(Value::as_str) != Some("action") {
            continue;
        }
        if let Some(data) = block.get("data") {
            let size = serde_json::to_vec(data)
                .map(|bytes| bytes.len())
                .unwrap_or(0);
            if size > MAX_ACTION_DATA {
                return Err(invalid(
                    operation,
                    format!("/blocks/{index}/data"),
                    format!("is more than {MAX_ACTION_DATA} bytes of JSON"),
                ));
            }
        }
    }
    Ok(())
}

/// The blocks a post or an edit gives: each kind one the config allows,
/// then each ref block's instance one the caller may read. A ref block
/// whose schema and id one of `before` holds is not read again.
pub(super) fn check_blocks(
    view: &InstanceView<'_, CommentsConfig>,
    operation: &str,
    blocks: Option<&Value>,
    before: Option<&Value>,
) -> Result<()> {
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
                    operation,
                    "block_not_allowed",
                    format!(
                        "block {index} is a {kind} block, which Comments on {} does not allow (it allows {allows})",
                        view.schema()
                    ),
                    json!({ "block": index, "kind": kind, "allowed": allowed }),
                ));
            }
        }
    }
    let held = blocks_of(before);
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
        if known || readable_instance(view, schema, id)? {
            continue;
        }
        return Err(veto(
            view,
            operation,
            "ref_not_found",
            format!(
                "block {index} refers to {schema} {id}, which is not an instance {} may read in namespace {}",
                view.principal().subject,
                view.namespace()
            ),
            json!({ "block": index, "schema": schema, "id": id }),
        ));
    }
    Ok(())
}

// readable_instance is whether the instance exists in the call's namespace
// and the caller may read it; an unknown schema is neither.
fn readable_instance<C>(view: &InstanceView<'_, C>, schema: &str, id: &str) -> Result<bool> {
    if !view.schemas().readable(schema)? {
        return Ok(false);
    }
    let none: &[&str] = &[];
    match view.instances().get(schema, id, Some(none)) {
        Ok(found) => Ok(found.is_some()),
        Err(error)
            if matches!(
                error.code(),
                Some(EngineErrorCode::NotFound | EngineErrorCode::Forbidden)
            ) =>
        {
            Ok(false)
        }
        Err(error) => Err(error),
    }
}

/// A `blocks` parameter's blocks, none when it is absent.
pub(super) fn blocks_of(blocks: Option<&Value>) -> &[Value] {
    blocks
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default()
}

/// A parameter the paramsSchema holds to an integer, which JSON may write
/// as 2 or 2.0.
pub(super) fn integer(value: Option<&Value>) -> Option<i64> {
    match value {
        Some(Value::Number(number)) => {
            let read = superschematic_engine::json::number_value(number);
            (read.fract() == 0.0).then_some(read as i64)
        }
        _ => None,
    }
}

/// An object of the given members, for a result.
pub(super) fn object(members: Vec<(&str, Value)>) -> Value {
    let mut out = Map::new();
    for (name, value) in members {
        out.insert(name.to_owned(), value);
    }
    Value::Object(out)
}
