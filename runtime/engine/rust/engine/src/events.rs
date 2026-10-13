//! The event log, the port of `src/events/log.ts` and `src/events/notifier.ts`
//! (D16, D58): one append-only table, `engine_events`. Each change appends
//! one event in the transaction that makes it: an instance's create,
//! update and delete, a writing behavior operation on it, a schema's
//! publish, and the define of a draft, which carries the draft's hash and
//! not its document. An event has a global cursor, which orders the whole
//! log, and an instance's events also a per-instance sequence, 1, 2, 3, ...
//! across its life, a re-create after a delete included.
//!
//! The cursor is the key the database gives the row (SQLite's
//! AUTOINCREMENT, Postgres's identity), read back with `RETURNING`. On
//! Postgres an identity is drawn before its transaction commits, so two
//! writers' cursors need not commit in cursor order; a read never misses an
//! event for it because every read of the log is scoped to one namespace,
//! the writes of a namespace are serialized by its lock, and the shared
//! namespace's publish events, which other namespaces read, are written
//! under the catalog's lock, which excludes every writer (D58). So within
//! what one read can see, cursors commit in order, and a reader that goes
//! on from a page's `next` sees every later event exactly once.
//!
//! A commit that appended an event wakes the waiters on the storage's
//! notifier ([`Events::wait`], [`Events::watch`]), in every process on
//! Postgres, where it is `pg_notify` on commit and each process listens:
//! the event stream, `watch` and the runner block on it rather than poll.
//!
//! Retention, which prunes the log and keeps each pruned instance's base,
//! runs on the runner and arrives with it (milestone 6); the log already
//! reads its floors and bases, so a storage the TypeScript engine pruned
//! reads as it did.

use std::collections::BTreeSet;
use std::time::{Duration, Instant};

use serde::Serialize;
use serde_json::Value;

use superschematic_engine_storage::{Row, Sql, SqlValue, Tx};

use crate::access::SchemaAction;
use crate::composition::is_behavior_name;
use crate::engine::Engine;
use crate::errors::{EngineError, Error, Result};
use crate::json;
use crate::namespaces::NamespaceView;
use crate::paging::page_size;
use crate::principal::{check_principal, Actor, Principal};
use crate::registry::document::check_schema_name;
use crate::values::{refs_of, refs_text, Stowed, ValueHolder, ValueStore};

/// Every event kind, in the order the log's reference lists them.
pub const EVENT_KINDS: [&str; 6] = [
    "create",
    "update",
    "delete",
    "operation",
    "publish",
    "define",
];

/// Why the runner's work wrote an event: a reaction to an event, or a
/// schedule.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct EventCause {
    /// The behavior whose reaction or schedule wrote it.
    pub behavior: String,
    /// The cursor of the event its reaction handled; absent for a
    /// schedule's.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub event: Option<i64>,
    /// The schedule that ran; absent for a reaction's.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub schedule: Option<String>,
    /// One more than its cause's depth.
    pub depth: i64,
}

/// What [`append_event`] writes.
pub(crate) struct NewEvent<'a> {
    pub kind: &'a str,
    pub namespace: &'a str,
    pub schema: &'a str,
    pub instance_id: Option<&'a str>,
    pub seq: Option<i64>,
    /// `None` for a define.
    pub version: Option<i64>,
    pub actor: &'a Actor,
    pub at: i64,
    /// The change as JSON text, or `None`.
    pub change: Option<String>,
    /// For an event the runner's work writes, what caused it.
    pub cause: Option<&'a EventCause>,
    /// For a change the value store stowed: the store, and the pointers to
    /// its refs and the hashes they name, which the event holds from its
    /// append on.
    pub values: Option<(&'a ValueStore, &'a Stowed)>,
}

