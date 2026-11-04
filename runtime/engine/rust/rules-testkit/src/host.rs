//! The reference host (D103): the adapter every language writes once, here
//! over memory instead of a database. It keeps each type's defined and
//! published versions (a [`TypeSpec`] each, its behavior uses and their
//! configs), the rows (own fields, owned fields by use id, a version), the
//! sub-table rows, the outbox of the behaviors' events, the armed
//! deadlines, the change log of the core's writes, one entry per row a
//! change set writes, and the outbox of changes a build-time project's
//! change trigger writes ([`QueuedChange`]): one per write of a row of a type
//! whose writes its behaviors hear, the core's and a raw one alike, with the
//! row's own and owned fields before and after it.
//!
//! It runs the host loop the core's README describes: it gives each
//! command a request id, calls the core, answers its reads from memory (a
//! row, whether a type has rows, a type's live version, a row's sub-table
//! rows, the rows of a type whose own field holds a key), grants its lock
//! keys (nothing to take in memory, where one writer holds), and applies the
//! change set all or nothing. It runs the queued changes as the API's worker
//! runs a table's change queue, each a change command of its own, first in
//! first out ([`MemoryHost::run_changes`]). It holds every row
//! the change set writes to the version it expects; when one has moved, it
//! runs the command again from fresh state, at most [`MAX_ATTEMPTS`] times,
//! then refuses it as `busy`. Whether a deadline is still armed is the
//! host's to check, never the core's (D103): a deadline command runs only
//! while its deadline is armed at the time it fell due for, checked before
//! each run, and its apply spends that arming right after the command's
//! row writes and before the change set's own deadline changes, so one
//! cleared or moved since does not fall due on its old arming, and one the
//! command arms again outlives it. It is generic: it knows no behavior, and
//! maps logical names onto memory the way a database adapter maps them
//! onto its layout.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};

use superschematic_rules::{
    key_of, merge_patch, Action, Answer, Answers, Caller, ChangeSet, Code, Command, Core,
    DeadlineChange, KeyedRow, LinkedRows, LockKey, OutboxEvent, Outcome, ReadRequest, Refusal, Row,
    RowChange, RowImage, RowRef, RowWrite, Step, SubRow, SubRowChange, TypeSpec, WriteKind,
    MAX_ROUNDS,
};

/// How many times the host runs a command whose row moved before it
/// refuses the command as `busy`.
pub const MAX_ATTEMPTS: usize = 8;

/// How many queued changes [`MemoryHost::run_changes`] runs in one call, the
/// ones the changes it runs queue included.
pub const MAX_CHANGES: usize = 1024;

/// One row of a type, as the host keeps it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct StoredRow {
    /// The author's fields.
    pub own: Map<String, Value>,
    /// The behaviors' fields, by use id and field; a field with no value is
    /// absent.
    pub owned: BTreeMap<String, Map<String, Value>>,
    /// The version the next write is held to: 1 at the create, one more
    /// with each write.
    pub version: i64,
    /// The row's place in the type's creation order.
    pub ordinal: u64,
}

impl StoredRow {
    /// The row as the core reads it.
    pub fn as_row(&self) -> Row {
        Row {
            own: self.own.clone(),
            owned: self.owned.clone(),
            version: self.version,
        }
    }

    /// The row's own and owned fields as a change trigger writes them, a
    /// field with no value absent.
    pub fn image(&self) -> RowImage {
        let present = |fields: &Map<String, Value>| -> Map<String, Value> {
            fields
                .iter()
                .filter(|(_, value)| !value.is_null())
                .map(|(name, value)| (name.clone(), value.clone()))
                .collect()
        };
        RowImage {
            own: present(&self.own),
            owned: self
                .owned
                .iter()
                .map(|(use_id, fields)| (use_id.clone(), present(fields)))
                .filter(|(_, fields)| !fields.is_empty())
                .collect(),
        }
    }
}

/// A write of a row the change trigger of a type whose writes its behaviors
/// hear wrote to the outbox (D103): the row's type and key, and the change.
#[derive(Clone, Debug, PartialEq)]
pub struct QueuedChange {
    pub type_name: String,
    pub key: String,
    pub change: RowChange,
}

/// A type's versions: the ones published, in order, and the one defined
/// and not published yet.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TypeVersions {
    pub published: Vec<TypeSpec>,
    pub draft: Option<TypeSpec>,
}

/// Where a sub-table row sits: the type and key of the row that owns it,
/// the use whose sub-table it is, the sub-table's declared name and the
/// row's key in it.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct SubRowKey {
    pub type_name: String,
    pub key: String,
    pub use_id: String,
    pub table: String,
    pub sub_key: String,
}

/// An event in the outbox: the row it is about, its key filled in, and the
/// event.
#[derive(Clone, Debug, PartialEq)]
pub struct Outboxed {
    pub type_name: String,
    pub key: String,
    pub event: OutboxEvent,
}

/// A deadline armed on a row, by its use and name.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct DeadlineKey {
    pub type_name: String,
    pub key: String,
    pub use_id: String,
    pub name: String,
}

/// When an armed deadline falls due and what it was armed with.
#[derive(Clone, Debug, PartialEq)]
pub struct Armed {
    pub at: i64,
    pub payload: Value,
}

/// One entry of the change log: the change a command made to a row, as a
/// build-time project's trigger writes it to the outbox (D103), with the
/// command that made it. A command that writes several rows logs one entry
/// for each.
#[derive(Clone, Debug, PartialEq)]
pub struct Logged {
    pub type_name: String,
    pub key: String,
    /// The command, its caller and its time.
    pub command: Command,
    /// The row's write, its key filled in.
    pub write: RowWrite,
    /// The owned fields the row held before the write, by use id; empty
    /// for a create.
    pub before: BTreeMap<String, Map<String, Value>>,
}

