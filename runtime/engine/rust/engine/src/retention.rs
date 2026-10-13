//! Event log retention, the port of `src/events/retention.ts` (D16,
//! amended: the log keeps what retention and the subscriptions need; D58).
//! Without it the log grows for good, and with it every large value an
//! event holds in the value store. An engine opened with
//! [`RetentionOptions`] prunes the log on the runner, as one more piece of
//! timed work: by age (`max_age_ms`, an event's `at` against the engine's
//! clock), by count (`max_events`, a namespace's events past its newest
//! `max_events`), or both, an event going when either lets it go. A
//! namespace's count is kept in memory, from one count of its events at the
//! first pass and the events appended since at each after, so a pass reads
//! what was appended, not the whole log.
//!
//! Retention prunes each namespace's events on their own, oldest first, in
//! batches of `batch_size`, each in its own transaction, and never past a
//! namespace's hold: the cursor of the least advanced subscription there
//! that has not handled the events after it, which the runner computes
//! ([`crate::runner`]). So pruning never takes an event a subscription still
//! has to handle, and one namespace's stuck subscription holds only that
//! namespace's events. A subscription that does not advance (halted, in an
//! archived namespace, or whose behavior the engine no longer runs) holds
//! like the others, unless `max_hold_ms` bounds it: then it holds no event
//! older than that, and once retention prunes past it, it halts with
//! `cursor_expired` when it next runs.
//!
//! A namespace's floor (`engine_log_floors`) is the last cursor retention
//! pruned of its events: a read from a cursor before it is
//! `cursor_expired`. The floor moves first in a batch's transaction, and the
//! trigger that keeps the log append-only lets a delete through only at or
//! before it. A pruned event takes its value holders with it; before it
//! goes, each instance event folds into its instance's base
//! (`engine_event_bases`), so a reaction's `before()` and a create after a
//! delete carry on from what retention kept.

use std::collections::{BTreeSet, HashMap};
use std::sync::{Mutex, PoisonError};

use serde_json::{Map, Value};

use superschematic_engine_storage::{Lock, Sql, SqlValue, Tx};

use crate::engine::Engine;
use crate::errors::{Error, Result};
use crate::events::{to_event, EngineEvent, EVENT_COLUMNS};
use crate::instances::reach::fold_event;
use crate::json;
use crate::values::{refs_of, refs_text, ValueHolder};

/// How long the event log keeps events, and how the runner prunes it
/// (`EngineOptions.retention`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RetentionOptions {
    /// Events older than this many milliseconds, by their `at`, are pruned.
    pub max_age_ms: Option<i64>,
    /// A namespace's events past its newest `max_events` are pruned.
    pub max_events: Option<i64>,
    /// The longest a subscription that does not advance holds retention.
    /// Absent, it holds until it advances.
    pub max_hold_ms: Option<i64>,
    /// How often the started runner prunes; 60000 by default, at least
    /// 1000.
    pub every_ms: Option<i64>,
    /// Events pruned in one transaction; 1000 by default, at most 10000.
    pub batch_size: Option<i64>,
}

/// How often the started runner prunes unless configured, in milliseconds.
pub const DEFAULT_RETENTION_EVERY_MS: i64 = 60_000;
/// Events pruned in one transaction unless configured.
pub const DEFAULT_RETENTION_BATCH_SIZE: i64 = 1_000;
/// The most events one transaction prunes.
pub const MAX_RETENTION_BATCH_SIZE: i64 = 10_000;
const MIN_RETENTION_EVERY_MS: i64 = 1_000;
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