/// Appends one event and returns its cursor; call it inside the change's
/// transaction. The waiters on the storage's notifier wake once that
/// transaction commits, and never if it rolls back.
pub(crate) fn append_event(tx: &Tx<'_>, event: &NewEvent<'_>) -> Result<i64> {
    let refs = event
        .values
        .map(|(_, stowed)| stowed.refs.as_slice())
        .unwrap_or_default();
    let row = tx.get(
        "INSERT INTO engine_events
           (kind, namespace, schema, instance_id, seq, version, actor, service, at, change, cause_behavior, cause_event, cause_schedule, depth, value_refs)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         RETURNING cursor",
        &[
            SqlValue::from(event.kind),
            SqlValue::from(event.namespace),
            SqlValue::from(event.schema),
            SqlValue::from(event.instance_id),
            SqlValue::from(event.seq),
            SqlValue::from(event.version),
            SqlValue::from(event.actor.actor.as_str()),
            SqlValue::from(event.actor.service.as_deref()),
            SqlValue::Integer(event.at),
            SqlValue::from(event.change.as_deref()),
            SqlValue::from(event.cause.map(|cause| cause.behavior.as_str())),
            SqlValue::from(event.cause.and_then(|cause| cause.event)),
            SqlValue::from(event.cause.and_then(|cause| cause.schedule.as_deref())),
            SqlValue::Integer(event.cause.map_or(0, |cause| cause.depth)),
            SqlValue::from(refs_text(refs)),
        ],
    )?;
    let cursor = match row {
        Some(row) => row.i64("cursor")?,
        None => {
            return Err(Error::internal(
                "the event log returned no cursor for an appended event",
            ))
        }
    };
    if let Some((store, stowed)) = event.values {
        if !stowed.hashes.is_empty() {
            store.hold(
                tx,
                &ValueHolder {
                    namespace: event.namespace,
                    schema: event.schema,
                    holder: "event",
                    id: event.instance_id.unwrap_or_default(),
                    key: &cursor.to_string(),
                },
                &stowed.hashes,
            )?;
        }
    }
    tx.notify(event.namespace)?;
    Ok(cursor)
}

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

/// Appends a define or publish event and returns its cursor.
pub(crate) fn append_schema_event(tx: &Tx<'_>, event: &SchemaEvent<'_>) -> Result<i64> {
    append_event(
        tx,
        &NewEvent {
            kind: event.kind,
            namespace: event.namespace,
            schema: event.schema,
            instance_id: None,
            seq: None,
            version: event.version,
            actor: event.actor,
            at: event.at,
            change: Some(event.change.clone()),
            cause: None,
            values: None,
        },
    )
}

/// The sequence an instance's next event takes: one past its last event's,
/// which retention may have pruned and kept in the instance's base.
pub(crate) fn next_seq(
    tx: &Tx<'_>,
    namespace: &str,
    schema: &str,
    instance_id: &str,
) -> Result<i64> {
    let params = [
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(instance_id),
    ];
    let last = tx
        .get(
            "SELECT MAX(seq) AS seq FROM engine_events WHERE namespace = ? AND schema = ? AND instance_id = ?",
            &params,
        )?
        .map(|row| row.opt_i64("seq"))
        .transpose()?
        .flatten()
        .unwrap_or(0);
    let base = tx
        .get(
            "SELECT seq FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?",
            &params,
        )?
        .map(|row| row.i64("seq"))
        .transpose()?
        .unwrap_or(0);
    Ok(last.max(base) + 1)
}

/// One entry of the event log, as a read returns it.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct EngineEvent {
    /// The global cursor: every later event has a higher one.
    pub cursor: i64,
    pub kind: String,
    /// The instance's namespace; for a publish or a define, the namespace
    /// that holds the schema.
    pub namespace: String,
    pub schema: String,
    /// `None` for a publish and a define.
    pub instance_id: Option<String>,
    /// The instance's sequence; `None` for a publish and a define.
    pub seq: Option<i64>,
    /// The schema version the change was made with, or the one a publish
    /// made live; `None` for a define.
    pub version: Option<i64>,
    /// The subject of the principal that made the change.
    pub actor: String,
    /// The deployable of the service whose call made the change (D37).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub service: Option<String>,
    /// When, in epoch milliseconds.
    pub at: i64,
    /// create: the instance as a read returns it, `{ data, behaviors }`;
    /// update: a merge patch of that; operation: what was called and what
    /// it changed; delete: null; publish: the schema document; define:
    /// `{ hash }`.
    pub change: Value,
    /// What caused it, for an event the runner's work wrote.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cause: Option<EventCause>,
    /// The JSON pointers into change of the members the log keeps in the
    /// value store, each a ref; absent when it holds none.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value_refs: Option<Vec<String>>,
}

/// Where a read starts: after a cursor, or at the log's head.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum EventsAfter {
    /// Events after this cursor; 0, which no event has, is the start of
    /// the log: after retention, the oldest event it kept.
    Cursor(i64),
    /// The log's last event: an empty page whose next is its cursor.
    Head,
}