impl Logged {
    /// `create`, `update`, `delete`, `operation` or `deadline`: a row's
    /// update is the command's kind.
    pub fn kind(&self) -> &'static str {
        match &self.write {
            RowWrite::Create { .. } => "create",
            RowWrite::Delete { .. } => "delete",
            _ => match &self.command.action {
                Action::Operation { .. } => "operation",
                Action::Deadline { .. } => "deadline",
                _ => "update",
            },
        }
    }

    /// The own fields the write set: a create's, an update's patch; empty
    /// for an operation, a deadline and a delete.
    pub fn own(&self) -> Map<String, Value> {
        match &self.write {
            RowWrite::Create { own, .. } | RowWrite::Update { own, .. } => own.clone(),
            _ => Map::new(),
        }
    }

    /// The owned fields the write set, by use id; empty for a delete.
    pub fn owned(&self) -> BTreeMap<String, Map<String, Value>> {
        match &self.write {
            RowWrite::Create { owned, .. } | RowWrite::Update { owned, .. } => owned.clone(),
            _ => BTreeMap::new(),
        }
    }

    /// The owned fields the write changed, by use id: each it set to a
    /// value other than the one the row held before, as the engine's event
    /// recorded a change, a JSON object that was one before by the merge
    /// patch between them, member by member (the engine's `diff_patch`); a
    /// use whose fields it left as they were left out. A create's are every
    /// one it set.
    pub fn changed(&self) -> BTreeMap<String, Map<String, Value>> {
        self.owned()
            .into_iter()
            .map(|(use_id, fields)| {
                let held = self.before.get(&use_id);
                let fields: Map<String, Value> = fields
                    .into_iter()
                    .filter_map(|(field, value)| {
                        let was = held
                            .and_then(|held| held.get(&field))
                            .unwrap_or(&Value::Null);
                        if *was == value {
                            return None;
                        }
                        let changed = match (was, &value) {
                            (Value::Object(was), Value::Object(now)) => {
                                Value::Object(diff_patch(was, now))
                            }
                            _ => value,
                        };
                        Some((field, changed))
                    })
                    .collect();
                (use_id, fields)
            })
            .filter(|(_, fields)| !fields.is_empty())
            .collect()
    }
}

// diff_patch is the merge patch that turns one object into another: each
// member that is new or changed, a nested object changed member by member,
// and null for a member the second no longer has, as the engine's event
// recorded the change of an object field.
fn diff_patch(before: &Map<String, Value>, after: &Map<String, Value>) -> Map<String, Value> {
    let mut patch = Map::new();
    for (key, value) in after {
        let previous = before.get(key);
        if previous == Some(value) {
            continue;
        }
        let changed = match (previous, value) {
            (Some(Value::Object(previous)), Value::Object(value)) => {
                Value::Object(diff_patch(previous, value))
            }
            _ => value.clone(),
        };
        patch.insert(key.clone(), changed);
    }
    for key in before.keys() {
        if !after.contains_key(key) {
            patch.insert(key.clone(), Value::Null);
        }
    }
    patch
}

/// What the host keeps: types, rows, sub-table rows, the outbox, the armed
/// deadlines, the change log, and the changes its hearing types' triggers
/// queued.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Store {
    types: BTreeMap<String, TypeVersions>,
    rows: BTreeMap<String, BTreeMap<String, StoredRow>>,
    sub_rows: BTreeMap<SubRowKey, Map<String, Value>>,
    outbox: Vec<Outboxed>,
    deadlines: BTreeMap<DeadlineKey, Armed>,
    log: Vec<Logged>,
    next_ordinal: u64,
    /// The types whose writes a behavior hears, which a change trigger
    /// queues a change of each write for.
    hearing: BTreeSet<String>,
    changes: Vec<QueuedChange>,
}

/// Why a change set did not apply.
#[derive(Clone, Debug, PartialEq)]
pub enum ApplyError {
    /// A row is not at the version the change set expects, or is gone: the
    /// command runs again from fresh state.
    Moved,
    /// The change set cannot apply as it stands (a key that exists), and
    /// running it again would not change that.
    Refused(Refusal),
}

impl Store {
    /// A store with nothing in it.
    pub fn new() -> Self {
        Self::default()
    }

    // ---- types ----

    /// The names of the types defined, published or not.
    pub fn type_names(&self) -> Vec<String> {
        self.types.keys().cloned().collect()
    }

    /// A type's versions.
    pub fn versions(&self, type_name: &str) -> Option<&TypeVersions> {
        self.types.get(type_name)
    }

    /// The version of a type commands run on: the last one published.
    pub fn live(&self, type_name: &str) -> Option<&TypeSpec> {
        self.types
            .get(type_name)
            .and_then(|versions| versions.published.last())
    }

    /// The number of the live version, from 1; 0 for a type never
    /// published.
    pub fn live_version(&self, type_name: &str) -> usize {
        self.types
            .get(type_name)
            .map_or(0, |versions| versions.published.len())
    }

    /// The version defined and not published yet.
    pub fn draft(&self, type_name: &str) -> Option<&TypeSpec> {
        self.types
            .get(type_name)
            .and_then(|versions| versions.draft.as_ref())
    }

    // ---- rows ----

    /// A row by its type and key.
    pub fn row(&self, type_name: &str, key: &str) -> Option<&StoredRow> {
        self.rows.get(type_name).and_then(|rows| rows.get(key))
    }

    /// A type's rows with their keys, in creation order.
    pub fn rows(&self, type_name: &str) -> Vec<(&str, &StoredRow)> {
        let mut rows: Vec<(&str, &StoredRow)> = self
            .rows
            .get(type_name)
            .map(|rows| rows.iter().map(|(key, row)| (key.as_str(), row)).collect())
            .unwrap_or_default();
        rows.sort_by_key(|(_, row)| row.ordinal);
        rows
    }