impl RetentionOptions {
    /// Reads the options from the JSON the TypeScript engine takes
    /// (`{ maxAgeMs?, maxEvents?, maxHoldMs?, everyMs?, batchSize? }`), a
    /// member that is no integer refused in its words.
    pub fn from_json(value: &Value) -> Result<Self> {
        let Value::Object(members) = value else {
            return Err(Error::usage(
                "retention is { maxAgeMs?, maxEvents?, maxHoldMs?, everyMs?, batchSize? }",
            ));
        };
        let member = |key: &str| -> Result<Option<i64>> {
            match members.get(key) {
                None => Ok(None),
                Some(given) => integer_of(given).map(Some).ok_or_else(|| {
                    Error::usage(format!(
                        "retention.{key} is an integer from {} to {}, got {}",
                        minimum_of(key),
                        maximum_of(key),
                        crate::calls::js_string(given)
                    ))
                }),
            }
        };
        Ok(RetentionOptions {
            max_age_ms: member("maxAgeMs")?,
            max_events: member("maxEvents")?,
            max_hold_ms: member("maxHoldMs")?,
            every_ms: member("everyMs")?,
            batch_size: member("batchSize")?,
        })
    }

    /// The options with their defaults, checked as the TypeScript engine
    /// checks them: an age, a count or both, and every number in range.
    pub(crate) fn resolve(&self) -> Result<ResolvedRetention> {
        let check = |key: &str, value: Option<i64>| -> Result<Option<i64>> {
            match value {
                Some(value) if value < minimum_of(key) || value > maximum_of(key) => {
                    Err(Error::usage(format!(
                        "retention.{key} is an integer from {} to {}, got {value}",
                        minimum_of(key),
                        maximum_of(key)
                    )))
                }
                other => Ok(other),
            }
        };
        let max_age_ms = check("maxAgeMs", self.max_age_ms)?;
        let max_events = check("maxEvents", self.max_events)?;
        if max_age_ms.is_none() && max_events.is_none() {
            return Err(Error::usage(
                "retention keeps events by age (maxAgeMs), by count (maxEvents) or both: give at least one",
            ));
        }
        Ok(ResolvedRetention {
            max_age_ms,
            max_events,
            max_hold_ms: check("maxHoldMs", self.max_hold_ms)?,
            every_ms: check("everyMs", self.every_ms)?.unwrap_or(DEFAULT_RETENTION_EVERY_MS),
            batch_size: check("batchSize", self.batch_size)?
                .unwrap_or(DEFAULT_RETENTION_BATCH_SIZE),
        })
    }
}

fn minimum_of(key: &str) -> i64 {
    if key == "everyMs" {
        MIN_RETENTION_EVERY_MS
    } else {
        1
    }
}

fn maximum_of(key: &str) -> i64 {
    if key == "batchSize" {
        MAX_RETENTION_BATCH_SIZE
    } else {
        MAX_SAFE_INTEGER
    }
}

// integer_of is a JSON number that is a safe integer.
fn integer_of(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= MAX_SAFE_INTEGER as f64)
        .then_some(read as i64)
}

/// Retention options with their defaults filled in.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ResolvedRetention {
    pub max_age_ms: Option<i64>,
    pub max_events: Option<i64>,
    pub max_hold_ms: Option<i64>,
    pub every_ms: i64,
    pub batch_size: i64,
}

/// What holds a namespace's events, as the runner finds it: the least
/// advanced cursor of its subscriptions that advance (active, retrying, not
/// yet run), and of the ones that do not (halted, archived, unregistered).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct NamespaceHold {
    pub advancing: Option<i64>,
    pub stuck: Option<i64>,
}

/// How far retention has pruned one namespace's events.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct LogFloor {
    pub namespace: String,
    pub floor: i64,
    pub pruned: i64,
}

/// Every namespace's floor, by namespace.
pub(crate) fn floors_of(tx: &Tx<'_>) -> Result<Vec<LogFloor>> {
    let rows = tx.all(
        Sql::per(
            "SELECT namespace, floor, pruned FROM engine_log_floors ORDER BY namespace",
            "SELECT namespace, floor, pruned FROM engine_log_floors ORDER BY namespace COLLATE \"C\"",
        ),
        &[],
    )?;
    rows.iter()
        .map(|row| {
            Ok(LogFloor {
                namespace: row.text("namespace")?.to_owned(),
                floor: row.i64("floor")?,
                pruned: row.i64("pruned")?,
            })
        })
        .collect()
}