/// What a read of the log takes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ReadEvents<'a> {
    /// `default` when absent.
    pub namespace: Option<&'a str>,
    pub schema: Option<&'a str>,
    /// Only this instance's events; needs `schema`.
    pub instance_id: Option<&'a str>,
    /// The start of the log when absent.
    pub after: Option<EventsAfter>,
    /// How many events to scan, 50 by default and at most 500.
    pub limit: Option<i64>,
    /// Only events of these kinds; at least one.
    pub kinds: Option<Vec<String>>,
    /// Only operation events of these behaviors, by name; at least one.
    pub behaviors: Option<Vec<String>>,
    /// No operation events of operations by these names.
    pub exclude: Option<Vec<String>>,
}

/// One page of the log.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct EventPage {
    pub events: Vec<EngineEvent>,
    /// The cursor to read on from.
    pub next: i64,
    /// Whether the log held more events past this page when it was read.
    pub more: bool,
}

/// The `engine_events` columns [`to_event`] reads.
pub(crate) const EVENT_COLUMNS: &str = "cursor, kind, namespace, schema, instance_id, seq, version, actor, service, at, change, cause_behavior, cause_event, cause_schedule, depth, value_refs";

/// An `engine_events` row of [`EVENT_COLUMNS`].
pub(crate) fn to_event(row: &Row) -> Result<EngineEvent> {
    let change = match row.opt_text("change")? {
        None => Value::Null,
        Some(text) => serde_json::from_str(text)
            .map_err(|error| Error::internal(format!("an event's change is not JSON: {error}")))?,
    };
    let cause = match row.opt_text("cause_behavior")? {
        None => None,
        Some(behavior) => Some(EventCause {
            behavior: behavior.to_owned(),
            event: row.opt_i64("cause_event")?,
            schedule: row.opt_text("cause_schedule")?.map(str::to_owned),
            depth: row.i64("depth")?,
        }),
    };
    let refs = refs_of(row.opt_text("value_refs")?)?;
    Ok(EngineEvent {
        cursor: row.i64("cursor")?,
        kind: row.text("kind")?.to_owned(),
        namespace: row.text("namespace")?.to_owned(),
        schema: row.text("schema")?.to_owned(),
        instance_id: row.opt_text("instance_id")?.map(str::to_owned),
        seq: row.opt_i64("seq")?,
        version: row.opt_i64("version")?,
        actor: row.text("actor")?.to_owned(),
        service: row.opt_text("service")?.map(str::to_owned),
        at: row.i64("at")?,
        change,
        cause,
        value_refs: (!refs.is_empty()).then_some(refs),
    })
}

/// The cursor a log's head is: its last event's, or the last one retention
/// pruned, 0 for a log that never held one.
pub(crate) fn log_head(tx: &Tx<'_>) -> Result<i64> {
    let last = tx
        .get("SELECT MAX(cursor) AS head FROM engine_events", &[])?
        .map(|row| row.opt_i64("head"))
        .transpose()?
        .flatten()
        .unwrap_or(0);
    let pruned = tx
        .get("SELECT MAX(floor) AS head FROM engine_log_floors", &[])?
        .map(|row| row.opt_i64("head"))
        .transpose()?
        .flatten()
        .unwrap_or(0);
    Ok(last.max(pruned))
}

/// The earliest cursor a read of a namespace may start from: the last
/// cursor retention pruned of its events and, for a read that takes them,
/// of the shared namespace's publishes. 0 when retention has pruned none.
pub(crate) fn log_floor(
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    namespace: &str,
    with_shared: bool,
) -> Result<i64> {
    let own = tx
        .get(
            "SELECT floor FROM engine_log_floors WHERE namespace = ?",
            &[SqlValue::from(namespace)],
        )?
        .map(|row| row.i64("floor"))
        .transpose()?
        .unwrap_or(0);
    let shared = if with_shared {
        view.lookup(namespace).get(1).cloned()
    } else {
        None
    };
    let published = match shared {
        None => 0,
        Some(shared) => tx
            .get(
                "SELECT publish_floor FROM engine_log_floors WHERE namespace = ?",
                &[SqlValue::from(shared.as_str())],
            )?
            .map(|row| row.i64("publish_floor"))
            .transpose()?
            .unwrap_or(0),
    };
    Ok(own.max(published))
}

