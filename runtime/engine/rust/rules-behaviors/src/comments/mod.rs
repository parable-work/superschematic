//! Comments, a conversation on each row (D16, rebuilt by D77), ported from
//! the engine's `comments/` with its rules and words
//! (`../behaviors/src/comments-contract.md`, batch 1 as batches 2 to 5 amend
//! it): messages, each top-level or a reply in the thread of a top-level
//! one, one level deep, which their authors edit and delete, others react
//! to and pin, and whose threads are resolved and reopened; a moderator
//! edits and deletes another's and purges. A message may quote another of
//! its thread, name subjects it mentions, and carry blocks: `ref` (another
//! row), `action` (a card for what the poster did) and `code` (a place in
//! code at a revision).
//!
//! Every writing operation is beside the row (D77's primitive 1,
//! [`superschematic_rules::OperationDecl::beside`]): it writes only the
//! use's sub-tables and its shared table, never moves the row's version,
//! its ETag or its audit stamps, and asks no other behavior's guard, so a
//! Lease another principal holds does not stop a reply. The core holds two
//! writes of one conversation apart through the conversation's row of the
//! shared table `conversations`: every writing operation moves it, and a
//! concurrent one moved past the version read runs the command again.
//!
//! The state is the use's. `messages`, keyed by the message's id in ten
//! digits, zero-padded, so the key's order is the ids'; `edits`, what each
//! edit replaced, keyed by the message and the edit's number; `reactions`,
//! keyed by the reaction's number on the row, which orders them; and the
//! shared `conversations`, keyed by the row's key, holding the greatest
//! message id, reaction number and stand the conversation has given. A
//! delete of the row deletes its sub-table rows, as the host deletes every
//! sub-table row of a deleted row, and leaves its conversation's row, so a
//! row re-created with the key numbers its messages on from it and never
//! reuses an id, as the engine's high-water marks did.
//!
//! The three fields, `messages`, `lastMessageAt` and `threads`, are
//! computed at a read from the `messages` sub-table, as the engine's
//! volatile fields were from its counts, so no write carries their moves.
//!
//! What the engine's Comments has that a table's leaves out, each a part
//! the core cannot run yet or a later step's: streamed replies (`begin`,
//! `appendDraft`, `finish`, `interrupt`, the `composing` field and the
//! config's `composeTimeoutMs` and `maxDrafts`), which the engine built on
//! signals (D77's primitive 2); read state and follows (`markRead`,
//! `follow`, the `unread` field), the inbox and deliveries (the config's
//! `deliverPermission`, `maxAttempts`, `retryBackoffMs` and
//! `deliveryRetentionMs`), which it built on namespace operations (D77's
//! primitive 3); `searchMessages`, a full-text search across a namespace;
//! attachment blocks and the config's `attachments`; `moveAnchor` and
//! `anchorPermission`; a message's `via` and `for`, which the core's
//! caller does not carry; and the operation events the engine logged, a
//! table's events reaching the app through its event queue (D104). The
//! core declares none of those operations and fields, and refuses a config
//! that sets one of those members, so a table's use waits for them in so
//! many words. A ref block names a row of a table the database's layout
//! holds, which the core reads; whether the caller may read it is the
//! route's, as a table's access policy is (D103).
//!
//! Comments is held to D103's test of a behavior and passes it: a
//! conversation is state of its own (messages, their edits and reactions,
//! pins and resolved threads), its rules say how that state changes (only
//! an author edits or deletes their message, within the config's window
//! and its count of edits; a reaction is a subject's once per emoji, at
//! most 20 emoji a message; a thread is resolved once and reopened once
//! resolved; a moderator alone purges), and it recurs across applications
//! (a ticket's, a change under review's, an incident's, a document's). No
//! schema feature keeps it: a `HasMany` of notes holds the rows and none
//! of the rules.
//!
//! configChange: Comments can be added to a table that has rows, which
//! start with an empty conversation, and cannot be removed from one: their
//! messages would stay behind with nothing to delete them. A config change
//! applies to the next call: narrowing `blocks` or `maxBodyLength` leaves
//! the messages as they are.

