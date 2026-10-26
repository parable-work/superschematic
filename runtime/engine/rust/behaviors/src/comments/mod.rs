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
//! A message may be streamed as it is written (batch 2): `begin` holds its
//! place as composing, `appendDraft` adds to its draft, a chunk at a time,
//! and sends the `draft` signal once its call commits, and `finish` posts
//! it; `interrupt` stops one, keeping what was written, and the schedule
//! `interrupt` calls it, as the runner, on each message whose author has
//! appended nothing for `composeTimeoutMs`. A message *stands* while it is
//! posted or interrupted, and the fields count the standing ones; the
//! `composing` field names who is writing one. Migration 3 adds the drafts.
//!
//! Each message that comes to stand takes a stand, its place in the order
//! the conversation's messages came to stand, which read markers count in
//! (batch 3): `markRead` moves the caller's marker, `follow` its follow of
//! a thread, and the `unread` field is the reader's. A message that comes
//! to stand records a delivery for each subject it mentions and each
//! follower of its thread; the namespace operations `inbox`,
//! `claimDeliveries` and `settleDeliveries` read and work them across a
//! namespace, and the schedule `prune` removes them once
//! `deliveryRetentionMs` passes. Migration 4 adds them.
//!
//! configChange: Comments can be added to a schema that has instances,
//! which start with an empty conversation, and cannot be removed from one:
//! their messages would stay behind with nothing to delete them. A config
//! change applies to the next call.

mod checks;
mod deliveries;
mod drafts;
mod follows;
mod inbox;
mod reads;
mod record;
mod storage;
mod writes;

use serde_json::Value;

use superschematic_engine::behaviors::{Every, InstanceView, Schedule};
use superschematic_engine::{Behavior, ConfigTarget, Error, Hooks, InstanceChange, Result};

use checks::integer;
use storage::conversation;

const NAME: &str = "Comments";

/// The longest body when the config gives no `maxBodyLength`.
pub const DEFAULT_MAX_BODY_LENGTH: i64 = 40_000;
/// How long a composing message's author may append nothing when the
/// config gives no `composeTimeoutMs`.
pub const DEFAULT_COMPOSE_TIMEOUT_MS: i64 = 60_000;
/// The most composing messages a subject has on an instance when the
/// config gives no `maxDrafts`.
pub const DEFAULT_MAX_DRAFTS: i64 = 5;
/// How many claims a delivery gets when the config gives no `maxAttempts`.
pub const DEFAULT_MAX_ATTEMPTS: i64 = 5;
/// How long after its first failure a delivery is due again when the
/// config gives no `retryBackoffMs`.
pub const DEFAULT_RETRY_BACKOFF_MS: i64 = 30_000;
/// How long a delivery is kept when the config gives no
/// `deliveryRetentionMs`: 30 days.
pub const DEFAULT_DELIVERY_RETENTION_MS: i64 = 2_592_000_000;
/// The schedule `prune`'s interval: an hour.
const PRUNE_EVERY_MS: i64 = 3_600_000;
/// The longest interval of the schedule `interrupt`.
const MAX_INTERRUPT_EVERY_MS: i64 = 15_000;

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
    /// How long a composing message's author may append nothing before
    /// the runner interrupts it.
    pub compose_timeout_ms: i64,
    /// The most composing messages one subject has on an instance at once.
    pub max_drafts: i64,
    /// What claimDeliveries and settleDeliveries need of the caller for
    /// this schema's deliveries; nobody claims them when `None`.
    pub deliver_permission: Option<String>,
    /// How many claims a delivery gets.
    pub max_attempts: i64,
    /// How long after its first failure a delivery is due again, doubled
    /// after each later one up to an hour.
    pub retry_backoff_ms: i64,
    /// How long a delivery is kept from when it was made.
    pub delivery_retention_ms: i64,
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
            compose_timeout_ms: DEFAULT_COMPOSE_TIMEOUT_MS,
            max_drafts: DEFAULT_MAX_DRAFTS,
            deliver_permission: None,
            max_attempts: DEFAULT_MAX_ATTEMPTS,
            retry_backoff_ms: DEFAULT_RETRY_BACKOFF_MS,
            delivery_retention_ms: DEFAULT_DELIVERY_RETENTION_MS,
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
            compose_timeout_ms: integer(raw.get("composeTimeoutMs"))
                .unwrap_or(DEFAULT_COMPOSE_TIMEOUT_MS),
            max_drafts: integer(raw.get("maxDrafts")).unwrap_or(DEFAULT_MAX_DRAFTS),
            deliver_permission: text("deliverPermission"),
            max_attempts: integer(raw.get("maxAttempts")).unwrap_or(DEFAULT_MAX_ATTEMPTS),
            retry_backoff_ms: integer(raw.get("retryBackoffMs"))
                .unwrap_or(DEFAULT_RETRY_BACKOFF_MS),
            delivery_retention_ms: integer(raw.get("deliveryRetentionMs"))
                .unwrap_or(DEFAULT_DELIVERY_RETENTION_MS),
        })
    }

    /// The schedule `interrupt`'s interval: a quarter of
    /// `composeTimeoutMs`, at most 15 seconds.
    pub fn interrupt_every_ms(&self) -> i64 {
        (self.compose_timeout_ms / 4).min(MAX_INTERRUPT_EVERY_MS)
    }
}

/// Comments: a conversation on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Comments;

// The `threads` field: the standing top-level messages, open and resolved.
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
                ("begin", |_, cx, params| drafts::begin(cx, params)),
                ("appendDraft", |_, cx, params| {
                    drafts::append_draft(cx, params)
                }),
                ("finish", |_, cx, params| drafts::finish(cx, params)),
                ("interrupt", |_, cx, params| drafts::interrupt(cx, params)),
                ("listMessages", |_, cx, params| {
                    reads::list_messages(cx, params)
                }),
                ("getMessage", |_, cx, params| reads::get_message(cx, params)),
                ("listEdits", |_, cx, params| reads::list_edits(cx, params)),
                ("listReactions", |_, cx, params| {
                    reads::list_reactions(cx, params)
                }),
                ("markRead", |_, cx, params| follows::mark_read(cx, params)),
                ("follow", |_, cx, params| follows::follow(cx, params)),
            ],
            namespace_operations: vec![
                ("inbox", |_, cx, params| inbox::inbox(cx, params)),
                ("claimDeliveries", |_, cx, params| {
                    deliveries::claim_deliveries(cx, params)
                }),
                ("settleDeliveries", |_, cx, params| {
                    deliveries::settle_deliveries(cx, params)
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
                ("composing", |_, view| {
                    Ok(Some(Value::Array(
                        storage::composing(view)?
                            .into_iter()
                            .map(Value::from)
                            .collect(),
                    )))
                }),
                ("unread", |_, view| {
                    Ok(Some(Value::from(follows::unread(view)?)))
                }),
            ],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    writes::delete_conversation(cx)?;
                }
                Ok(())
            }),
            schedules: vec![
                (
                    "interrupt",
                    Schedule {
                        every: Every::Config(|_, config: &CommentsConfig| {
                            Ok(Some(config.interrupt_every_ms()))
                        }),
                        run: |_, cx| drafts::interrupt_idle(cx),
                    },
                ),
                (
                    "prune",
                    Schedule {
                        every: Every::Ms(PRUNE_EVERY_MS),
                        run: |_, cx| deliveries::prune(cx),
                    },
                ),
            ],
            ..Hooks::default()
        }
    }
}
