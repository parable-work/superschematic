//! Comments, the core's conversation on an instance (D16, rebuilt by D77):
//! messages, each top-level or a reply in the thread of a top-level one,
//! one level deep, which their authors edit and delete, others react to
//! and pin, and whose threads are resolved and reopened; a moderator edits
//! and deletes another's and purges. `comments-contract.md` beside this
//! module is the contract it implements: the record, the operations'
//! parameters, checks in order, refusals and results, the paging, the
//! fields and the storage.
//!
//! Every writing operation is beside the instance (D77 primitive 1): it
//! writes only Comments' own tables and its `count` column, never moves the
//! instance's seq, ETag or updatedAt, asks no other behavior's guard (so a
//! Lease another principal holds does not stop a reply), and appends one
//! operation event of no sequence whose change carries its result. The
//! three fields, `messages`, `lastMessageAt` and `threads`, are volatile:
//! each is read from the counts the writes keep, and no event carries
//! their moves.
//!
//! Each message has a number on its instance, 1, 2, 3, ..., never reused;
//! numbers are per instance. Deleting the instance deletes its
//! conversation. Migration 2 moves version 1's comments, a chain of
//! replies, into threads one level deep.
//!
//! configChange: Comments can be added to a schema that has instances,
//! which start with an empty conversation, and cannot be removed from one:
//! their messages would stay behind with nothing to delete them. A config
//! change applies to the next call.

mod checks;
mod reads;
mod record;
mod storage;
mod writes;

use serde_json::Value;

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::{Behavior, ConfigTarget, Error, Hooks, InstanceChange, Result};

use checks::integer;
use storage::conversation;

const NAME: &str = "Comments";

/// The longest body when the config gives no `maxBodyLength`.
pub const DEFAULT_MAX_BODY_LENGTH: i64 = 40_000;

/// Comments' config. Every member is optional; `{}` and no config are the
/// same.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommentsConfig {
    /// The longest body a post or an edit gives, in code points.
    pub max_body_length: i64,
    /// How long after posting its author may edit a message; no limit when
    /// `None`. A moderator is not held to it.
    pub edit_window_ms: Option<i64>,
    /// The block kinds a post or an edit may give; every kind when `None`.
    pub blocks: Option<Vec<String>>,
    /// Edit and delete another's message, edit past the window, purge.
    pub moderate_permission: Option<String>,
    /// Pin and unpin; anyone the policy lets write when `None`.
    pub pin_permission: Option<String>,
    /// Resolve and reopen another's thread; only its author when `None`.
    pub resolve_permission: Option<String>,
}

impl Default for CommentsConfig {
    fn default() -> Self {
        CommentsConfig {
            max_body_length: DEFAULT_MAX_BODY_LENGTH,
            edit_window_ms: None,
            blocks: None,
            moderate_permission: None,
            pin_permission: None,
            resolve_permission: None,
        }
    }
}

impl CommentsConfig {
    /// The config as the configSchema has checked it.
    pub fn parse(raw: &Value) -> Result<Self> {
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        let blocks = match raw.get("blocks") {
            None | Some(Value::Null) => None,
            Some(Value::Array(kinds)) => Some(
                kinds
                    .iter()
                    .map(|kind| {
                        kind.as_str().map(str::to_owned).ok_or_else(|| {
                            Error::config("Comments: blocks lists block kinds, as strings")
                        })
                    })
                    .collect::<Result<Vec<_>>>()?,
            ),
            Some(_) => return Err(Error::config("Comments: blocks is an array")),
        };
        Ok(CommentsConfig {
            max_body_length: integer(raw.get("maxBodyLength")).unwrap_or(DEFAULT_MAX_BODY_LENGTH),
            edit_window_ms: integer(raw.get("editWindowMs")),
            blocks,
            moderate_permission: text("moderatePermission"),
            pin_permission: text("pinPermission"),
            resolve_permission: text("resolvePermission"),
        })
    }
}

/// Comments: a conversation on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Comments;

// The `threads` field: the posted top-level messages, open and resolved.
fn threads(view: &InstanceView<'_, CommentsConfig>) -> Result<Option<Value>> {
    let found = conversation(view)?.unwrap_or_default();
    Ok(Some(checks::object(vec![
        ("open", Value::from(found.open_threads)),
        ("resolved", Value::from(found.resolved_threads)),
    ])))
}

impl Behavior for Comments {
    type Config = CommentsConfig;

    fn declaration(&self) -> &str {
        include_str!("../../declarations/Comments.behavior.json")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<CommentsConfig> {
        CommentsConfig::parse(raw)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the messages its instances have would stay behind with nothing to delete them"
                        .to_owned()
                }))
            }),
            guidance: Some(|_, config, target| crate::guidance::comments::guidance(config, target)),
            migrations: storage::migrations(),
            operations: vec![
                ("post", |_, cx, params| writes::post(cx, params)),
                ("edit", |_, cx, params| writes::edit(cx, params)),
                ("deleteMessage", |_, cx, params| {
                    writes::delete_message(cx, params)
                }),
                ("purge", |_, cx, params| writes::purge(cx, params)),
                ("react", |_, cx, params| writes::react(cx, params)),
                ("unreact", |_, cx, params| writes::unreact(cx, params)),
                ("pin", |_, cx, params| writes::pin(cx, params)),
                ("unpin", |_, cx, params| writes::unpin(cx, params)),
                ("resolve", |_, cx, params| writes::resolve(cx, params)),
                ("reopen", |_, cx, params| writes::reopen(cx, params)),
                ("listMessages", |_, cx, params| {
                    reads::list_messages(cx, params)
                }),
                ("getMessage", |_, cx, params| reads::get_message(cx, params)),
                ("listEdits", |_, cx, params| reads::list_edits(cx, params)),
                ("listReactions", |_, cx, params| {
                    reads::list_reactions(cx, params)
                }),
            ],
            fields: vec![
                ("messages", |_, view| {
                    Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
                }),
                ("lastMessageAt", |_, view| {
                    Ok(conversation(view)?
                        .and_then(|found| found.last_message_at)
                        .map(Value::from))
                }),
                ("threads", |_, view| threads(view)),
            ],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    writes::delete_conversation(cx)?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}
