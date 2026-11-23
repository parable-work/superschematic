//! The development layout over memory: the store's contract written once
//! with no database, which the handler's unit tests run on and every
//! database store answers as. A transaction is a copy of the state that a
//! commit keeps and a rollback drops; a unit applies to a copy of its own,
//! kept whole or not at all.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    Code, Refusal, RowChange, SharedRow, SharedRowChange, SubRow, SubRowChange,
};

use crate::store::{
    Armed, Draft, DueDeadline, HeldSubRow, QueuedChange, QueuedEvent, Store, StoreAnswer,
    StoreRead, StoreWrite, StoredRow, Unit, Version,
};

/// Where a sub-table row sits in its behavior's shared sub-table.
type SubKey = (String, String, String, String, String, String);

/// Where a deadline sits: its row's type and id, its use and its name.
type DeadlineKey = (String, String, String, String);

/// Where a row of a use's shared table sits: its type, its use, its table
/// and its key.
type SharedKey = (String, String, String, String);

#[derive(Clone, Debug, Default, PartialEq)]
struct State {
    versions: BTreeMap<String, Vec<Version>>,
    drafts: BTreeMap<String, Value>,
    rows: BTreeMap<String, BTreeMap<String, StoredRow>>,
    /// By behavior, type, id, use, table and key.
    sub_rows: BTreeMap<SubKey, Map<String, Value>>,
    /// The rows of the uses' shared tables, by type, use, table and key,
    /// each with its version, which no row's delete takes.
    shared: BTreeMap<SharedKey, SharedRow>,
    /// By type, id, use and name: the time it was armed at, when it falls
    /// due, and its payload.
    deadlines: BTreeMap<DeadlineKey, (i64, i64, Value)>,
    events: Vec<QueuedEvent>,
    changes: BTreeMap<i64, (String, String, RowChange)>,
    next_seq: i64,
}

/// The development layout in memory.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct MemoryStore {
    state: State,
    saved: Option<State>,
}

/// Why a unit stopped: a fence that did not hold, or a refusal.
enum Stop {
    Moved,
    Refused(Refusal),
}

impl MemoryStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// The events queued, in the order written.
    pub fn events(&self) -> &[QueuedEvent] {
        &self.state.events
    }

    /// Forgets the events queued, as a worker that handed them on.
    pub fn take_events(&mut self) -> Vec<QueuedEvent> {
        std::mem::take(&mut self.state.events)
    }

    /// A row's sub-table rows of one use's table, in key order.
    pub fn sub_rows(&self, type_name: &str, id: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        self.state
            .sub_rows
            .iter()
            .filter(|((_, t, i, u, tbl, _), _)| {
                t == type_name && i == id && u == use_id && tbl == table
            })
            .map(|((_, _, _, _, _, key), values)| SubRow::new(key.clone(), values.clone()))
            .collect()
    }

    /// The row of a key of a use's shared table, with its version.
    pub fn shared_row(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Option<&SharedRow> {
        self.state.shared.get(&(
            type_name.to_owned(),
            use_id.to_owned(),
            table.to_owned(),
            key.to_owned(),
        ))
    }

    /// A row's armed deadlines: each its use, name, time and payload, by use
    /// and name.
    pub fn armed(&self, type_name: &str, id: &str) -> Vec<(String, String, i64, Value)> {
        self.state
            .deadlines
            .iter()
            .filter(|((t, i, _, _), _)| t == type_name && i == id)
            .map(|((_, _, use_id, name), (at, _, payload))| {
                (use_id.clone(), name.clone(), *at, payload.clone())
            })
            .collect()
    }

    /// A stored row, past any transaction.
    pub fn row(&self, type_name: &str, id: &str) -> Option<&StoredRow> {
        self.state.rows.get(type_name).and_then(|rows| rows.get(id))
    }
}