/// The engine's retention: its options and each namespace's count of kept
/// events, through the last cursor counted.
pub(crate) struct Retention {
    pub options: ResolvedRetention,
    counts: Mutex<HashMap<String, (i64, i64)>>,
}

impl Retention {
    pub fn new(options: ResolvedRetention) -> Self {
        Retention {
            options,
            counts: Mutex::new(HashMap::new()),
        }
    }

    /// The namespaces the log holds events of, with one indexed seek each:
    /// a namespace no longer configured keeps its events until retention
    /// prunes them too.
    pub fn namespaces_with_events(&self, engine: &Engine) -> Result<Vec<String>> {
        engine.read(|tx| {
            let mut names = Vec::new();
            let mut after = String::new();
            loop {
                let row = tx.get(
                    "SELECT namespace FROM engine_events WHERE namespace > ? ORDER BY namespace LIMIT 1",
                    &[SqlValue::from(after.as_str())],
                )?;
                let Some(row) = row else {
                    return Ok(names);
                };
                after = row.text("namespace")?.to_owned();
                names.push(after.clone());
            }
        })
    }

    /// Prunes the oldest events of one namespace that retention lets go,
    /// at most `batch_size` of them, none its hold keeps, in one
    /// transaction, and returns how many: fewer than `batch_size` when it
    /// pruned all it may for now.
    pub fn batch(&self, engine: &Engine, namespace: &str, hold: NamespaceHold) -> Result<i64> {
        let options = self.options;
        let floor = engine.read(|tx| floor_of(tx, namespace))?;
        // Without max_hold_ms a subscription that does not advance holds as
        // one that does.
        let firm = if options.max_hold_ms.is_none() {
            lowest(hold.advancing, hold.stuck)
        } else {
            hold.advancing
        };
        let limit = firm.unwrap_or(MAX_SAFE_INTEGER);
        if limit <= floor {
            return Ok(0);
        }
        let over = match options.max_events {
            None => 0,
            Some(max) => self.kept(engine, namespace, floor)? - max,
        };
        if options.max_age_ms.is_none() && over <= 0 {
            return Ok(0);
        }
        let count = if options.max_age_ms.is_none() {
            options.batch_size.min(over)
        } else {
            options.batch_size
        };
        let rows = engine.read(|tx| {
            Ok(tx.all(
                format!(
                    "SELECT {EVENT_COLUMNS} FROM engine_events WHERE namespace = ? AND cursor > ? AND cursor <= ? ORDER BY cursor LIMIT ?"
                ),
                &[
                    SqlValue::from(namespace),
                    SqlValue::Integer(floor),
                    SqlValue::Integer(limit),
                    SqlValue::Integer(count),
                ],
            )?)
        })?;
        let events = rows.iter().map(to_event).collect::<Result<Vec<_>>>()?;
        let stuck = if options.max_hold_ms.is_none() {
            None
        } else {
            hold.stuck
        };
        let gone = self.gone(engine, events, over, stuck);
        if gone.is_empty() {
            return Ok(0);
        }
        let pruned = gone.len() as i64;
        engine.write(Lock::Namespace(namespace), |tx| {
            prune(engine, tx, namespace, floor, &gone)
        })?;
        if let Some(count) = self
            .counts
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get_mut(namespace)
        {
            count.0 -= pruned;
        }
        Ok(pruned)
    }

