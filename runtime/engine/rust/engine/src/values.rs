//! The value store (D16, amended: a large value is stored once), the port
//! of `src/values/store.ts` and `src/values/values.ts`.
//!
//! A top-level member of what the engine writes (an instance's own fields
//! in its row, a change in the event log, or an object a behavior keeps in
//! its own tables) whose JSON is longer than a threshold (64 KiB by
//! default) is stored once, under the SHA-256 of its canonical JSON
//! ([`crate::json::canonical_json`]), and the row keeps a ref in its place:
//!
//! ```json
//! { "$value": "<64 hex digits>", "bytes": <the canonical JSON's length> }
//! ```
//!
//! Beside each such object the engine keeps the JSON pointers of the members
//! that hold a ref (`value_refs`), so a value that happens to look like a ref
//! is never taken for one. Reading the object back puts each value in its
//! place ([`ValueStore::fill`]). No value is longer than `maxBytes` (16 MiB
//! of canonical JSON by default): a write that would store one is refused
//! (`value_too_large`).
//!
//! Where the values live is configuration (D58), not a function the host
//! passes: the engine's own table `engine_payloads`, written in the
//! transaction that writes the row, by default; or a [`ValueDriver`] a
//! deployment's engine crate implements over other storage. A driver's
//! writes are outside the engine's transactions, so a rollback cannot undo
//! them: once the outermost transaction ends, committed or rolled back, the
//! store removes each value it wrote there that no holder references. That
//! settling takes the catalog's lock, so it never removes a value another
//! writer is about to hold.
//!
//! Who holds a value is `engine_payload_holders`: one row per value per
//! holder (an instance's row, an event, or a row of a behavior's tables, by
//! namespace and schema). A write records the values its row holds and
//! drops the ones it no longer holds; the value whose last holder goes is
//! removed, in that transaction for the engine's table, after it for a
//! driver. On Postgres several writers hold values at once (D58), so adding
//! a holder takes an advisory lock on the value's hash shared and removing
//! a value takes it exclusively: a value is never removed under a writer
//! that is about to hold it.
//!
//! A value never changes under its hash, so the store keeps the values it
//! read last parsed in memory (`cacheBytes`, 32 MiB by default), right in
//! every process. Values come back in canonical form: an object's members
//! sorted.
//!
//! Reading a value by its hash, through a schema that references it, comes
//! with the store's API.

use std::collections::{BTreeSet, HashMap, VecDeque};
use std::sync::{Arc, Mutex, PoisonError};
use std::thread::{self, ThreadId};

use serde_json::{Map, Value};

use superschematic_engine_storage::{lock_key, Db, Dialect, Lock, Sql, SqlValue, Tx};

use crate::errors::{EngineError, Error, Result};
use crate::json;

/// The member of a ref that names its value's hash.
pub const VALUE_REF_KEY: &str = "$value";

/// The threshold a member's JSON must exceed to be stored by hash: 64 KiB.
pub const DEFAULT_VALUE_THRESHOLD: u64 = 64 * 1024;

/// The least threshold an engine takes: 1 KiB.
pub const MIN_VALUE_THRESHOLD: u64 = 1024;

/// The default of [`ValueOptions::cache_bytes`]: 32 MiB.
pub const DEFAULT_VALUE_CACHE_BYTES: u64 = 32 * 1024 * 1024;

/// The default of [`ValueOptions::max_bytes`]: 16 MiB.
pub const DEFAULT_VALUE_MAX_BYTES: u64 = 16 * 1024 * 1024;

/// A failure of a deployment's value driver.
pub type DriverError = Box<dyn std::error::Error + Send + Sync>;