// One of a read's filters: its key, what it lists, the names it takes,
// and whether it may be empty (exclude, which then drops nothing).
struct FilterSpec {
    key: &'static str,
    what: &'static str,
    valid: fn(&str) -> bool,
    rule: &'static str,
    empty_allowed: bool,
}

fn is_event_kind(kind: &str) -> bool {
    EVENT_KINDS.contains(&kind)
}

const FILTERS: [FilterSpec; 3] = [
    FilterSpec {
        key: "kinds",
        what: "event kinds",
        valid: is_event_kind,
        rule: "one of create, update, delete, operation, publish, define",
        empty_allowed: false,
    },
    FilterSpec {
        key: "behaviors",
        what: "behavior names",
        valid: is_behavior_name,
        rule: "a behavior name",
        empty_allowed: false,
    },
    FilterSpec {
        key: "exclude",
        what: "operation names",
        valid: is_operation_name,
        rule: "an operation name, camelCase",
        empty_allowed: true,
    },
];

// filter_list checks one of a read's filters: absent, or a list of names
// each its rule takes; an empty list keeps nothing, so only exclude, which
// drops nothing then, may be empty. Each item is JSON, so a binding's
// non-string is refused as the TypeScript engine refuses it.
fn filter_list(
    spec: &FilterSpec,
    value: Option<&[Value]>,
) -> Result<Option<BTreeSet<String>>, EngineError> {
    let Some(value) = value else {
        return Ok(None);
    };
    if value.is_empty() && !spec.empty_allowed {
        return Err(EngineError::invalid_argument(format!(
            "{} lists no {}, so it would keep no event; leave it out to keep every one",
            spec.key, spec.what
        )));
    }
    let mut names = BTreeSet::new();
    for item in value {
        match item {
            Value::String(name) if (spec.valid)(name) => {
                names.insert(name.clone());
            }
            other => {
                return Err(EngineError::invalid_argument(format!(
                    "{}: {} is not {}",
                    spec.key,
                    json::stringify(other),
                    spec.rule
                )))
            }
        }
    }
    Ok((!names.is_empty()).then_some(names))
}

/// Checks a read's kinds, behaviors and exclude as JSON, in that order: a
/// value that is no list is refused, then the items as the typed read
/// checks them.
pub(crate) fn check_filters(
    given: [Option<&Value>; 3],
) -> Result<[Option<Vec<String>>; 3], EngineError> {
    let mut out: [Option<Vec<String>>; 3] = [None, None, None];
    for ((spec, value), slot) in FILTERS.iter().zip(given).zip(out.iter_mut()) {
        let Some(value) = value else {
            continue;
        };
        let Value::Array(items) = value else {
            return Err(EngineError::invalid_argument(format!(
                "{} is a list of {}",
                spec.key, spec.what
            )));
        };
        filter_list(spec, Some(items))?;
        *slot = Some(
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect(),
        );
    }
    Ok(out)
}

fn is_operation_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes[0].is_ascii_lowercase()
        && bytes.iter().all(u8::is_ascii_alphanumeric)
}

// What keeps a scanned row: its kinds, behaviors and exclude, checked.
struct EventFilter {
    kinds: Option<BTreeSet<String>>,
    behaviors: Option<BTreeSet<String>>,
    exclude: Option<BTreeSet<String>>,
}

impl EventFilter {
    fn new(options: &ReadEvents<'_>) -> Result<Self, EngineError> {
        let as_values = |list: &Option<Vec<String>>| {
            list.as_ref().map(|names| {
                names
                    .iter()
                    .map(|name| Value::from(name.as_str()))
                    .collect::<Vec<_>>()
            })
        };
        let [kinds, behaviors, exclude] =
            [&options.kinds, &options.behaviors, &options.exclude].map(as_values);
        Ok(EventFilter {
            kinds: filter_list(&FILTERS[0], kinds.as_deref())?,
            behaviors: filter_list(&FILTERS[1], behaviors.as_deref())?,
            exclude: filter_list(&FILTERS[2], exclude.as_deref())?,
        })
    }