    /// Whether a type has a row.
    pub fn has_rows(&self, type_name: &str) -> bool {
        self.rows
            .get(type_name)
            .is_some_and(|rows| !rows.is_empty())
    }

    /// Writes a row as a database writes one past the adapter (a repair,
    /// a test's competing writer) at a time, in epoch milliseconds: the host
    /// moves its version on, and the type's change trigger, when its writes
    /// are heard, queues the write (an update, or an insert of a new row).
    pub fn write_row(&mut self, type_name: &str, key: &str, own: Map<String, Value>, at: i64) {
        let before = self.row(type_name, key).map(StoredRow::image);
        let ordinal = self.next_ordinal;
        let rows = self.rows.entry(type_name.to_owned()).or_default();
        match rows.get_mut(key) {
            Some(row) => {
                row.own = own;
                row.version += 1;
            }
            None => {
                rows.insert(
                    key.to_owned(),
                    StoredRow {
                        own,
                        version: 1,
                        ordinal,
                        ..StoredRow::default()
                    },
                );
                self.next_ordinal += 1;
            }
        }
        let after = self.row(type_name, key).map(StoredRow::image);
        self.queue(type_name, key, at, before, after);
    }

    /// Sets fields a behavior owns on a row as a database writes them past
    /// the adapter at a time, in epoch milliseconds: an operator's repair,
    /// which nothing refuses (D103), or a test's raw SQL. Null clears a
    /// field. The version stays where it was, as a raw write that leaves the
    /// `@optimistic` column alone leaves it, the change log, which logs the
    /// core's writes, gets no entry, and the type's change trigger, when its
    /// writes are heard, queues the update. Returns whether the row exists.
    pub fn write_owned(
        &mut self,
        type_name: &str,
        key: &str,
        owned: &BTreeMap<String, Map<String, Value>>,
        at: i64,
    ) -> bool {
        let Some(row) = self
            .rows
            .get_mut(type_name)
            .and_then(|rows| rows.get_mut(key))
        else {
            return false;
        };
        let before = row.image();
        set_owned(&mut row.owned, owned);
        let after = row.image();
        self.queue(type_name, key, at, Some(before), Some(after));
        true
    }

    /// Deletes a row as a database deletes one past the adapter at a time,
    /// in epoch milliseconds: its sub-table rows go with it, as their
    /// foreign key cascades, and its deadlines stay, a queue's messages
    /// holding no key of the row, until its type's change trigger, when its
    /// writes are heard, has the delete run. Returns whether the row
    /// existed.
    pub fn delete_row(&mut self, type_name: &str, key: &str, at: i64) -> bool {
        let Some(row) = self
            .rows
            .get_mut(type_name)
            .and_then(|rows| rows.remove(key))
        else {
            return false;
        };
        self.sub_rows
            .retain(|held, _| !(held.type_name == type_name && held.key == key));
        self.queue(type_name, key, at, Some(row.image()), None);
        true
    }

    // ---- the changes the triggers queue ----

    /// Whether a type's writes are heard: its change trigger queues a
    /// change of each.
    pub fn hears(&self, type_name: &str) -> bool {
        self.hearing.contains(type_name)
    }

    /// The types whose writes are heard, which the host sets from its core
    /// whenever a type's live version changes.
    pub fn set_hearing(&mut self, types: BTreeSet<String>) {
        self.hearing = types;
    }

    /// The changes queued and not run yet, first in first out.
    pub fn changes(&self) -> &[QueuedChange] {
        &self.changes
    }

    /// Takes the first change queued, as a worker claims a change queue's
    /// head.
    pub fn take_change(&mut self) -> Option<QueuedChange> {
        (!self.changes.is_empty()).then(|| self.changes.remove(0))
    }

    // queue writes a change of a row of a type whose writes are heard, as
    // its change trigger does: an insert with no row before, a delete with
    // none after, and an update only when its own or owned fields moved.
    fn queue(
        &mut self,
        type_name: &str,
        key: &str,
        at: i64,
        before: Option<RowImage>,
        after: Option<RowImage>,
    ) {
        if !self.hears(type_name) {
            return;
        }
        let write = match (&before, &after) {
            (None, Some(_)) => WriteKind::Insert,
            (Some(_), None) => WriteKind::Delete,
            (Some(before), Some(after)) if before != after => WriteKind::Update,
            _ => return,
        };
        self.changes.push(QueuedChange {
            type_name: type_name.to_owned(),
            key: key.to_owned(),
            change: RowChange {
                write,
                at,
                before,
                after,
            },
        });
    }

    /// A row's sub-table rows, by use id, sub-table and key.
    pub fn sub_rows(&self, type_name: &str, key: &str) -> Vec<(&SubRowKey, &Map<String, Value>)> {
        self.sub_rows
            .iter()
            .filter(|(at, _)| at.type_name == type_name && at.key == key)
            .collect()
    }

    /// The rows of one sub-table of one use on a row, as the core reads
    /// them, in key order.
    pub fn sub_table(&self, type_name: &str, key: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        self.sub_rows
            .iter()
            .filter(|(at, _)| {
                at.type_name == type_name
                    && at.key == key
                    && at.use_id == use_id
                    && at.table == table
            })
            .map(|(at, values)| SubRow::new(at.sub_key.clone(), values.clone()))
            .collect()
    }