/// Where a deployment keeps the engine's values instead of
/// `engine_payloads`: storage of its own, which its engine crate reaches.
/// Every call is synchronous and runs on the engine call's thread, inside
/// its transaction but outside the database's (D58: config, never a host
/// callback across a binding).
pub trait ValueDriver: Send + Sync {
    /// The canonical JSON stored under a hash, or `None`.
    fn read(&self, hash: &str) -> std::result::Result<Option<String>, DriverError>;
    /// Stores a value's canonical JSON under its hash; a hash it has
    /// already is left as it is.
    fn write(&self, hash: &str, json: &str) -> std::result::Result<(), DriverError>;
    /// Removes the value under a hash, which no holder references any more.
    fn remove(&self, hash: &str) -> std::result::Result<(), DriverError>;
    /// The hashes it stores after `after`, in order, at most `limit`: what a
    /// sweep pages through. `None` for a driver that cannot list, which is
    /// not swept.
    fn list(
        &self,
        after: &str,
        limit: usize,
    ) -> Option<std::result::Result<Vec<String>, DriverError>> {
        let _ = (after, limit);
        None
    }
}

/// The value store's configuration (`EngineOptions.values`).
#[derive(Clone, Default)]
pub struct ValueOptions {
    /// A member whose JSON is longer than this many UTF-8 bytes is stored
    /// by hash: 65536 by default, at least 1024.
    pub threshold_bytes: Option<u64>,
    /// Where values live; `engine_payloads` when absent.
    pub driver: Option<Arc<dyn ValueDriver>>,
    /// How many bytes of values, by their canonical JSON, the engine keeps
    /// parsed in memory: 32 MiB by default, 0 for none.
    pub cache_bytes: Option<u64>,
    /// The longest value the engine stores, in UTF-8 bytes of its canonical
    /// JSON: 16 MiB by default, at least the threshold.
    pub max_bytes: Option<u64>,
}

impl std::fmt::Debug for ValueOptions {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ValueOptions")
            .field("threshold_bytes", &self.threshold_bytes)
            .field("driver", &self.driver.as_ref().map(|_| "custom"))
            .field("cache_bytes", &self.cache_bytes)
            .field("max_bytes", &self.max_bytes)
            .finish()
    }
}

/// The hash a ref names, or `None` for a value that is no ref.
pub(crate) fn ref_hash(value: &Value) -> Option<&str> {
    let hash = value.as_object()?.get(VALUE_REF_KEY)?.as_str()?;
    is_hash(hash).then_some(hash)
}