fn apply_one(state: &mut State, write: &StoreWrite) -> Result<(), Stop> {
    match write {
        StoreWrite::PutDraft {
            type_name,
            document,
        } => {
            state.drafts.insert(type_name.clone(), document.clone());
        }
        StoreWrite::PutVersion {
            type_name,
            version,
            document,
        } => {
            let versions = state.versions.entry(type_name.clone()).or_default();
            let live = versions.last().map_or(0, |known| known.version);
            if live != version - 1 {
                return Err(Stop::Moved);
            }
            versions.push(Version {
                type_name: type_name.clone(),
                version: *version,
                document: document.clone(),
            });
            state.drafts.remove(type_name);
        }
        StoreWrite::Insert {
            type_name,
            id,
            data,
            owned,
        } => {
            let rows = state.rows.entry(type_name.clone()).or_default();
            if rows.contains_key(id) {
                return Err(Stop::Refused(Refusal::new(
                    Code::Conflict,
                    format!("{type_name} {id} already exists"),
                )));
            }
            rows.insert(
                id.clone(),
                StoredRow {
                    id: id.clone(),
                    version: 1,
                    data: data.clone(),
                    owned: owned.clone(),
                },
            );
        }
        StoreWrite::Update {
            type_name,
            id,
            expected,
            data,
            owned,
        } => {
            let row = state
                .rows
                .get_mut(type_name)
                .and_then(|rows| rows.get_mut(id))
                .filter(|row| row.version == *expected)
                .ok_or(Stop::Moved)?;
            row.data.clone_from(data);
            row.owned.clone_from(owned);
            row.version += 1;
        }
        StoreWrite::Rewrite {
            type_name,
            id,
            data,
            owned,
        } => {
            let row = state
                .rows
                .get_mut(type_name)
                .and_then(|rows| rows.get_mut(id))
                .ok_or(Stop::Moved)?;
            row.data.clone_from(data);
            row.owned.clone_from(owned);
        }
        StoreWrite::Delete {
            type_name,
            id,
            expected,
        } => {
            let rows = state.rows.entry(type_name.clone()).or_default();
            let held = rows
                .get(id)
                .is_some_and(|row| expected.is_none_or(|expected| row.version == expected));
            if !held {
                return Err(Stop::Moved);
            }
            rows.remove(id);
            state
                .sub_rows
                .retain(|(_, t, i, _, _, _), _| !(t == type_name && i == id));
            state
                .deadlines
                .retain(|(t, i, _, _), _| !(t == type_name && i == id));
        }
        StoreWrite::Gone { type_name, id } => {
            let there = state
                .rows
                .get(type_name)
                .is_some_and(|rows| rows.contains_key(id));
            if !there {
                state
                    .sub_rows
                    .retain(|(_, t, i, _, _, _), _| !(t == type_name && i == id));
                state
                    .deadlines
                    .retain(|(t, i, _, _), _| !(t == type_name && i == id));
            }
        }
        StoreWrite::SubRow {
            type_name,
            id,
            behavior,
            use_id,
            table,
            key,
            change,
        } => {
            let at = (
                behavior.clone(),
                type_name.clone(),
                id.clone(),
                use_id.clone(),
                table.clone(),
                key.clone(),
            );
            match change {
                SubRowChange::Insert { values } => {
                    if state.sub_rows.contains_key(&at) {
                        return Err(Stop::Moved);
                    }
                    let values = values
                        .iter()
                        .filter(|(_, value)| !value.is_null())
                        .map(|(name, value)| (name.clone(), value.clone()))
                        .collect();
                    state.sub_rows.insert(at, values);
                }
                SubRowChange::Update { values } => {
                    let row = state.sub_rows.get_mut(&at).ok_or(Stop::Moved)?;
                    for (name, value) in values {
                        if value.is_null() {
                            row.shift_remove(name);
                        } else {
                            row.insert(name.clone(), value.clone());
                        }
                    }
                }
                SubRowChange::Delete => {
                    state.sub_rows.remove(&at).ok_or(Stop::Moved)?;
                }
                other => {
                    return Err(Stop::Refused(Refusal::internal(format!(
                        "the development store takes no sub-table change of this kind: {other:?}"
                    ))))
                }
            }
        }
        StoreWrite::SharedRow {
            type_name,
            use_id,
            table,
            key,
            change,
        } => {
            let at = (
                type_name.clone(),
                use_id.clone(),
                table.clone(),
                key.clone(),
            );
            match change {
                SharedRowChange::Insert { values } => {
                    if state.shared.contains_key(&at) {
                        return Err(Stop::Moved);
                    }
                    state
                        .shared
                        .insert(at, SharedRow::new(key.clone(), values.clone(), 1));
                }
                SharedRowChange::Update {
                    expected_version,
                    values,
                } => {
                    let row = state
                        .shared
                        .get_mut(&at)
                        .filter(|row| row.version == *expected_version)
                        .ok_or(Stop::Moved)?;
                    for (name, value) in values {
                        if value.is_null() {
                            row.values.shift_remove(name);
                        } else {
                            row.values.insert(name.clone(), value.clone());
                        }
                    }
                    row.version += 1;
                }
                other => {
                    return Err(Stop::Refused(Refusal::internal(format!(
                        "the development store takes no shared-table change of this kind: {other:?}"
                    ))))
                }
            }
        }
        StoreWrite::DropUse {
            type_name,
            behavior,
            use_id,
        } => {
            state
                .sub_rows
                .retain(|(b, t, _, u, _, _), _| !(b == behavior && t == type_name && u == use_id));
            state
                .shared
                .retain(|(t, u, _, _), _| !(t == type_name && u == use_id));
        }
        StoreWrite::Arm {
            type_name,
            id,
            use_id,
            name,
            at,
            payload,
        } => {
            state.deadlines.insert(
                (type_name.clone(), id.clone(), use_id.clone(), name.clone()),
                (*at, *at, payload.clone()),
            );
        }
        StoreWrite::Clear {
            type_name,
            id,
            use_id,
            name,
        } => {
            state
                .deadlines
                .remove(&(type_name.clone(), id.clone(), use_id.clone(), name.clone()));
        }
        StoreWrite::ClearUse { type_name, use_id } => {
            state
                .deadlines
                .retain(|(t, _, u, _), _| !(t == type_name && u == use_id));
        }
        StoreWrite::Spend {
            type_name,
            id,
            use_id,
            name,
            at,
        } => {
            let key = (type_name.clone(), id.clone(), use_id.clone(), name.clone());
            match state.deadlines.get(&key) {
                Some((armed, _, _)) if armed == at => {
                    state.deadlines.remove(&key);
                }
                _ => return Err(Stop::Moved),
            }
        }
        StoreWrite::Release {
            type_name,
            id,
            use_id,
            name,
            due,
        } => {
            let armed = state
                .deadlines
                .get_mut(&(type_name.clone(), id.clone(), use_id.clone(), name.clone()))
                .ok_or(Stop::Moved)?;
            armed.1 = *due;
        }
        StoreWrite::Event {
            type_name,
            id,
            use_id,
            kind,
            at,
            payload,
        } => {
            state.next_seq += 1;
            state.events.push(QueuedEvent {
                seq: state.next_seq,
                type_name: type_name.clone(),
                id: id.clone(),
                use_id: use_id.clone(),
                kind: kind.clone(),
                at: *at,
                payload: payload.clone(),
            });
        }
        StoreWrite::Change {
            type_name,
            id,
            change,
        } => {
            state.next_seq += 1;
            state.changes.insert(
                state.next_seq,
                (type_name.clone(), id.clone(), change.clone()),
            );
        }
        StoreWrite::Dequeue { seq } => {
            state.changes.remove(seq).ok_or(Stop::Moved)?;
        }
    }
    Ok(())
}