    // keeps returns a scanned row's event, or None for one a filter drops;
    // kinds compare before the change is parsed.
    fn keeps(&self, row: &Row) -> Result<Option<EngineEvent>> {
        let kind = row.text("kind")?;
        if self
            .kinds
            .as_ref()
            .is_some_and(|kinds| !kinds.contains(kind))
        {
            return Ok(None);
        }
        if kind != "operation" && self.behaviors.is_some() {
            return Ok(None);
        }
        let event = to_event(row)?;
        if kind == "operation" {
            let behavior = event
                .change
                .get("behavior")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let operation = event
                .change
                .get("operation")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if self
                .behaviors
                .as_ref()
                .is_some_and(|behaviors| !behaviors.contains(behavior))
                || self
                    .exclude
                    .as_ref()
                    .is_some_and(|exclude| exclude.contains(operation))
            {
                return Ok(None);
            }
        }
        Ok(Some(event))
    }
}

/// The event log (`engine.events`).
pub struct Events<'e> {
    pub(crate) engine: &'e Engine,
}

impl Events<'_> {
    /// The page of events after a cursor that the principal may read and
    /// the filters keep. From `head` it returns no events, and the log's
    /// last cursor as next. With no cursor, or 0, it starts at the
    /// namespace's floor; from a cursor before the floor it is
    /// `cursor_expired`. A page scans at most its limit, kept or not, and
    /// its next is past every event it scanned.
    pub fn read(&self, principal: &Principal, options: &ReadEvents<'_>) -> Result<EventPage> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(options.namespace)?;
            let (from_head, after) = match options.after {
                Some(EventsAfter::Head) => (true, 0),
                Some(EventsAfter::Cursor(cursor)) => (false, cursor),
                None => (false, 0),
            };
            if after < 0 {
                return Err(cursor_refusal(&after.to_string()).into());
            }
            let limit = page_size(options.limit)?;
            let keeps = EventFilter::new(options)?;
            if let Some(schema) = options.schema {
                check_schema_name(schema)?;
                engine
                    .access
                    .require(principal, SchemaAction::Read, &namespace, schema)?;
            }
            if options.instance_id.is_some() && options.schema.is_none() {
                return Err(EngineError::invalid_argument(
                    "reading one instance's events needs its schema",
                )
                .into());
            }
            if from_head {
                return Ok(EventPage {
                    events: Vec::new(),
                    next: log_head(tx)?,
                    more: false,
                });
            }
            // A read from the start starts at the floor; one from a cursor
            // before it would miss what retention pruned. One instance's
            // events leave the shared namespace's publishes out.
            let floor = log_floor(tx, &view, &namespace, options.instance_id.is_none())?;
            if after > 0 && after < floor {
                return Err(
                    EngineError::cursor_expired(&namespace, after, floor, log_head(tx)?).into(),
                );
            }
            let from = after.max(floor);
            let rows = match (options.instance_id, options.schema) {
                (Some(instance_id), Some(schema)) => tx.all(
                    Sql::per(
                        format!("SELECT {EVENT_COLUMNS} FROM engine_events INDEXED BY engine_events_instance
         WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor > ? ORDER BY seq LIMIT ?"),
                        format!("SELECT {EVENT_COLUMNS} FROM engine_events
         WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor > ? ORDER BY seq LIMIT ?"),
                    ),
                    &[
                        SqlValue::from(namespace.as_str()),
                        SqlValue::from(schema),
                        SqlValue::from(instance_id),
                        SqlValue::Integer(from),
                        SqlValue::Integer(limit as i64 + 1),
                    ],
                )?,
                _ => namespace_rows(tx, &view, &namespace, options.schema, from, limit + 1)?,
            };
            let scanned = &rows[..rows.len().min(limit)];
            let mut readable: Vec<(String, bool)> = Vec::new();
            let mut events = Vec::new();
            for row in scanned {
                let Some(event) = keeps.keeps(row)? else {
                    continue;
                };
                let allowed = match readable.iter().find(|(schema, _)| *schema == event.schema) {
                    Some((_, allowed)) => *allowed,
                    None => {
                        let allowed = options.schema.is_some()
                            || engine.access.allows(
                                principal,
                                SchemaAction::Read,
                                &namespace,
                                &event.schema,
                            );
                        readable.push((event.schema.clone(), allowed));
                        allowed
                    }
                };
                if allowed {
                    events.push(event);
                }
            }
            // Next is past every event scanned, kept or not.
            let next = match scanned.last() {
                Some(last) => last.i64("cursor")?,
                None => from,
            };
            Ok(EventPage {
                events,
                next,
                more: rows.len() > limit,
            })
        })
    }

    /// The cursor of the log's last event, 0 for a log that never held one.
    /// Retention never moves it back.
    pub fn head(&self) -> Result<i64> {
        self.engine.db.read(log_head)
    }

    /// The earliest cursor a read of a namespace may start from: 0 until
    /// retention prunes the namespace's events or the shared namespace's
    /// publishes it reads, then the last cursor it pruned of them.
    pub fn floor(&self, namespace: Option<&str>) -> Result<i64> {
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(namespace)?;
            log_floor(tx, &view, &namespace, true)
        })
    }

    /// The storage's notifier's epoch: a count of the commits that
    /// appended events, which [`Events::wait`] blocks on.
    pub fn epoch(&self) -> u64 {
        self.engine.db.notifier().epoch()
    }

    /// Waits until a commit that appended events moves the epoch past
    /// `after`, or `timeout` elapses, and returns the epoch. On Postgres a
    /// commit in any process wakes it, through the storage's listener.
    pub fn wait(&self, after: u64, timeout: Duration) -> u64 {
        self.engine.db.notifier().wait(after, timeout)
    }

    /// A read that waits: the page after the cursor, as soon as the log
    /// holds an event past it (kept or not), or an empty page when
    /// `timeout` elapses first. From `head` it waits for events past the
    /// head the call found. It reads, then waits on the epoch it read
    /// before, so no commit between the two is missed.
    pub fn watch(
        &self,
        principal: &Principal,
        options: &ReadEvents<'_>,
        timeout: Duration,
    ) -> Result<EventPage> {
        let deadline = Instant::now() + timeout;
        let mut reading = options.clone();
        if reading.after == Some(EventsAfter::Head) {
            reading.after = Some(EventsAfter::Cursor(self.read(principal, options)?.next));
        }
        loop {
            let epoch = self.epoch();
            let page = self.read(principal, &reading)?;
            let from = match reading.after {
                Some(EventsAfter::Cursor(cursor)) => cursor,
                _ => 0,
            };
            let now = Instant::now();
            if !page.events.is_empty() || page.more || page.next > from || now >= deadline {
                return Ok(page);
            }
            self.wait(epoch, deadline - now);
        }
    }
}