    /// A row's sub-table rows as the core reads them, by use id, then by
    /// table, each table's rows in key order.
    pub fn sub_tables(
        &self,
        type_name: &str,
        key: &str,
    ) -> BTreeMap<String, BTreeMap<String, Vec<SubRow>>> {
        let mut tables: BTreeMap<String, BTreeMap<String, Vec<SubRow>>> = BTreeMap::new();
        for (at, values) in self.sub_rows(type_name, key) {
            tables
                .entry(at.use_id.clone())
                .or_default()
                .entry(at.table.clone())
                .or_default()
                .push(SubRow::new(at.sub_key.clone(), values.clone()));
        }
        tables
    }

    /// The outbox, in the order the events were written.
    pub fn outbox(&self) -> &[Outboxed] {
        &self.outbox
    }

    /// The armed deadlines.
    pub fn deadlines(&self) -> &BTreeMap<DeadlineKey, Armed> {
        &self.deadlines
    }

    /// The time a deadline is armed at; `None` when it is not armed.
    pub fn armed_at(&self, at: &DeadlineKey) -> Option<i64> {
        self.deadlines.get(at).map(|armed| armed.at)
    }

    /// The deadlines due at a time, earliest first.
    pub fn due(&self, now: i64) -> Vec<(DeadlineKey, Armed)> {
        let mut due: Vec<(DeadlineKey, Armed)> = self
            .deadlines
            .iter()
            .filter(|(_, armed)| armed.at <= now)
            .map(|(at, armed)| (at.clone(), armed.clone()))
            .collect();
        due.sort_by_key(|(_, armed)| armed.at);
        due
    }

    /// The change log, in the order the changes were applied.
    pub fn log(&self) -> &[Logged] {
        &self.log
    }

    /// The change log of one row.
    pub fn log_of(&self, type_name: &str, key: &str) -> Vec<&Logged> {
        self.log
            .iter()
            .filter(|entry| entry.type_name == type_name && entry.key == key)
            .collect()
    }

    // ---- what the core asks ----

    /// The answer to one of the core's reads, from memory; `None` for a
    /// kind of read this host does not answer.
    pub fn answer(&self, request: &ReadRequest) -> Option<Answer> {
        match request {
            ReadRequest::Row { type_name, key } => {
                Some(Answer::Row(self.row(type_name, key).map(StoredRow::as_row)))
            }
            ReadRequest::HasRows { type_name } => Some(Answer::HasRows(self.has_rows(type_name))),
            ReadRequest::Spec { type_name } => Some(Answer::Spec(self.live(type_name).cloned())),
            ReadRequest::SubRows {
                type_name,
                key,
                use_id,
                table,
            } => Some(Answer::SubRows(
                self.sub_table(type_name, key, use_id, table),
            )),
            ReadRequest::Linked {
                type_name,
                field,
                key,
                after,
                limit,
            } => Some(Answer::Linked(self.linked(
                type_name,
                field,
                key,
                after.as_deref(),
                *limit,
            ))),
            ReadRequest::Holding {
                type_name,
                use_id,
                field,
                value,
                after,
                limit,
            } => Some(Answer::Linked(self.holding(
                type_name,
                use_id,
                field,
                value,
                after.as_deref(),
                *limit,
            ))),
            _ => None,
        }
    }

    /// The rows of a type whose use's owned field holds a value, compared
    /// as JSON, in key order (by code point), after `after` when given, at
    /// most `limit`, and whether more follow: a holding read's answer.
    pub fn holding(
        &self,
        type_name: &str,
        use_id: &str,
        field: &str,
        value: &Value,
        after: Option<&str>,
        limit: usize,
    ) -> LinkedRows {
        let mut rows: Vec<KeyedRow> = self
            .rows
            .get(type_name)
            .into_iter()
            .flatten()
            .filter(|(row_key, row)| {
                row.owned
                    .get(use_id)
                    .and_then(|fields| fields.get(field))
                    .is_some_and(|held| !held.is_null() && held == value)
                    && after.is_none_or(|after| row_key.as_str() > after)
            })
            .map(|(row_key, row)| KeyedRow {
                key: row_key.clone(),
                row: row.as_row(),
            })
            .collect();
        let more = rows.len() > limit;
        rows.truncate(limit);
        LinkedRows { rows, more }
    }

    /// The rows of a type whose own field holds a key, in key order (by
    /// code point), after `after` when given, at most `limit`, and whether
    /// more follow: a linked read's answer.
    pub fn linked(
        &self,
        type_name: &str,
        field: &str,
        key: &str,
        after: Option<&str>,
        limit: usize,
    ) -> LinkedRows {
        let mut rows: Vec<KeyedRow> = self
            .rows
            .get(type_name)
            .into_iter()
            .flatten()
            .filter(|(row_key, row)| {
                row.own.get(field).and_then(key_of).as_deref() == Some(key)
                    && after.is_none_or(|after| row_key.as_str() > after)
            })
            .map(|(row_key, row)| KeyedRow {
                key: row_key.clone(),
                row: row.as_row(),
            })
            .collect();
        let more = rows.len() > limit;
        rows.truncate(limit);
        LinkedRows { rows, more }
    }

    // ---- applying ----

    /// Applies a command's change set, all or nothing: every row it writes,
    /// each held to the version it expects, with a change log entry for
    /// each and, for a type whose writes are heard, a queued change; a row a
    /// change command found gone, whose deadlines go, and its sub-table rows
    /// unless the row came back; for a deadline command, the spend of its
    /// arming, which applies only while the deadline is still armed at the
    /// time the command names; then its sub-table rows, its events and its
    /// deadlines, those of a row it deletes left out, since the row's go with
    /// it. The
    /// command runs on a row of `type_name`. Returns the version of the
    /// command's own row after the write; `None` when the change set does
    /// not write that row, or deletes it.
    pub fn apply(
        &mut self,
        type_name: &str,
        command: &Command,
        changes: &ChangeSet,
    ) -> Result<Option<i64>, ApplyError> {
        let mut next = self.clone();
        let version = next.apply_in_place(type_name, command, changes)?;
        *self = next;
        Ok(version)
    }

