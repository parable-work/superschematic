//! The events a table's Comments emits when its config sets `events`
//! (#713): `mentioned` and `replied`, each about the row, in the command's
//! transaction (D104), naming the subjects that should hear of a message.
//! They carry what the deliveries record (who should hear of a message,
//! and why) to the notifier, whose inbox is the in-app inbox (D153): the
//! API's behavior worker hands each to the project's handler, which calls
//! the notifier's `fromBehaviorEvent`, and a route's recipient `{ payload:
//! "subjects" }` reaches each subject who may read the row.
//!
//! A subject both mentioned and following hears of the message once, as
//! mentioned. The author and the subject the message was written for are
//! never among them, as they are not among its deliveries.

use serde_json::{Map, Value};
use superschematic_rules::{Cx, Hook};

use super::messages::Message;
use super::CommentsConfig;

/// A message names subjects that should hear of it by their mention.
pub(super) const MENTIONED: &str = "mentioned";
/// A reply names the followers of its thread that should hear of it.
pub(super) const REPLIED: &str = "replied";

/// The longest excerpt of a message's body an event carries, in code
/// points.
const EXCERPT: usize = 200;

/// Emits `kind` about the message to `subjects`, when the config sets
/// events and there is any.
pub(super) fn emit(
    cx: &Cx<'_, CommentsConfig>,
    kind: &str,
    message: &Message,
    subjects: &[&str],
) -> Hook<()> {
    if !cx.config().events || subjects.is_empty() {
        return Ok(());
    }
    let mut payload = Map::new();
    payload.insert("message".into(), Value::from(message.id));
    if let Some(thread) = message.thread {
        payload.insert("thread".into(), Value::from(thread));
    }
    payload.insert("author".into(), Value::from(message.author.as_str()));
    if let Some(acted_for) = &message.for_subject {
        payload.insert("forSubject".into(), Value::from(acted_for.as_str()));
    }
    if let Some(via) = &message.via {
        payload.insert("via".into(), Value::from(via.as_str()));
    }
    let excerpt: String = message.body.chars().take(EXCERPT).collect();
    if !excerpt.is_empty() {
        payload.insert("excerpt".into(), Value::from(excerpt));
    }
    payload.insert(
        "subjects".into(),
        Value::Array(
            subjects
                .iter()
                .map(|subject| Value::from(*subject))
                .collect(),
        ),
    );
    cx.emit(kind, Value::Object(payload))
}
