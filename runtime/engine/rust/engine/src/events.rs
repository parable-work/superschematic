//! Appending to the event log, the part of `src/events/log.ts` the registry
//! writes with: a define event, which records a draft's hash, and a publish
//! event, which records the version's canonical document (D16). Milestone
//! 4b brings the log's reads, its filters and retention, and the events of
//! instances.
//!
//! The cursor is the key the database gives the row, read back with
//! `RETURNING` on both databases. A commit that appended an event wakes
//! the waiters on the storage's notifier, in every process on Postgres
//! (D58).

use superschematic_engine_storage::{SqlValue, Tx};

use crate::errors::Result;
use crate::principal::Actor;

/// What a schema's define or publish appends.
pub(crate) struct SchemaEvent<'a> {
    /// `define` or `publish`.
    pub kind: &'a str,
    pub namespace: &'a str,
    pub schema: &'a str,
    /// The published version; `None` for a define.
    pub version: Option<i64>,
    pub actor: &'a Actor,
    /// When, in epoch milliseconds.
    pub at: i64,
    /// The change as JSON text: a define's `{"hash": ...}`, a publish's
    /// canonical document.
    pub change: String,
}

/// Appends an event and returns its cursor.
pub(crate) fn append_schema_event(tx: &Tx<'_>, event: &SchemaEvent<'_>) -> Result<i64> {
    let row = tx.get(
        "INSERT INTO engine_events
           (kind, namespace, schema, instance_id, seq, version, actor, service, at, change, cause_behavior, cause_event, cause_schedule, depth, value_refs)
         VALUES (?, ?, ?, NULL, NULL, ?, ?, ?, ?, ?, NULL, NULL, NULL, 0, NULL)
         RETURNING cursor",
        &[
            SqlValue::from(event.kind),
            SqlValue::from(event.namespace),
            SqlValue::from(event.schema),
            SqlValue::from(event.version),
            SqlValue::from(event.actor.actor.as_str()),
            SqlValue::from(event.actor.service.as_deref()),
            SqlValue::Integer(event.at),
            SqlValue::from(event.change.as_str()),
        ],
    )?;
    let cursor = match row {
        Some(row) => row.i64("cursor")?,
        None => {
            return Err(crate::errors::Error::internal(
                "the event log returned no cursor for an appended event",
            ))
        }
    };
    tx.notify(event.namespace)?;
    Ok(cursor)
}