    fn apply_in_place(
        &mut self,
        type_name: &str,
        command: &Command,
        changes: &ChangeSet,
    ) -> Result<Option<i64>, ApplyError> {
        let own_key = command.action.key();
        let mut version = None;
        let mut deleted: Vec<(String, String)> = Vec::new();
        for write in &changes.rows {
            let key = keyed(&write.row())?;
            if let RowWrite::Gone { type_name, .. } = write {
                self.gone(type_name, &key);
                continue;
            }
            let held = self
                .row(write.type_name(), &key)
                .filter(|_| !matches!(write, RowWrite::Create { .. }));
            let image = held.map(StoredRow::image);
            let before = held.map(|row| row.owned.clone()).unwrap_or_default();
            let after = self.write(write.type_name(), &key, write)?;
            if write.type_name() == type_name && Some(key.as_str()) == own_key {
                version = after;
            }
            if matches!(write, RowWrite::Delete { .. }) {
                deleted.push((write.type_name().to_owned(), key.clone()));
            }
            let left = self.row(write.type_name(), &key).map(StoredRow::image);
            self.queue(write.type_name(), &key, command.now, image, left);
            let mut logged = write.clone();
            if let RowWrite::Create { key: slot, .. } = &mut logged {
                *slot = Some(key.clone());
            }
            self.log.push(Logged {
                type_name: write.type_name().to_owned(),
                key,
                command: command.clone(),
                write: logged,
                before,
            });
        }
        if let Action::Deadline {
            key,
            use_id,
            name,
            at,
            ..
        } = &command.action
        {
            // The deadline fell due: it is spent, right after the row's
            // writes and before the change set's own deadline changes, so
            // an arming the command makes again outlives the spend. One
            // cleared or moved since the run read it is not this arming's,
            // and the command runs again to find so.
            let armed = DeadlineKey {
                type_name: type_name.to_owned(),
                key: key.clone(),
                use_id: use_id.clone(),
                name: name.clone(),
            };
            if self.armed_at(&armed) != Some(*at) {
                return Err(ApplyError::Moved);
            }
            self.deadlines.remove(&armed);
        }
        // A delete takes the row's sub-table rows and deadlines with it, so
        // the change set's own for it go nowhere.
        let gone = |row: &RowRef, key: &str| {
            deleted
                .iter()
                .any(|(name, deleted)| *name == row.type_name && deleted == key)
        };
        for sub in &changes.sub_rows {
            let key = keyed(&sub.row)?;
            if gone(&sub.row, &key) {
                continue;
            }
            let at = SubRowKey {
                type_name: sub.row.type_name.clone(),
                key,
                use_id: sub.use_id.clone(),
                table: sub.table.clone(),
                sub_key: sub.key.clone(),
            };
            self.write_sub_row(at, &sub.change)?;
        }
        // Every event is written, whatever rows the change set writes.
        for event in &changes.events {
            self.outbox.push(Outboxed {
                type_name: event.row.type_name.clone(),
                key: keyed(&event.row)?,
                event: event.clone(),
            });
        }
        for change in &changes.deadlines {
            let key = keyed(change.row())?;
            if gone(change.row(), &key) {
                continue;
            }
            self.write_deadline(&key, change)?;
        }
        Ok(version)
    }

    // write applies one row's write and returns its version after.
    fn write(
        &mut self,
        type_name: &str,
        key: &str,
        write: &RowWrite,
    ) -> Result<Option<i64>, ApplyError> {
        let ordinal = self.next_ordinal;
        let rows = self.rows.entry(type_name.to_owned()).or_default();
        match write {
            RowWrite::Create { own, owned, .. } => {
                if rows.contains_key(key) {
                    return Err(ApplyError::Refused(Refusal::new(
                        Code::Conflict,
                        format!("{type_name} {key} already exists"),
                    )));
                }
                let mut row = StoredRow {
                    own: own.clone(),
                    version: 1,
                    ordinal,
                    ..StoredRow::default()
                };
                set_owned(&mut row.owned, owned);
                rows.insert(key.to_owned(), row);
                self.next_ordinal += 1;
                Ok(Some(1))
            }
            RowWrite::Update {
                expected_version,
                own,
                owned,
                ..
            } => {
                let row = rows.get_mut(key).ok_or(ApplyError::Moved)?;
                if row.version != *expected_version {
                    return Err(ApplyError::Moved);
                }
                row.own = merge_patch(&row.own, own);
                set_owned(&mut row.owned, owned);
                row.version += 1;
                Ok(Some(row.version))
            }
            RowWrite::Delete {
                expected_version, ..
            } => {
                match rows.get(key) {
                    Some(row) if row.version == *expected_version => {}
                    _ => return Err(ApplyError::Moved),
                }
                rows.remove(key);
                // The row's sub-table rows and deadlines go with it.
                self.sub_rows
                    .retain(|at, _| !(at.type_name == type_name && at.key == key));
                self.deadlines
                    .retain(|at, _| !(at.type_name == type_name && at.key == key));
                Ok(None)
            }
            other => Err(ApplyError::Refused(unknown("row write", other))),
        }
    }

    // gone applies a change command's row found gone: its deadlines go, and
    // its sub-table rows with them, unless the row came back since.
    fn gone(&mut self, type_name: &str, key: &str) {
        if self.row(type_name, key).is_some() {
            return;
        }
        self.sub_rows
            .retain(|at, _| !(at.type_name == type_name && at.key == key));
        self.deadlines
            .retain(|at, _| !(at.type_name == type_name && at.key == key));
    }