/// The refusal of an event cursor, as the request wrote it.
pub fn cursor_refusal(given: &str) -> EngineError {
    EngineError::invalid_argument(format!(
        "an event cursor is a non-negative integer or head, got {given}"
    ))
}

// The events of a namespace after a cursor, optionally of one schema, with
// the shared namespace's publish events when the namespace looks names up
// there. Each arm is one indexed range read of at most `count` rows, and
// the merge keeps the first `count` by cursor.
fn namespace_rows(
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    namespace: &str,
    schema: Option<&str>,
    after: i64,
    count: usize,
) -> Result<Vec<Row>> {
    let by_schema = if schema.is_some() {
        " AND schema = ?"
    } else {
        ""
    };
    let arm = |holder: &str| {
        let mut params = vec![SqlValue::from(holder)];
        params.extend(schema.map(SqlValue::from));
        params.push(SqlValue::Integer(after));
        params.push(SqlValue::Integer(count as i64));
        params
    };
    let own = format!(
        "SELECT {EVENT_COLUMNS} FROM engine_events
      WHERE namespace = ?{by_schema} AND cursor > ? ORDER BY cursor LIMIT ?"
    );
    let Some(shared) = view.lookup(namespace).get(1).cloned() else {
        return Ok(tx.all(own, &arm(namespace))?);
    };
    let published = format!(
        "SELECT {EVENT_COLUMNS} FROM engine_events
      WHERE kind = 'publish' AND namespace = ?{by_schema} AND cursor > ? ORDER BY cursor LIMIT ?"
    );
    let mut params = arm(namespace);
    params.extend(arm(&shared));
    params.push(SqlValue::Integer(count as i64));
    Ok(tx.all(
        format!(
            "SELECT {EVENT_COLUMNS} FROM ({own}) AS own_events UNION ALL SELECT {EVENT_COLUMNS} FROM ({published}) AS shared_publishes ORDER BY cursor LIMIT ?"
        ),
        &params,
    )?)
}