mod checks;
mod messages;
mod params;
mod reads;
mod writes;

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Hook, Row, SchemaIssue, SubRow,
};

use crate::declared;

use messages::Message;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Comments";

/// Its declaration file, which the compiler registers and the engine
/// embeds.
pub const DECLARATION: &str =
    include_str!("../../../behaviors/declarations/Comments.behavior.json");

/// The sub-table of the row's messages.
pub const MESSAGES: &str = "messages";
/// The sub-table of what each edit replaced.
pub const EDITS: &str = "edits";
/// The sub-table of the reactions.
pub const REACTIONS: &str = "reactions";
/// The shared table of each row's conversation's high-water marks.
pub const CONVERSATIONS: &str = "conversations";

/// The computed field that counts the standing messages.
pub const MESSAGES_FIELD: &str = "messages";
/// The computed field of the latest standing message's postedAt.
pub const LAST_MESSAGE_AT: &str = "lastMessageAt";
/// The computed field of the threads, open and resolved.
pub const THREADS: &str = "threads";

/// The longest body when the config gives no `maxBodyLength`.
pub const DEFAULT_MAX_BODY_LENGTH: i64 = 40_000;

/// The operations a table's Comments leaves out of its declaration, each
/// with why: the engine's alone until the core runs what they need. The
/// build leaves them out of a table's operations too
/// (`registry.TableLeavesOut`).
pub const LEFT_OUT_OPERATIONS: [(&str, &str); 7] = [
    ("begin", STREAMED),
    ("appendDraft", STREAMED),
    ("finish", STREAMED),
    ("interrupt", STREAMED),
    ("markRead", READ_STATE),
    ("follow", READ_STATE),
    ("moveAnchor", ANCHORS),
];

/// The fields a table's Comments leaves out of its declaration, each with
/// why.
pub const LEFT_OUT_FIELDS: [(&str, &str); 2] = [("composing", STREAMED), ("unread", READ_STATE)];

/// Why a streamed reply waits.
pub const STREAMED: &str = "a streamed reply (begin, appendDraft, finish, interrupt and the composing field) waits for signals on the core, which the engine sent its drafts by (D77's primitive 2)";
/// Why read state waits.
pub const READ_STATE: &str = "read state (markRead, follow, the unread field and a listing's readUpTo), the inbox and deliveries each waits for namespace operations on the core, which the engine read them across a namespace by (D77's primitive 3)";
/// Why moving a code block's anchor waits.
pub const ANCHORS: &str = "moving a code block's anchor (moveAnchor and anchorPermission) waits for code anchors on the core (D77's step 5, D103's step 4)";

/// The config members a table's Comments refuses, each with why it waits.
const WAITING_MEMBERS: [(&str, &str); 9] = [
    ("composeTimeoutMs", STREAMED),
    ("maxDrafts", STREAMED),
    ("deliverPermission", READ_STATE),
    ("maxAttempts", READ_STATE),
    ("retryBackoffMs", READ_STATE),
    ("deliveryRetentionMs", READ_STATE),
    ("anchorPermission", ANCHORS),
    ("attachments", ATTACHMENTS),
    ("blocks", ATTACHMENTS),
];

/// Why an attachment block waits.
pub const ATTACHMENTS: &str =
    "an attachment block waits for attachments on the core (D77's step 5, D103's step 4)";