/// Whether a text is a value's hash: 64 lowercase hex digits.
pub fn is_hash(hash: &str) -> bool {
    hash.len() == 64
        && hash
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// The stored form of a list of pointers: JSON text, or `None` for none.
pub(crate) fn refs_text(refs: &[String]) -> Option<String> {
    if refs.is_empty() {
        return None;
    }
    let list: Vec<Value> = refs.iter().map(|at| Value::from(at.as_str())).collect();
    Some(json::stringify(&Value::Array(list)))
}

/// A `value_refs` column read: the pointers, or none.
pub(crate) fn refs_of(text: Option<&str>) -> Result<Vec<String>> {
    let Some(text) = text else {
        return Ok(Vec::new());
    };
    let value: Value = serde_json::from_str(text)
        .map_err(|error| Error::internal(format!("value_refs is not JSON: {error}")))?;
    Ok(value
        .as_array()
        .map(|refs| {
            refs.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default())
}

/// Who holds values: an instance's row, one of its events, or a row of a
/// behavior's tables.
pub(crate) struct ValueHolder<'a> {
    pub namespace: &'a str,
    pub schema: &'a str,
    /// `instance`, `event`, or the name of the behavior whose table holds
    /// it.
    pub holder: &'a str,
    /// The instance's id; '' for a behavior's row of no instance.
    pub id: &'a str,
    /// '' for an instance's row, an event's cursor, or the behavior's own
    /// key for its row.
    pub key: &'a str,
}

/// An object as stored: each large member a ref, with the pointers to them
/// and their hashes.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct Stowed {
    pub value: Map<String, Value>,
    /// JSON pointers to the members that hold a ref.
    pub refs: Vec<String>,
    pub hashes: BTreeSet<String>,
}

impl Stowed {
    /// Stowed parts put together under one object, at the keys given, after
    /// `base`'s members.
    pub fn join(base: Map<String, Value>, parts: Vec<(String, Stowed)>) -> Stowed {
        let mut joined = Stowed {
            value: base,
            ..Stowed::default()
        };
        for (key, part) in parts {
            joined.value.insert(key, Value::Object(part.value));
            joined.refs.extend(part.refs);
            joined.hashes.extend(part.hashes);
        }
        joined
    }
}

// One JSON pointer token, its leading slash included.
fn pointer_token(key: &str) -> String {
    format!("/{}", key.replace('~', "~0").replace('/', "~1"))
}

#[derive(Clone)]
struct CachedValue {
    value: Arc<Value>,
    bytes: u64,
}

// The values read last, parsed, up to a total of bytes, the oldest first.
struct ValueCache {
    capacity: u64,
    size: u64,
    order: VecDeque<String>,
    values: HashMap<String, CachedValue>,
}

impl ValueCache {
    fn new(capacity: u64) -> Self {
        ValueCache {
            capacity,
            size: 0,
            order: VecDeque::new(),
            values: HashMap::new(),
        }
    }

    fn get(&mut self, hash: &str) -> Option<CachedValue> {
        let value = self.values.get(hash)?.clone();
        self.order.retain(|each| each != hash);
        self.order.push_back(hash.to_owned());
        Some(value)
    }

    fn set(&mut self, hash: &str, value: CachedValue) {
        if value.bytes > self.capacity {
            return;
        }
        self.delete(hash);
        self.size += value.bytes;
        self.values.insert(hash.to_owned(), value);
        self.order.push_back(hash.to_owned());
        while self.size > self.capacity {
            let Some(oldest) = self.order.pop_front() else {
                break;
            };
            if let Some(gone) = self.values.remove(&oldest) {
                self.size -= gone.bytes;
            }
        }
    }

    fn delete(&mut self, hash: &str) {
        if let Some(gone) = self.values.remove(hash) {
            self.size -= gone.bytes;
            self.order.retain(|each| each != hash);
        }
    }
}

/// The value store an engine keeps: its threshold, its driver, its cache,
/// and the hashes a driver's writes leave to settle once each thread's
/// transaction ends.
pub(crate) struct ValueStore {
    pub threshold: u64,
    pub max_bytes: u64,
    driver: Option<Arc<dyn ValueDriver>>,
    cache: Mutex<ValueCache>,
    pending: Mutex<HashMap<ThreadId, BTreeSet<String>>>,
}

impl std::fmt::Debug for ValueStore {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ValueStore")
            .field("threshold", &self.threshold)
            .field("max_bytes", &self.max_bytes)
            .finish_non_exhaustive()
    }
}

impl ValueStore {
    /// The store the options configure; options no store could run with
    /// are refused as the TypeScript engine refuses them (TypeError).
    pub fn new(options: &ValueOptions) -> Result<Self> {
        let threshold = match options.threshold_bytes {
            None => DEFAULT_VALUE_THRESHOLD,
            Some(threshold) if threshold < MIN_VALUE_THRESHOLD => {
                return Err(Error::usage(format!(
                    "values.thresholdBytes is an integer of at least {MIN_VALUE_THRESHOLD}, got {threshold}"
                )))
            }
            Some(threshold) => threshold,
        };
        let max_bytes = match options.max_bytes {
            None => DEFAULT_VALUE_MAX_BYTES.max(threshold),
            Some(max) if max < threshold => {
                return Err(Error::usage(format!(
                    "values.maxBytes is an integer of at least values.thresholdBytes ({threshold}), got {max}"
                )))
            }
            Some(max) => max,
        };
        Ok(ValueStore {
            threshold,
            max_bytes,
            driver: options.driver.clone(),
            cache: Mutex::new(ValueCache::new(
                options.cache_bytes.unwrap_or(DEFAULT_VALUE_CACHE_BYTES),
            )),
            pending: Mutex::new(HashMap::new()),
        })
    }