    fn write_sub_row(&mut self, at: SubRowKey, change: &SubRowChange) -> Result<(), ApplyError> {
        let named = |at: &SubRowKey| {
            format!(
                "{} {} {}.{} {}",
                at.type_name, at.key, at.use_id, at.table, at.sub_key
            )
        };
        match change {
            SubRowChange::Insert { values } => {
                if self.sub_rows.contains_key(&at) {
                    return Err(ApplyError::Refused(Refusal::new(
                        Code::Conflict,
                        format!("sub-table row {} already exists", named(&at)),
                    )));
                }
                let values = values
                    .iter()
                    .filter(|(_, value)| !value.is_null())
                    .map(|(column, value)| (column.clone(), value.clone()))
                    .collect();
                self.sub_rows.insert(at, values);
            }
            SubRowChange::Update { values } => {
                let Some(row) = self.sub_rows.get_mut(&at) else {
                    return Err(ApplyError::Refused(Refusal::not_found(format!(
                        "sub-table row {} does not exist",
                        named(&at)
                    ))));
                };
                // A null is no value, as a NULL column reads.
                for (column, value) in values {
                    if value.is_null() {
                        row.remove(column);
                    } else {
                        row.insert(column.clone(), value.clone());
                    }
                }
            }
            SubRowChange::Delete => {
                if self.sub_rows.remove(&at).is_none() {
                    return Err(ApplyError::Refused(Refusal::not_found(format!(
                        "sub-table row {} does not exist",
                        named(&at)
                    ))));
                }
            }
            other => return Err(ApplyError::Refused(unknown("sub-table change", other))),
        }
        Ok(())
    }

    fn write_deadline(&mut self, key: &str, change: &DeadlineChange) -> Result<(), ApplyError> {
        let at = DeadlineKey {
            type_name: change.row().type_name.clone(),
            key: key.to_owned(),
            use_id: change.use_id().to_owned(),
            name: change.name().to_owned(),
        };
        match change {
            DeadlineChange::Arm {
                at: due, payload, ..
            } => {
                self.deadlines.insert(
                    at,
                    Armed {
                        at: *due,
                        payload: payload.clone(),
                    },
                );
            }
            DeadlineChange::Clear { .. } => {
                self.deadlines.remove(&at);
            }
            other => return Err(ApplyError::Refused(unknown("deadline change", other))),
        }
        Ok(())
    }
}

// keyed is a row's key: the host fills a create's before it runs it, so a
// row with none reached it from a create it did not key.
fn keyed(row: &RowRef) -> Result<String, ApplyError> {
    row.key.clone().ok_or_else(|| {
        ApplyError::Refused(Refusal::invalid_argument(format!(
            "a create of {} reached the host without a key",
            row.type_name
        )))
    })
}

// unknown refuses a kind of change this host does not take (the core's
// enums are open), as its own failure.
fn unknown(what: &str, change: &impl std::fmt::Debug) -> Refusal {
    Refusal::internal(format!(
        "the reference host takes no {what} of this kind: {change:?}"
    ))
}

// set_owned writes owned fields onto a row's: a null clears one.
fn set_owned(
    row: &mut BTreeMap<String, Map<String, Value>>,
    owned: &BTreeMap<String, Map<String, Value>>,
) {
    for (use_id, fields) in owned {
        let slot = row.entry(use_id.clone()).or_default();
        for (field, value) in fields {
            if value.is_null() {
                slot.remove(field);
            } else {
                slot.insert(field.clone(), value.clone());
            }
        }
    }
}

/// What a command did once its change set applied.
#[derive(Clone, Debug, PartialEq)]
pub struct Applied {
    /// The row's key: the create's, or the one the command names.
    pub key: String,
    /// The row's version after the write; `None` when the command wrote
    /// no row or deleted it.
    pub version: Option<i64>,
    /// How many times the command ran: one more for each time its row
    /// moved before the apply.
    pub attempts: usize,
    /// The lock keys the last run held.
    pub locks: Vec<LockKey>,
    /// The change set applied.
    pub changes: ChangeSet,
}

impl Applied {
    /// What the command returns: an operation's result, null for the rest.
    pub fn result(&self) -> &Value {
        &self.changes.result
    }
}

/// The reference host: a core, the store it answers from and applies to,
/// the id source a create without an id draws from, and the count its
/// request ids are made from.
#[derive(Debug)]
pub struct MemoryHost {
    core: Core,
    store: Store,
    attempts: usize,
    ids: IdSource,
    requests: u64,
}

/// The ids the host makes for a create that names none: the ones queued
/// first, in order, then `id-1`, `id-2`, and on.
#[derive(Clone, Debug, Default)]
struct IdSource {
    queued: std::collections::VecDeque<String>,
    drawn: u64,
}

impl IdSource {
    fn next(&mut self) -> String {
        if let Some(id) = self.queued.pop_front() {
            return id;
        }
        self.drawn += 1;
        format!("id-{}", self.drawn)
    }
}

impl MemoryHost {
    /// A host with an empty store over a core.
    pub fn new(core: Core) -> Self {
        Self::with_store(core, Store::new())
    }

    /// A host over a core and a store that holds what an earlier host
    /// wrote (an engine reopened on the same file).
    pub fn with_store(core: Core, store: Store) -> Self {
        let mut host = MemoryHost {
            core,
            store,
            attempts: MAX_ATTEMPTS,
            ids: IdSource::default(),
            requests: 0,
        };
        host.hear();
        host
    }