/// Comments' config. Every member is optional; `{}` and no config are the
/// same.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommentsConfig {
    /// The longest body a post or an edit gives, in code points.
    pub max_body_length: i64,
    /// How long after its postedAt its author may edit a message; no limit
    /// when `None`. A moderator is not held to it.
    pub edit_window_ms: Option<i64>,
    /// The block kinds a post or an edit may give; every kind a table's
    /// Comments takes when `None`.
    pub blocks: Option<Vec<String>>,
    /// Edit and delete another's message, edit past the window, purge.
    pub moderate_permission: Option<String>,
    /// Pin and unpin; anyone the route lets call them when `None`.
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
    /// Reads a config held to the declaration's configSchema, in its words,
    /// then refuses a member a table's Comments waits for.
    pub fn parse(raw: &Value) -> Result<CommentsConfig, ConfigError> {
        let issues = params::config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        for (member, why) in WAITING_MEMBERS {
            let Some(value) = raw.get(member) else {
                continue;
            };
            // A blocks list waits only when it names attachment.
            if member == "blocks"
                && !value
                    .as_array()
                    .is_some_and(|kinds| kinds.iter().any(|kind| kind == "attachment"))
            {
                continue;
            }
            return Err(if member == "blocks" {
                format!("blocks lists attachment: {why}").into()
            } else {
                format!("{member}: {why}").into()
            });
        }
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        Ok(CommentsConfig {
            max_body_length: raw
                .get("maxBodyLength")
                .and_then(checks::whole)
                .unwrap_or(DEFAULT_MAX_BODY_LENGTH),
            edit_window_ms: raw.get("editWindowMs").and_then(checks::whole),
            blocks: raw.get("blocks").and_then(Value::as_array).map(|kinds| {
                kinds
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            }),
            moderate_permission: text("moderatePermission"),
            pin_permission: text("pinPermission"),
            resolve_permission: text("resolvePermission"),
        })
    }
}

/// Comments: a conversation on each row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Comments;

impl Behavior for Comments {
    type Config = CommentsConfig;

    // The declaration file names the three fields, the sub-tables and the
    // shared table; the operations and fields a table's Comments leaves
    // out are left out of it, and so are the namespace's operations, which
    // the core runs no command for.
    fn declaration(&self) -> Declaration {
        let mut declared = declared::declaration(DECLARATION);
        let operations: Vec<&str> = LEFT_OUT_OPERATIONS.iter().map(|(name, _)| *name).collect();
        declared
            .operations
            .retain(|operation| !operations.contains(&operation.name.as_str()));
        let fields: Vec<&str> = LEFT_OUT_FIELDS.iter().map(|(name, _)| *name).collect();
        declared
            .fields
            .retain(|field| !fields.contains(&field.as_str()));
        declared
            .computed
            .retain(|field| !fields.contains(&field.as_str()));
        declared
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<CommentsConfig, ConfigError> {
        CommentsConfig::parse(raw)
    }

    // See the module's header: removed from a table with rows, its
    // messages would stay behind.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&CommentsConfig>,
        after: Option<&CommentsConfig>,
    ) -> Hook<Option<String>> {
        if before.is_none() || after.is_some() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(
            "the messages its instances have would stay behind with nothing to delete them"
                .to_owned(),
        ))
    }

    fn check_params(
        &self,
        _config: &CommentsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params::params_issues(operation, params).into_issues()
    }

    fn operation(&self, cx: &Cx<'_, CommentsConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            writes::POST => writes::post(cx, params),
            writes::EDIT => writes::edit(cx, params),
            writes::DELETE_MESSAGE => writes::delete_message(cx, params),
            writes::PURGE => writes::purge(cx, params),
            writes::REACT => writes::react(cx, params),
            writes::UNREACT => writes::unreact(cx, params),
            writes::PIN => writes::pin(cx, params, true),
            writes::UNPIN => writes::pin(cx, params, false),
            writes::RESOLVE => writes::resolve(cx, params, true),
            writes::REOPEN => writes::resolve(cx, params, false),
            reads::LIST_MESSAGES => reads::list_messages(cx, params),
            reads::GET_MESSAGE => reads::get_message(cx, params),
            reads::LIST_EDITS => reads::list_edits(cx, params),
            reads::LIST_REACTIONS => reads::list_reactions(cx, params),
            reads::LIST_PINNED => reads::list_pinned(cx, params),
            _ => Err(cx.defect(format!("{name} is not an operation of Comments"))),
        }
    }

    // The three fields, from the messages: how many stand, the latest
    // standing one's postedAt, and the standing top-level messages, open
    // and resolved.
    fn read_fields(
        &self,
        _config: &CommentsConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let messages: Vec<Message> = tables
            .get(MESSAGES)
            .map(|rows| rows.iter().filter_map(Message::of).collect())
            .unwrap_or_default();
        messages::fields(&messages)
    }
}