    fn cache(&self) -> std::sync::MutexGuard<'_, ValueCache> {
        // Nothing panics while holding the lock, so a poisoned one is whole.
        self.cache.lock().unwrap_or_else(PoisonError::into_inner)
    }

    // over reports whether a member's JSON is longer than the threshold, in
    // UTF-8 bytes: only an object, an array or a string can be.
    fn over(&self, member: &Value) -> bool {
        matches!(
            member,
            Value::Object(_) | Value::Array(_) | Value::String(_)
        ) && json::stringify(member).len() as u64 > self.threshold
    }

    /// An object as it is stored: each top-level member whose JSON is
    /// longer than the threshold stored by hash and a ref in its place.
    /// `prefix` is the object's pointer in what is stored (an operation's
    /// params are at `/params`); `known` lists the pointers of members that
    /// hold a ref already, kept as they are; `inline` names members kept as
    /// they are whatever their length (the fields an index covers, in an
    /// instance's row). Members go in the order JavaScript iterates them.
    pub fn stow(
        &self,
        tx: &Tx<'_>,
        object: &Map<String, Value>,
        prefix: &str,
        known: &BTreeSet<String>,
        inline: &BTreeSet<String>,
    ) -> Result<Stowed> {
        let mut stowed = Stowed::default();
        for key in json::js_keys(object) {
            let member = &object[key];
            let at = format!("{prefix}{}", pointer_token(key));
            if known.contains(&at) {
                let hash = ref_hash(member).ok_or_else(|| {
                    Error::internal(format!(
                        "value store: {at} is listed as a ref and holds none"
                    ))
                })?;
                stowed.hashes.insert(hash.to_owned());
                stowed.value.insert(key.to_owned(), member.clone());
                stowed.refs.push(at);
                continue;
            }
            if !self.over(member) {
                stowed.value.insert(key.to_owned(), member.clone());
                continue;
            }
            let text = json::canonical_json(member).map_err(|error| Error::usage(error.0))?;
            let bytes = text.len() as u64;
            if bytes > self.max_bytes {
                return Err(EngineError::value_too_large(&at, bytes, self.max_bytes).into());
            }
            if inline.contains(key) {
                stowed.value.insert(key.to_owned(), member.clone());
                continue;
            }
            let hash = json::sha256_hex(&text);
            self.write(tx, &hash, &text)?;
            let mut reference = Map::new();
            reference.insert(VALUE_REF_KEY.into(), Value::from(hash.as_str()));
            reference.insert("bytes".into(), Value::from(bytes));
            stowed
                .value
                .insert(key.to_owned(), Value::Object(reference));
            stowed.refs.push(at);
            stowed.hashes.insert(hash);
        }
        Ok(stowed)
    }

    /// An instance as the log holds it, or a change of one: a merge patch of
    /// what a read returns, `{ data, behaviors }`. `data`'s own fields keep
    /// the refs `known` lists (pointers from the change's root, prefix
    /// included), and each behavior's fields go under its name, a large one
    /// by hash as an own field is. With `whole`, the change is the instance
    /// and holds both parts; otherwise a part that is absent or empty is
    /// left out.
    pub fn stow_change(
        &self,
        tx: &Tx<'_>,
        prefix: &str,
        data: Option<&Map<String, Value>>,
        behaviors: Option<&Map<String, Value>>,
        whole: bool,
        known: &BTreeSet<String>,
    ) -> Result<Stowed> {
        let mut parts = Vec::new();
        let none = BTreeSet::new();
        if let Some(data) = data.filter(|data| whole || !data.is_empty()) {
            parts.push((
                "data".to_owned(),
                self.stow(tx, data, &format!("{prefix}/data"), known, &none)?,
            ));
        }
        if let Some(behaviors) = behaviors.filter(|behaviors| whole || !behaviors.is_empty()) {
            let mut entries = Vec::new();
            for name in json::js_keys(behaviors) {
                let empty = Map::new();
                let fields = behaviors[name].as_object().unwrap_or(&empty);
                entries.push((
                    name.to_owned(),
                    self.stow(
                        tx,
                        fields,
                        &format!("{prefix}/behaviors{}", pointer_token(name)),
                        known,
                        &none,
                    )?,
                ));
            }
            parts.push(("behaviors".to_owned(), Stowed::join(Map::new(), entries)));
        }
        Ok(Stowed::join(Map::new(), parts))
    }

    /// Records that a holder references exactly these values: adds the new
    /// ones, drops the ones it no longer holds, and removes a dropped value
    /// nothing else holds. Call it in the write's transaction.
    pub fn hold(
        &self,
        tx: &Tx<'_>,
        holder: &ValueHolder<'_>,
        hashes: &BTreeSet<String>,
    ) -> Result<()> {
        let key = holder_key(holder);
        let held: Vec<String> = tx
            .all(
                Sql::per(
                    "SELECT hash FROM engine_payload_holders INDEXED BY engine_payload_holders_holder WHERE namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
                    "SELECT hash FROM engine_payload_holders WHERE namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
                ),
                &key,
            )?
            .iter()
            .map(|row| row.text("hash").map(str::to_owned))
            .collect::<Result<_, _>>()?;
        for hash in hashes {
            if !held.contains(hash) {
                lock_value(tx, hash, false)?;
                let mut params = vec![SqlValue::from(hash.as_str())];
                params.extend(key.iter().cloned());
                tx.run(
                    "INSERT INTO engine_payload_holders (hash, namespace, schema, holder, id, key) VALUES (?, ?, ?, ?, ?, ?)",
                    &params,
                )?;
            }
        }
        let dropped: Vec<String> = held
            .into_iter()
            .filter(|hash| !hashes.contains(hash))
            .collect();
        for hash in &dropped {
            let mut params = vec![SqlValue::from(hash.as_str())];
            params.extend(key.iter().cloned());
            tx.run(
                "DELETE FROM engine_payload_holders WHERE hash = ? AND namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
                &params,
            )?;
        }
        self.collect(tx, dropped)
    }

    /// Drops every value a holder holds: its row's, or with no key every
    /// row's of that holder on the instance id. A value nothing else holds
    /// is removed.
    pub fn release(
        &self,
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        holder: &str,
        id: &str,
        key: Option<&str>,
    ) -> Result<()> {
        let mut condition = "namespace = ? AND schema = ? AND holder = ? AND id = ?".to_owned();
        let mut params = vec![
            SqlValue::from(namespace),
            SqlValue::from(schema),
            SqlValue::from(holder),
            SqlValue::from(id),
        ];
        if let Some(key) = key {
            condition.push_str(" AND key = ?");
            params.push(SqlValue::from(key));
        }
        let hashes: Vec<String> = tx
            .all(
                Sql::per(
                    format!("SELECT DISTINCT hash FROM engine_payload_holders INDEXED BY engine_payload_holders_holder WHERE {condition}"),
                    format!("SELECT DISTINCT hash FROM engine_payload_holders WHERE {condition}"),
                ),
                &params,
            )?
            .iter()
            .map(|row| row.text("hash").map(str::to_owned))
            .collect::<Result<_, _>>()?;
        if hashes.is_empty() {
            return Ok(());
        }
        tx.run(
            format!("DELETE FROM engine_payload_holders WHERE {condition}"),
            &params,
        )?;
        self.collect(tx, hashes)
    }

    /// A copy of a stored object with each ref its pointers name replaced
    /// by its value.
    pub fn fill(&self, tx: &Tx<'_>, stored: &Value, refs: &[String]) -> Result<Value> {
        let mut filled = stored.clone();
        for at in refs {
            let tokens = parse_pointer(at)?;
            let mut member = &mut filled;
            for token in &tokens {
                member = member
                    .as_object_mut()
                    .and_then(|object| object.get_mut(token))
                    .ok_or_else(|| {
                        Error::internal(format!(
                            "value store: {at} does not name a member of the stored object"
                        ))
                    })?;
            }
            let hash = ref_hash(member)
                .ok_or_else(|| Error::internal(format!("value store: {at} holds no ref")))?
                .to_owned();
            let value = self.cached(tx, &hash)?.ok_or_else(|| {
                Error::internal(format!(
                    "value store: no value {hash}, which {at} of a stored object refers to"
                ))
            })?;
            *member = Value::clone(&value.value);
        }
        Ok(filled)
    }

    // cached is a value, parsed, from the cache or the driver.
    fn cached(&self, tx: &Tx<'_>, hash: &str) -> Result<Option<CachedValue>> {
        if let Some(hit) = self.cache().get(hash) {
            return Ok(Some(hit));
        }
        let text = match &self.driver {
            None => tx
                .get(
                    "SELECT value FROM engine_payloads WHERE hash = ?",
                    &[SqlValue::from(hash)],
                )?
                .map(|row| row.text("value").map(str::to_owned))
                .transpose()?,
            Some(driver) => driver.read(hash).map_err(driver_error)?,
        };
        let Some(text) = text else {
            return Ok(None);
        };
        let value: Value = serde_json::from_str(&text).map_err(|error| {
            Error::internal(format!("stored value {hash} is not JSON: {error}"))
        })?;
        let cached = CachedValue {
            value: Arc::new(value),
            bytes: text.len() as u64,
        };
        self.cache().set(hash, cached.clone());
        Ok(Some(cached))
    }

    // write stores a value: in engine_payloads in the write's transaction,
    // or through the driver, which keeps it whatever the transaction does,
    // so the store notes it for settle.
    fn write(&self, tx: &Tx<'_>, hash: &str, text: &str) -> Result<()> {
        lock_value(tx, hash, false)?;
        match &self.driver {
            None => {
                tx.run(
                    "INSERT INTO engine_payloads (hash, value, bytes) VALUES (?, ?, ?) ON CONFLICT (hash) DO NOTHING",
                    &[
                        SqlValue::from(hash),
                        SqlValue::from(text),
                        SqlValue::Integer(text.len() as i64),
                    ],
                )?;
            }
            Some(driver) => {
                driver.write(hash, text).map_err(driver_error)?;
                self.note(hash);
            }
        }
        Ok(())
    }

    // collect removes each value no holder references any more: in the
    // write's transaction for engine_payloads, after it for a driver.
    fn collect(&self, tx: &Tx<'_>, hashes: Vec<String>) -> Result<()> {
        for hash in hashes.into_iter().collect::<BTreeSet<_>>() {
            if self.driver.is_some() {
                self.note(&hash);
                continue;
            }
            lock_value(tx, &hash, true)?;
            if held(tx, &hash)? {
                continue;
            }
            tx.run(
                "DELETE FROM engine_payloads WHERE hash = ?",
                &[SqlValue::from(hash.as_str())],
            )?;
            self.cache().delete(&hash);
        }
        Ok(())
    }

    // note keeps a hash a driver wrote or whose holder went, to settle once
    // this thread's transaction ends.
    fn note(&self, hash: &str) {
        self.pending
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .entry(thread::current().id())
            .or_default()
            .insert(hash.to_owned());
    }

    /// Removes, through the driver, each value this thread's transaction
    /// wrote or dropped that no holder references now: what a rolled-back
    /// write left, and what a committed one let go. Call it once the
    /// transaction has ended; the engine's writes do. It checks in a read,
    /// then removes under the catalog's lock, which waits for every writer
    /// that might be about to hold one. A failure leaves the value for a
    /// sweep.
    pub fn settle(&self, db: &Db) {
        let Some(driver) = &self.driver else {
            return;
        };
        let pending = self
            .pending
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(&thread::current().id());
        let Some(pending) = pending else {
            return;
        };
        let unheld: Result<Vec<String>> = db.read(|tx| {
            let mut unheld = Vec::new();
            for hash in &pending {
                if !held(tx, hash)? {
                    unheld.push(hash.clone());
                }
            }
            Ok(unheld)
        });
        let Ok(unheld) = unheld else {
            return;
        };
        if unheld.is_empty() {
            return;
        }
        let _ = db.write(Lock::Catalog, |tx| {
            for hash in &unheld {
                if !held(tx, hash)? {
                    driver.remove(hash).map_err(driver_error)?;
                    self.cache().delete(hash);
                }
            }
            Ok::<_, Error>(())
        });
    }
}