impl Store for MemoryStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        let mut due: Vec<(i64, DueDeadline)> = self
            .state
            .deadlines
            .iter()
            .filter(|(_, (_, due, _))| *due <= now)
            .map(|((type_name, id, use_id, name), (at, due, payload))| {
                (
                    *due,
                    DueDeadline {
                        type_name: type_name.clone(),
                        id: id.clone(),
                        use_id: use_id.clone(),
                        name: name.clone(),
                        at: *at,
                        payload: payload.clone(),
                    },
                )
            })
            .collect();
        due.sort_by_key(|(due, _)| *due);
        Ok(due.into_iter().map(|(_, deadline)| deadline).collect())
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        Ok(self
            .state
            .changes
            .iter()
            .map(|(seq, (type_name, id, change))| QueuedChange {
                seq: *seq,
                type_name: type_name.clone(),
                id: id.clone(),
                change: change.clone(),
            })
            .collect())
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        Ok(self
            .state
            .events
            .iter()
            .filter(|event| event.seq > after)
            .cloned()
            .collect())
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.saved = Some(self.state.clone());
        Ok(())
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.saved = None;
        Ok(())
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if let Some(saved) = self.saved.take() {
            self.state = saved;
        }
        Ok(())
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        let state = &self.state;
        Ok(match read {
            StoreRead::Live => StoreAnswer::Live(
                state
                    .versions
                    .values()
                    .filter_map(|versions| versions.last().cloned())
                    .collect(),
            ),
            StoreRead::Draft { type_name } => {
                StoreAnswer::Draft(state.drafts.get(type_name).map(|document| Draft {
                    type_name: type_name.clone(),
                    document: document.clone(),
                }))
            }
            StoreRead::Row { type_name, id } => StoreAnswer::Row(
                state
                    .rows
                    .get(type_name)
                    .and_then(|rows| rows.get(id))
                    .cloned(),
            ),
            StoreRead::Rows { type_name } => StoreAnswer::Rows(
                state
                    .rows
                    .get(type_name)
                    .map(|rows| rows.values().cloned().collect())
                    .unwrap_or_default(),
            ),
            StoreRead::SubRows {
                type_name,
                id,
                use_id,
                table,
            } => StoreAnswer::SubRows(self.sub_rows(type_name, id, use_id, table)),
            StoreRead::TableSubRows {
                type_name,
                use_id,
                table,
            } => {
                let mut rows: Vec<HeldSubRow> = state
                    .sub_rows
                    .iter()
                    .filter(|((_, t, _, u, tbl, _), _)| {
                        t == type_name && u == use_id && tbl == table
                    })
                    .map(|((_, _, id, _, _, key), values)| HeldSubRow {
                        id: id.clone(),
                        row: SubRow::new(key.clone(), values.clone()),
                    })
                    .collect();
                rows.sort_by(|a, b| (&a.id, &a.row.key).cmp(&(&b.id, &b.row.key)));
                StoreAnswer::TableSubRows(rows)
            }
            StoreRead::SharedRow {
                type_name,
                use_id,
                table,
                key,
            } => StoreAnswer::SharedRow(self.shared_row(type_name, use_id, table, key).cloned()),
            StoreRead::Armed {
                type_name,
                id,
                use_id,
                name,
            } => StoreAnswer::Armed(
                state
                    .deadlines
                    .get(&(type_name.clone(), id.clone(), use_id.clone(), name.clone()))
                    .map(|(at, _, payload)| Armed {
                        at: *at,
                        payload: payload.clone(),
                    }),
            ),
            StoreRead::Page { .. } | StoreRead::Holders { .. } | StoreRead::Reached { .. } => {
                return Err(crate::store::unqueried(read))
            }
        })
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        let mut next = self.state.clone();
        for write in &unit.writes {
            match apply_one(&mut next, write) {
                Ok(()) => {}
                Err(Stop::Moved) => return Ok(false),
                Err(Stop::Refused(refusal)) => return Err(refusal),
            }
        }
        self.state = next;
        Ok(true)
    }
}