    /// The types whose writes are heard, as the core's behaviors say from
    /// the types' live versions: a type one of whose uses' behaviors hears
    /// its row's writes, and a type a listener of another type names
    /// ([`Core::listeners`]) through a relation of it to that type. A type
    /// whose configs the core refuses names no listener.
    pub fn hearing(&self) -> BTreeSet<String> {
        let store = &self.store;
        let mut hearing = BTreeSet::new();
        for name in store.type_names() {
            let Some(spec) = store.live(&name) else {
                continue;
            };
            let hears = spec.uses.iter().any(|entry| {
                self.core
                    .declaration(&entry.behavior)
                    .is_some_and(|declaration| declaration.hears_changes)
            });
            if hears {
                hearing.insert(name.clone());
            }
            for listen in self.core.listeners(spec).unwrap_or_default() {
                let points_here = store
                    .live(&listen.type_name)
                    .and_then(|heard| heard.relation(&listen.link))
                    .and_then(|field| field.relation.as_ref())
                    .is_some_and(|relation| relation.type_name == name);
                if points_here {
                    hearing.insert(listen.type_name);
                }
            }
        }
        hearing
    }

    // hear sets the store's hearing types from the live versions.
    fn hear(&mut self) {
        let hearing = self.hearing();
        self.store.set_hearing(hearing);
    }

    /// The host with a bound of its own on how many times a command runs
    /// (at least once).
    pub fn with_attempts(mut self, attempts: usize) -> Self {
        self.attempts = attempts.max(1);
        self
    }

    pub fn core(&self) -> &Core {
        &self.core
    }

    pub fn store(&self) -> &Store {
        &self.store
    }

    /// The store, for a write past the adapter.
    pub fn store_mut(&mut self) -> &mut Store {
        &mut self.store
    }

    /// The store, the host given up.
    pub fn into_store(self) -> Store {
        self.store
    }

    /// Queues ids for the creates that name none, ahead of the ones the
    /// host makes.
    pub fn queue_ids<I: Into<String>>(&mut self, ids: impl IntoIterator<Item = I>) {
        self.ids.queued.extend(ids.into_iter().map(Into::into));
    }

    // ---- types ----

    /// Defines a type's next version: the core's define check of its uses
    /// and configs and, when the type has a live version, its config-change
    /// check from it, so a version that could not be published is refused
    /// when it is defined. The version replaces one defined and not
    /// published yet.
    pub fn define(&mut self, spec: TypeSpec, caller: &Caller, now: i64) -> Result<(), Refusal> {
        let command = self.define_command(&spec, caller, now);
        self.check(&spec, &command)?;
        let versions = self.store.types.entry(spec.name.clone()).or_default();
        versions.draft = Some(spec);
        Ok(())
    }

    /// The check [`MemoryHost::define`] runs a version with: the define
    /// check for a type with no live version, else the publish check from
    /// the live one.
    pub fn define_command(&self, spec: &TypeSpec, caller: &Caller, now: i64) -> Command {
        let action = match self.store.live(&spec.name).cloned() {
            Some(from) => Action::Publish { from: Some(from) },
            None => Action::Define,
        };
        Command::new(caller.clone(), now, action)
    }

    /// Publishes the version of a type defined last: the core's publish
    /// check from the live version, which reads whether the type has rows
    /// now. Returns the new version's number; `None` when no version waits.
    pub fn publish(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
    ) -> Result<Option<usize>, Refusal> {
        let Some((draft, command)) = self.publish_command(type_name, caller, now) else {
            return Ok(None);
        };
        self.check(&draft, &command)?;
        let versions = self.store.types.entry(type_name.to_owned()).or_default();
        versions.published.push(draft);
        versions.draft = None;
        let published = versions.published.len();
        self.hear();
        Ok(Some(published))
    }

    /// The version [`MemoryHost::publish`] would publish and the check it
    /// runs it with, the publish check from the live version; `None` when
    /// no version waits.
    pub fn publish_command(
        &self,
        type_name: &str,
        caller: &Caller,
        now: i64,
    ) -> Option<(TypeSpec, Command)> {
        let draft = self.store.draft(type_name).cloned()?;
        let from = self.store.live(type_name).cloned();
        Some((
            draft,
            Command::new(caller.clone(), now, Action::Publish { from }),
        ))
    }

    /// Runs a define or publish check of a spec, which writes nothing and
    /// keeps no version: what [`MemoryHost::define`] and
    /// [`MemoryHost::publish`] run before they keep one.
    pub fn check(&self, spec: &TypeSpec, command: &Command) -> Result<(), Refusal> {
        match self.settle(spec, command)?.0 {
            Outcome::Refused(refusal) => Err(refusal),
            Outcome::Changed(_) => Ok(()),
        }
    }

    // ---- commands ----

    /// Runs a command on a type's live version and applies its change set.
    /// A command that carries no request id gets one from the host
    /// (`request-1`, `request-2`, and on), kept for every time it runs, and
    /// a create that names no id gets one too.
    pub fn execute(&mut self, type_name: &str, command: Command) -> Result<Applied, Refusal> {
        self.execute_racing(type_name, command, |_, _| {})
    }

    /// Runs a command as [`MemoryHost::execute`] does, with a writer that
    /// commits between each run and its apply, as another transaction
    /// would: `race` gets the store and the attempt, from 0. A test of the
    /// version loop moves a row there.
    pub fn execute_racing(
        &mut self,
        type_name: &str,
        command: Command,
        mut race: impl FnMut(&mut Store, usize),
    ) -> Result<Applied, Refusal> {
        let command = self.prepare(command);
        for attempt in 0..self.attempts {
            // Each run reads fresh state, the type's live version with it,
            // and a deadline is run only while it is armed as it fell due.
            self.still_due(type_name, &command)?;
            let spec = self.store.live(type_name).cloned().ok_or_else(|| {
                Refusal::not_found(format!("type {type_name} has no published version"))
            })?;
            let (outcome, locks) = self.settle(&spec, &command)?;
            let changes = match outcome {
                Outcome::Refused(refusal) => return Err(refusal),
                Outcome::Changed(changes) => *changes,
            };
            race(&mut self.store, attempt);
            match self.store.apply(type_name, &command, &changes) {
                Ok(version) => {
                    let key = command.action.key().unwrap_or_default().to_owned();
                    return Ok(Applied {
                        key,
                        version,
                        attempts: attempt + 1,
                        locks,
                        changes,
                    });
                }
                Err(ApplyError::Moved) => continue,
                Err(ApplyError::Refused(refusal)) => return Err(refusal),
            }
        }
        Err(Refusal::new(
            Code::Busy,
            format!(
                "{} of {type_name}{}: the row kept moving while the command ran; it ran {} times and nothing was written, so it may be tried again",
                command.action.name(),
                command
                    .action
                    .key()
                    .map(|key| format!(" {key}"))
                    .unwrap_or_default(),
                self.attempts
            ),
        ))
    }