    // gone keeps the run of events, oldest first, that retention lets go:
    // each older than max_age_ms or among the namespace's over oldest, and,
    // past a stuck subscription's cursor, no younger than max_hold_ms. It
    // stops at the first that stays, so the floor never passes one kept.
    fn gone(
        &self,
        engine: &Engine,
        events: Vec<EngineEvent>,
        over: i64,
        stuck: Option<i64>,
    ) -> Vec<EngineEvent> {
        let now = engine.clock.now();
        let mut gone = Vec::new();
        for (index, event) in events.into_iter().enumerate() {
            let let_go = self
                .options
                .max_age_ms
                .is_some_and(|age| event.at < now - age)
                || (index as i64) < over;
            let held = match (stuck, self.options.max_hold_ms) {
                (Some(stuck), Some(hold)) => event.cursor > stuck && event.at >= now - hold,
                _ => false,
            };
            if !let_go || held {
                break;
            }
            gone.push(event);
        }
        gone
    }

    // kept is how many events of a namespace the log holds after its
    // floor: the count it kept, plus the events appended since it last
    // counted. The first count reads every event the namespace keeps; each
    // after, only the new ones.
    fn kept(&self, engine: &Engine, namespace: &str, floor: i64) -> Result<i64> {
        let known = self
            .counts
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get(namespace)
            .copied();
        let since = known.map_or(floor, |(_, through)| through);
        let (appended, last) = engine.read(|tx| {
            let row = tx.get(
                "SELECT COUNT(*) AS count, MAX(cursor) AS last FROM engine_events WHERE namespace = ? AND cursor > ?",
                &[SqlValue::from(namespace), SqlValue::Integer(since)],
            )?;
            Ok(match row {
                None => (0, None),
                Some(row) => (row.i64("count")?, row.opt_i64("last")?),
            })
        })?;
        let kept = known.map_or(0, |(kept, _)| kept) + appended;
        let through = if appended > 0 {
            last.unwrap_or(since)
        } else {
            since
        };
        self.counts
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(namespace.to_owned(), (kept, through));
        Ok(kept)
    }
}

fn lowest(a: Option<i64>, b: Option<i64>) -> Option<i64> {
    match (a, b) {
        (Some(a), Some(b)) => Some(a.min(b)),
        (a, None) => a,
        (None, b) => b,
    }
}

fn floor_of(tx: &Tx<'_>, namespace: &str) -> Result<i64> {
    Ok(tx
        .get(
            "SELECT floor FROM engine_log_floors WHERE namespace = ?",
            &[SqlValue::from(namespace)],
        )?
        .map(|row| row.i64("floor"))
        .transpose()?
        .unwrap_or(0))
}

// prune removes a namespace's oldest events, in cursor order, inside a
// transaction: each instance event folds into its instance's base, the
// events' value holders go, the floor moves past them, and then they do.
fn prune(
    engine: &Engine,
    tx: &Tx<'_>,
    namespace: &str,
    floor: i64,
    events: &[EngineEvent],
) -> Result<()> {
    let mut by_instance: Vec<((String, String), Vec<&EngineEvent>)> = Vec::new();
    for event in events {
        let Some(id) = &event.instance_id else {
            continue;
        };
        let key = (event.schema.clone(), id.clone());
        match by_instance.iter_mut().find(|(known, _)| *known == key) {
            Some((_, list)) => list.push(event),
            None => by_instance.push((key, vec![event])),
        }
    }
    for (_, instance_events) in &by_instance {
        fold(engine, tx, namespace, instance_events)?;
    }
    for event in events {
        if event.value_refs.is_some() {
            engine.values.release(
                tx,
                namespace,
                &event.schema,
                "event",
                event.instance_id.as_deref().unwrap_or_default(),
                Some(&event.cursor.to_string()),
            )?;
        }
    }
    let last = events.last().map_or(floor, |event| event.cursor);
    let published = events
        .iter()
        .filter(|event| event.kind == "publish")
        .map(|event| event.cursor)
        .max()
        .unwrap_or(0);
    tx.run(
        Sql::per(
            "INSERT INTO engine_log_floors (namespace, floor, publish_floor, pruned) VALUES (?, ?, ?, ?)
             ON CONFLICT (namespace) DO UPDATE SET floor = excluded.floor,
               publish_floor = MAX(engine_log_floors.publish_floor, excluded.publish_floor), pruned = engine_log_floors.pruned + excluded.pruned",
            "INSERT INTO engine_log_floors (namespace, floor, publish_floor, pruned) VALUES (?, ?, ?, ?)
             ON CONFLICT (namespace) DO UPDATE SET floor = excluded.floor,
               publish_floor = GREATEST(engine_log_floors.publish_floor, excluded.publish_floor), pruned = engine_log_floors.pruned + excluded.pruned",
        ),
        &[
            SqlValue::from(namespace),
            SqlValue::Integer(last),
            SqlValue::Integer(published),
            SqlValue::Integer(events.len() as i64),
        ],
    )?;
    tx.run(
        "DELETE FROM engine_events WHERE namespace = ? AND cursor > ? AND cursor <= ?",
        &[
            SqlValue::from(namespace),
            SqlValue::Integer(floor),
            SqlValue::Integer(last),
        ],
    )?;
    Ok(())
}