fn holder_key(holder: &ValueHolder<'_>) -> [SqlValue; 5] {
    [
        SqlValue::from(holder.namespace),
        SqlValue::from(holder.schema),
        SqlValue::from(holder.holder),
        SqlValue::from(holder.id),
        SqlValue::from(holder.key),
    ]
}

// held reports whether any holder references a value.
fn held(tx: &Tx<'_>, hash: &str) -> Result<bool> {
    Ok(tx
        .get(
            "SELECT 1 AS held FROM engine_payload_holders WHERE hash = ? LIMIT 1",
            &[SqlValue::from(hash)],
        )?
        .is_some())
}

// lock_value takes a value's advisory lock on Postgres, shared to hold the
// value and exclusive to remove it; SQLite's one writer needs none.
fn lock_value(tx: &Tx<'_>, hash: &str, exclusive: bool) -> Result<()> {
    if tx.dialect() != Dialect::Postgres || tx.read_only() {
        return Ok(());
    }
    let key = lock_key(&["value", tx.postgres_schema().unwrap_or_default(), hash]);
    tx.run(
        if exclusive {
            "SELECT pg_advisory_xact_lock(?)"
        } else {
            "SELECT pg_advisory_xact_lock_shared(?)"
        },
        &[SqlValue::Integer(key)],
    )?;
    Ok(())
}