    /// The command as [`MemoryHost::execute`] runs it: a request id from
    /// the host (`request-1`, `request-2`, and on) when it carries none,
    /// and an id from the host's id source for a create that names none. A
    /// command it has prepared comes back as it was, so a caller that
    /// records what ran (a transcript) prepares it first.
    pub fn prepare(&mut self, mut command: Command) -> Command {
        if let Action::Create { id: id @ None, .. } = &mut command.action {
            *id = Some(self.ids.next());
        }
        if command.request.is_empty() {
            self.requests += 1;
            command.request = format!("request-{}", self.requests);
        }
        command
    }

    /// Runs every deadline due at a time, earliest first, as the caller,
    /// each a command of its own at that time: what a worker of the
    /// project runs (D103). A deadline that one run before it cleared or
    /// moved is not run.
    pub fn run_due(
        &mut self,
        now: i64,
        caller: &Caller,
    ) -> Vec<(DeadlineKey, Result<Applied, Refusal>)> {
        let due = self.store.due(now);
        let mut ran = Vec::new();
        for (at, armed) in due {
            if self.store.armed_at(&at) != Some(armed.at) {
                continue;
            }
            let command = Command::new(
                caller.clone(),
                now,
                Action::Deadline {
                    key: at.key.clone(),
                    use_id: at.use_id.clone(),
                    name: at.name.clone(),
                    at: armed.at,
                    payload: armed.payload,
                },
            );
            let outcome = self.execute(&at.type_name, command);
            ran.push((at, outcome));
        }
        ran
    }

    /// The change command a queued change runs as, as a worker that claimed
    /// it runs it: the caller and the time.
    pub fn change_command(queued: &QueuedChange, caller: &Caller, now: i64) -> Command {
        Command::new(
            caller.clone(),
            now,
            Action::Change {
                key: queued.key.clone(),
                change: queued.change.clone(),
            },
        )
    }

    /// Runs the queued changes, first in first out, each a change command of
    /// its own at a time, as the caller: what the API's worker runs of a
    /// table's change queue (D103). The changes the commands queue run too,
    /// at most [`MAX_CHANGES`] in one call. A change refused is taken off, as
    /// a message that goes dead.
    pub fn run_changes(
        &mut self,
        caller: &Caller,
        now: i64,
    ) -> Vec<(QueuedChange, Result<Applied, Refusal>)> {
        let mut ran = Vec::new();
        while ran.len() < MAX_CHANGES {
            let Some(queued) = self.store.take_change() else {
                break;
            };
            let command = Self::change_command(&queued, caller, now);
            let outcome = self.execute(&queued.type_name, command);
            ran.push((queued, outcome));
        }
        ran
    }

    // still_due refuses a deadline command whose deadline is no longer
    // armed at the time it names, or is not due yet at the command's time.
    fn still_due(&self, type_name: &str, command: &Command) -> Result<(), Refusal> {
        let Action::Deadline {
            key,
            use_id,
            name,
            at,
            ..
        } = &command.action
        else {
            return Ok(());
        };
        let armed = DeadlineKey {
            type_name: type_name.to_owned(),
            key: key.clone(),
            use_id: use_id.clone(),
            name: name.clone(),
        };
        if self.store.armed_at(&armed) != Some(*at) {
            return Err(Refusal::not_found(format!(
                "{type_name} {key} has no deadline {use_id}.{name} armed at {at}"
            )));
        }
        if *at > command.now {
            return Err(Refusal::invalid_argument(format!(
                "deadline {use_id}.{name} of {type_name} {key} falls due at {at}, after {}",
                command.now
            )));
        }
        Ok(())
    }

    // settle runs the core until its outcome, answering its reads from the
    // store and granting its locks: the locks the run held come back with
    // the outcome.
    fn settle(
        &self,
        spec: &TypeSpec,
        command: &Command,
    ) -> Result<(Outcome, Vec<LockKey>), Refusal> {
        let mut answers = Answers::new();
        let mut locks = Vec::new();
        for _ in 0..MAX_ROUNDS {
            match self.core.run(spec, command, &answers) {
                Step::Done(outcome) => return Ok((outcome, locks)),
                Step::Read(requests) => {
                    for request in requests {
                        // A kind of read this host does not take is its
                        // own failure, not the caller's or a behavior's.
                        let answer = self.store.answer(&request).ok_or_else(|| {
                            Refusal::internal(format!(
                                "the reference host answers no read of this kind: {}",
                                serde_json::to_string(&request).unwrap_or_default()
                            ))
                        })?;
                        answers.answer(request, answer);
                    }
                }
                Step::Lock(keys) => {
                    // One writer holds in memory: a lock is granted as it
                    // is asked, and the core asks again for the reads made
                    // before it.
                    for key in keys {
                        if !locks.contains(&key) {
                            locks.push(key.clone());
                        }
                        answers.hold(key);
                    }
                }
            }
        }
        Err(Refusal::internal(format!(
            "{} of {}: the run still asked for reads or locks after {MAX_ROUNDS} rounds",
            command.action.name(),
            spec.name
        )))
    }
}