// fold folds one instance's pruned events, in sequence order, into its
// base: the instance as the log had it after the last of them.
fn fold(engine: &Engine, tx: &Tx<'_>, namespace: &str, events: &[&EngineEvent]) -> Result<()> {
    let Some(first) = events.first() else {
        return Ok(());
    };
    let schema = first.schema.as_str();
    let instance_id = first.instance_id.as_deref().unwrap_or_default();
    let key = [
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(instance_id),
    ];
    let base = tx.get(
        "SELECT data, value_refs FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?",
        &key,
    )?;
    let mut data: Option<Value> = match &base {
        Some(row) => match row.opt_text("data")? {
            None => None,
            Some(text) => {
                let stored: Value = serde_json::from_str(text).map_err(|error| {
                    Error::internal(format!("an event base is not JSON: {error}"))
                })?;
                Some(
                    engine
                        .values
                        .fill(tx, &stored, &refs_of(row.opt_text("value_refs")?)?)?,
                )
            }
        },
        None => None,
    };
    for event in events {
        let change = engine.values.fill(
            tx,
            &event.change,
            event.value_refs.as_deref().unwrap_or_default(),
        )?;
        data = fold_event(data, &event.kind, change);
    }
    let holder = ValueHolder {
        namespace,
        schema,
        holder: "base",
        id: instance_id,
        key: "",
    };
    let seq = events.last().and_then(|event| event.seq).unwrap_or(0);
    let Some(state) = data else {
        engine.values.hold(tx, &holder, &BTreeSet::new())?;
        let mut params = key.to_vec();
        params.push(SqlValue::Integer(seq));
        tx.run(
            "INSERT INTO engine_event_bases (namespace, schema, instance_id, seq, data, value_refs) VALUES (?, ?, ?, ?, NULL, NULL)
             ON CONFLICT (namespace, schema, instance_id) DO UPDATE SET seq = excluded.seq, data = NULL, value_refs = NULL",
            &params,
        )?;
        return Ok(());
    };
    let empty = Map::new();
    let own = state.get("data").and_then(Value::as_object);
    let behaviors = state.get("behaviors").and_then(Value::as_object);
    let stowed = engine.values.stow_change(
        tx,
        "",
        Some(own.unwrap_or(&empty)),
        Some(behaviors.unwrap_or(&empty)),
        true,
        &BTreeSet::new(),
    )?;
    engine.values.hold(tx, &holder, &stowed.hashes)?;
    let mut params = key.to_vec();
    params.push(SqlValue::Integer(seq));
    params.push(SqlValue::from(json::stringify(&Value::Object(
        stowed.value.clone(),
    ))));
    params.push(SqlValue::from(refs_text(&stowed.refs)));
    tx.run(
        "INSERT INTO engine_event_bases (namespace, schema, instance_id, seq, data, value_refs) VALUES (?, ?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, instance_id) DO UPDATE SET seq = excluded.seq, data = excluded.data, value_refs = excluded.value_refs",
        &params,
    )?;
    Ok(())
}