fn driver_error(error: DriverError) -> Error {
    Error::internal(format!("value driver: {error}"))
}

fn parse_pointer(at: &str) -> Result<Vec<String>> {
    if !at.starts_with('/') {
        return Err(Error::internal(format!(
            "value store: {at} is not a JSON pointer"
        )));
    }
    Ok(json::pointer_tokens(at))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_ref_names_a_hash() {
        let hash = "a".repeat(64);
        assert_eq!(
            ref_hash(&json!({"$value": hash, "bytes": 3})),
            Some(hash.as_str())
        );
        assert_eq!(ref_hash(&json!({"$value": "short"})), None);
        assert_eq!(ref_hash(&json!({"$value": "A".repeat(64)})), None);
        assert_eq!(ref_hash(&json!("text")), None);
        assert_eq!(refs_text(&[]), None);
        assert_eq!(refs_text(&["/slug".into()]).as_deref(), Some("[\"/slug\"]"));
        assert_eq!(refs_of(Some("[\"/slug\"]")).unwrap(), ["/slug"]);
        assert!(refs_of(None).unwrap().is_empty());
        assert_eq!(pointer_token("a/b~c"), "/a~1b~0c");
    }

    #[test]
    fn options_are_checked() {
        let refused = |options: ValueOptions| ValueStore::new(&options).unwrap_err().to_string();
        assert_eq!(
            refused(ValueOptions {
                threshold_bytes: Some(1023),
                ..ValueOptions::default()
            }),
            "values.thresholdBytes is an integer of at least 1024, got 1023"
        );
        assert_eq!(
            refused(ValueOptions {
                threshold_bytes: Some(2048),
                max_bytes: Some(1024),
                ..ValueOptions::default()
            }),
            "values.maxBytes is an integer of at least values.thresholdBytes (2048), got 1024"
        );
        let store = ValueStore::new(&ValueOptions {
            threshold_bytes: Some(DEFAULT_VALUE_MAX_BYTES * 2),
            ..ValueOptions::default()
        })
        .unwrap();
        assert_eq!(store.max_bytes, DEFAULT_VALUE_MAX_BYTES * 2);
    }

    #[test]
    fn the_cache_keeps_the_values_read_last_up_to_its_bytes() {
        let mut cache = ValueCache::new(10);
        let value = |bytes| CachedValue {
            value: Arc::new(json!(bytes)),
            bytes,
        };
        cache.set("a", value(4));
        cache.set("b", value(4));
        assert!(cache.get("a").is_some());
        cache.set("c", value(4));
        assert!(cache.get("b").is_none(), "the oldest goes first");
        assert!(cache.get("a").is_some() && cache.get("c").is_some());
        cache.set("big", value(11));
        assert!(
            cache.get("big").is_none(),
            "a value past the capacity is not kept"
        );
        cache.delete("a");
        assert_eq!(cache.size, 4);
    }
}
