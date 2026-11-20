//! The development layout as a host of the core reads it (D117): one
//! command of the core run over the store's rows, its reads answered from
//! the reads the bridge asks of the store, its locks held, and its change
//! set written as the store's writes. It is the reference host's mapping
//! (`superschematic-rules-testkit`'s `Store`), pure: a row is its id, its
//! version, its own fields as one JSON object and its owned fields as
//! another; a sub-table row sits in its behavior's shared sub-table, keyed
//! by type, id, use, table and key; a deadline, an event and a heard write
//! are rows of the store's deadlines, event queue and change queue.
//!
//! The bridge asks a store for a row by its id, every row of a type, a
//! row's rows of a sub-table, every row's rows of a sub-table and a
//! deadline's arming; it answers the core's linked, holding and table reads
//! from every row of the type, in id order by code point, so a store learns
//! no rule. A host runs each command in one transaction that holds the
//! store's one writer from its start, so every lock the core asks for is
//! held as it asks, and the core's answers are its reads.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};
use superschematic_rules::{
    key_of, merge_patch, Action, Answer, Answers, ChangeSet, Command, Core, DeadlineChange,
    KeyedRow, LinkedRows, Outcome, ReadRequest, Refusal, Row, RowChange, RowImage, RowWrite, Step,
    SubRowChange, TableRef, TypeSpec, WriteKind, MAX_ROUNDS,
};
use superschematic_rules_behaviors::subjects::canonical_subject;

use crate::store::{StoreAnswer, StoreAnswers, StoreRead, StoreWrite, StoredRow};
use crate::values::same;

/// What one run of a command over the store came to.
#[derive(Clone, Debug, PartialEq)]
pub enum Ran {
    /// The store reads it needs before it goes on.
    Read(Vec<StoreRead>),
    /// The core's refusal, or the host's own (a deadline no longer armed).
    Refused(Refusal),
    /// The change set and the writes that apply it.
    Changed(Box<Changed>),
}

/// A change set as the store applies it.
#[derive(Clone, Debug, PartialEq)]
pub struct Changed {
    /// The core's change set, each create's key filled in.
    pub changes: ChangeSet,
    /// The writes that apply it, in the order the store applies them.
    pub writes: Vec<StoreWrite>,
    /// Each row it writes as the writes leave it, by type and id, in the
    /// change set's order: `None` for a row it deletes or finds gone.
    pub rows: Vec<(String, String, Option<StoredRow>)>,
}

/// A command's run over the store: the core, every type's live version by
/// name (what a call on another type's row binds), and the types whose
/// writes a behavior hears, each of whose writes queues a change.
pub struct Bridge<'a> {
    pub core: &'a Core,
    pub specs: &'a BTreeMap<String, TypeSpec>,
    pub hearing: &'a BTreeSet<String>,
}

/// A stored row as the core reads it.
pub fn core_row(row: &StoredRow) -> Row {
    Row {
        own: row.data.clone(),
        owned: row.owned.clone(),
        version: row.version,
    }
}

/// A stored row's image as a change trigger writes it: its own and owned
/// fields, a field with no value absent.
pub fn image(row: &StoredRow) -> RowImage {
    let present = |fields: &Map<String, Value>| -> Map<String, Value> {
        fields
            .iter()
            .filter(|(_, value)| !value.is_null())
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect()
    };
    RowImage {
        own: present(&row.data),
        owned: row
            .owned
            .iter()
            .map(|(use_id, fields)| (use_id.clone(), present(fields)))
            .filter(|(_, fields)| !fields.is_empty())
            .collect(),
    }
}

/// Owned fields with others set on them, a null clearing one, and a use
/// left with none dropped.
pub fn set_owned(
    held: &BTreeMap<String, Map<String, Value>>,
    owned: &BTreeMap<String, Map<String, Value>>,
) -> BTreeMap<String, Map<String, Value>> {
    let mut out = held.clone();
    for (use_id, fields) in owned {
        let slot = out.entry(use_id.clone()).or_default();
        for (field, value) in fields {
            if value.is_null() {
                slot.shift_remove(field);
            } else {
                slot.insert(field.clone(), value.clone());
            }
        }
    }
    out.retain(|_, fields| !fields.is_empty());
    out
}

/// The stored row of a type by its id, from a read of it or of every row
/// of the type: `Some(None)` when the store has none of the id, `None` when
/// the bridge has asked neither.
pub fn row_of<'b>(
    answers: &'b StoreAnswers,
    type_name: &str,
    id: &str,
) -> Option<Option<&'b StoredRow>> {
    let read = StoreRead::Row {
        type_name: type_name.to_owned(),
        id: id.to_owned(),
    };
    if let Some(StoreAnswer::Row(row)) = answers.get(&read) {
        return Some(row.as_ref());
    }
    rows_of(answers, type_name).map(|rows| rows.iter().find(|row| row.id == id))
}

/// Every stored row of a type, when the bridge has asked for them.
pub fn rows_of<'b>(answers: &'b StoreAnswers, type_name: &str) -> Option<&'b [StoredRow]> {
    match answers.get(&StoreRead::Rows {
        type_name: type_name.to_owned(),
    }) {
        Some(StoreAnswer::Rows(rows)) => Some(rows),
        _ => None,
    }
}

// UUID_TYPES are the IR types of an own field whose values compare in
// either written form, as an adapter binds both against a uuid column.
const UUID_TYPES: [&str; 2] = ["Identity.UUID", "Identity.UserID"];

// held_equal is whether an own field holds a value as a read across a
// table compares it: an absent field and a null are one value, two numbers
// equal by value, a UUID field's strings in either form, anything else by
// its JSON.
fn held_equal(held: Option<&Value>, value: &Value, uuid: bool) -> bool {
    let held = held.filter(|held| !held.is_null());
    match (held, value) {
        (None, Value::Null) => true,
        (None, _) | (Some(_), Value::Null) => false,
        (Some(Value::String(held)), Value::String(value)) if uuid => {
            canonical_subject(held) == canonical_subject(value)
        }
        (Some(held), value) => same(held, value),
    }
}

// page is one page of keyed rows past `after`, at most `limit`, and whether
// more follow.
fn page(rows: Vec<KeyedRow>, after: Option<&str>, limit: usize) -> LinkedRows {
    let mut rows: Vec<KeyedRow> = rows
        .into_iter()
        .filter(|row| after.is_none_or(|after| row.key.as_str() > after))
        .take(limit.saturating_add(1))
        .collect();
    let more = rows.len() > limit;
    rows.truncate(limit);
    LinkedRows { rows, more }
}

impl Bridge<'_> {
    fn spec_of<'s>(&'s self, own: &'s TypeSpec, type_name: &str) -> Option<&'s TypeSpec> {
        if own.name == type_name {
            return Some(own);
        }
        self.specs.get(type_name)
    }

    /// The core's answer to one of its reads, from the store's answers;
    /// `Ok(None)` with the store reads it needs pushed onto `need`.
    fn answer(
        &self,
        own: &TypeSpec,
        request: &ReadRequest,
        answers: &StoreAnswers,
        need: &mut Vec<StoreRead>,
    ) -> Result<Option<Answer>, Refusal> {
        let mut ask = |read: StoreRead| {
            if !need.contains(&read) {
                need.push(read);
            }
        };
        Ok(match request {
            ReadRequest::Row { type_name, key } => match row_of(answers, type_name, key) {
                Some(row) => Some(Answer::Row(row.map(core_row))),
                None => {
                    ask(StoreRead::Row {
                        type_name: type_name.clone(),
                        id: key.clone(),
                    });
                    None
                }
            },
            ReadRequest::HasRows { type_name } => match rows_of(answers, type_name) {
                Some(rows) => Some(Answer::HasRows(!rows.is_empty())),
                None => {
                    ask(StoreRead::Rows {
                        type_name: type_name.clone(),
                    });
                    None
                }
            },
            // Another type's live version; the core never asks the command's
            // own type's.
            ReadRequest::Spec { type_name } => {
                Some(Answer::Spec(self.spec_of(own, type_name).cloned()))
            }
            ReadRequest::SubRows {
                type_name,
                key,
                use_id,
                table,
            } => {
                let read = StoreRead::SubRows {
                    type_name: type_name.clone(),
                    id: key.clone(),
                    use_id: use_id.clone(),
                    table: table.clone(),
                };
                match answers.get(&read) {
                    Some(StoreAnswer::SubRows(rows)) => Some(Answer::SubRows(rows.clone())),
                    _ => {
                        ask(read);
                        None
                    }
                }
            }
            ReadRequest::Linked {
                type_name,
                field,
                key,
                after,
                limit,
            } => {
                if self.spec_of(own, type_name).is_none() {
                    return Ok(Some(Answer::Linked(LinkedRows::default())));
                }
                let Some(rows) = rows_of(answers, type_name) else {
                    ask(StoreRead::Rows {
                        type_name: type_name.clone(),
                    });
                    return Ok(None);
                };
                let keyed = rows
                    .iter()
                    .filter(|row| row.data.get(field).and_then(key_of).as_deref() == Some(key))
                    .map(|row| KeyedRow::new(row.id.clone(), core_row(row)))
                    .collect();
                Some(Answer::Linked(page(keyed, after.as_deref(), *limit)))
            }
            ReadRequest::Holding {
                type_name,
                use_id,
                field,
                value,
                after,
                limit,
            } => {
                if self.spec_of(own, type_name).is_none() {
                    return Ok(Some(Answer::Linked(LinkedRows::default())));
                }
                let Some(rows) = rows_of(answers, type_name) else {
                    ask(StoreRead::Rows {
                        type_name: type_name.clone(),
                    });
                    return Ok(None);
                };
                let keyed = rows
                    .iter()
                    .filter(|row| {
                        row.owned
                            .get(use_id)
                            .and_then(|fields| fields.get(field))
                            .is_some_and(|held| !held.is_null() && same(held, value))
                    })
                    .map(|row| KeyedRow::new(row.id.clone(), core_row(row)))
                    .collect();
                Some(Answer::Linked(page(keyed, after.as_deref(), *limit)))
            }
            ReadRequest::Rows {
                type_name,
                equal,
                tables,
                after,
                limit,
            } => {
                let Some(spec) = self.spec_of(own, type_name) else {
                    return Ok(Some(Answer::Linked(LinkedRows::default())));
                };
                let uuid_fields: BTreeSet<&str> = spec
                    .fields
                    .iter()
                    .filter(|field| UUID_TYPES.contains(&field.type_name.as_str()))
                    .map(|field| field.name.as_str())
                    .collect();
                let mut ready = true;
                let rows = rows_of(answers, type_name);
                if rows.is_none() {
                    ask(StoreRead::Rows {
                        type_name: type_name.clone(),
                    });
                    ready = false;
                }
                let mut held: BTreeMap<&TableRef, &[crate::store::HeldSubRow]> = BTreeMap::new();
                for table in tables {
                    let read = StoreRead::TableSubRows {
                        type_name: type_name.clone(),
                        use_id: table.use_id.clone(),
                        table: table.table.clone(),
                    };
                    match answers.get(&read) {
                        Some(StoreAnswer::TableSubRows(rows)) => {
                            held.insert(table, rows);
                        }
                        _ => {
                            ask(read);
                            ready = false;
                        }
                    }
                }
                let (Some(rows), true) = (rows, ready) else {
                    return Ok(None);
                };
                let keyed = rows
                    .iter()
                    .filter(|row| {
                        equal.iter().all(|(field, value)| {
                            held_equal(
                                row.data.get(field),
                                value,
                                uuid_fields.contains(field.as_str()),
                            )
                        })
                    })
                    .map(|row| {
                        let mut keyed = KeyedRow::new(row.id.clone(), core_row(row));
                        for table in tables {
                            let rows = held
                                .get(table)
                                .map(|rows| {
                                    rows.iter()
                                        .filter(|sub| sub.id == row.id)
                                        .map(|sub| sub.row.clone())
                                        .collect()
                                })
                                .unwrap_or_default();
                            keyed
                                .tables
                                .entry(table.use_id.clone())
                                .or_default()
                                .insert(table.table.clone(), rows);
                        }
                        keyed
                    })
                    .collect();
                Some(Answer::Linked(page(keyed, after.as_deref(), *limit)))
            }
            other => {
                return Err(Refusal::internal(format!(
                    "the development store answers no read of this kind: {}",
                    serde_json::to_string(other).unwrap_or_default()
                )))
            }
        })
    }

    /// Runs a command on a type, at the version `spec` is, over the store's
    /// answers: the reads it needs, its refusal, or its change set and the
    /// writes that apply it. A deadline command runs only while its
    /// deadline is armed at the time it names and not after the command's
    /// time, which it reads first, and its writes spend that arming right
    /// after the row writes (D103).
    pub fn run(&self, spec: &TypeSpec, command: &Command, answers: &StoreAnswers) -> Ran {
        let type_name = spec.name.as_str();
        if let Action::Deadline {
            key,
            use_id,
            name,
            at,
            ..
        } = &command.action
        {
            let read = StoreRead::Armed {
                type_name: type_name.to_owned(),
                id: key.clone(),
                use_id: use_id.clone(),
                name: name.clone(),
            };
            let armed = match answers.get(&read) {
                Some(StoreAnswer::Armed(armed)) => armed,
                _ => return Ran::Read(vec![read]),
            };
            if armed.as_ref().map(|armed| armed.at) != Some(*at) {
                return Ran::Refused(Refusal::not_found(format!(
                    "{type_name} {key} has no deadline {use_id}.{name} armed at {at}"
                )));
            }
            if *at > command.now {
                return Ran::Refused(Refusal::invalid_argument(format!(
                    "deadline {use_id}.{name} of {type_name} {key} falls due at {at}, after {}",
                    command.now
                )));
            }
        }
        let mut core_answers = Answers::new();
        for _ in 0..MAX_ROUNDS {
            match self.core.run(spec, command, &core_answers) {
                Step::Lock(keys) => {
                    // The request's transaction holds the store's one
                    // writer from its start: every lock is held as asked,
                    // and the reads the core asks again are answered again
                    // from the same state.
                    for key in keys {
                        core_answers.hold(key);
                    }
                }
                Step::Read(requests) => {
                    let mut need = Vec::new();
                    for request in requests {
                        match self.answer(spec, &request, answers, &mut need) {
                            Ok(Some(answer)) => core_answers.answer(request, answer),
                            Ok(None) => {}
                            Err(refusal) => return Ran::Refused(refusal),
                        }
                    }
                    if !need.is_empty() {
                        return Ran::Read(need);
                    }
                }
                Step::Done(Outcome::Refused(refusal)) => return Ran::Refused(refusal),
                Step::Done(Outcome::Changed(changes)) => {
                    return self.written(spec, command, *changes, answers)
                }
            }
        }
        Ran::Refused(Refusal::internal(format!(
            "{} of {type_name}: the run still asked for reads or locks after {MAX_ROUNDS} rounds",
            command.action.name()
        )))
    }

    fn behavior_of(&self, own: &TypeSpec, type_name: &str, use_id: &str) -> Option<String> {
        self.spec_of(own, type_name)?
            .use_of(use_id)
            .map(|(_, used)| used.behavior.clone())
    }

    // written maps a change set onto the store's writes: each row write,
    // each a queued change when its type's writes are heard; a deadline
    // command's spend of its arming; the sub-table rows, the events and the
    // deadline changes, a deleted row's sub-table rows and deadlines left
    // out, since they go with it.
    fn written(
        &self,
        spec: &TypeSpec,
        command: &Command,
        mut changes: ChangeSet,
        answers: &StoreAnswers,
    ) -> Ran {
        let create_key = match &command.action {
            Action::Create { id, .. } => id.clone(),
            _ => None,
        };
        let fill = |row: &mut superschematic_rules::RowRef| {
            if row.key.is_none() && row.type_name == spec.name {
                row.key.clone_from(&create_key);
            }
        };
        for write in &mut changes.rows {
            if let RowWrite::Create { type_name, key, .. } = write {
                if key.is_none() && *type_name == spec.name {
                    key.clone_from(&create_key);
                }
            }
        }
        for sub in &mut changes.sub_rows {
            fill(&mut sub.row);
        }
        for event in &mut changes.events {
            fill(&mut event.row);
        }
        for change in &mut changes.deadlines {
            match change {
                DeadlineChange::Arm { row, .. } | DeadlineChange::Clear { row, .. } => fill(row),
                _ => {}
            }
        }

        let mut writes = Vec::new();
        let mut queued = Vec::new();
        let mut need = Vec::new();
        let mut now_rows: Vec<(String, String, Option<StoredRow>)> = Vec::new();
        let mut deleted: BTreeSet<(String, String)> = BTreeSet::new();
        let at = command.now;
        for write in &changes.rows {
            let type_name = write.type_name().to_owned();
            let Some(id) = write.key().map(str::to_owned) else {
                return Ran::Refused(Refusal::invalid_argument(format!(
                    "a create of {type_name} reached the store without a key"
                )));
            };
            let held: Option<StoredRow> = now_rows
                .iter()
                .rev()
                .find(|(t, k, _)| *t == type_name && *k == id)
                .map(|(_, _, row)| row.clone())
                .unwrap_or_else(|| row_of(answers, &type_name, &id).flatten().cloned());
            let hears = self.hearing.contains(&type_name);
            match write {
                RowWrite::Create { own, owned, .. } => {
                    let data: Map<String, Value> = own
                        .iter()
                        .filter(|(_, value)| !value.is_null())
                        .map(|(name, value)| (name.clone(), value.clone()))
                        .collect();
                    let owned = set_owned(&BTreeMap::new(), owned);
                    let after = StoredRow {
                        id: id.clone(),
                        version: 1,
                        data: data.clone(),
                        owned: owned.clone(),
                    };
                    writes.push(StoreWrite::Insert {
                        type_name: type_name.clone(),
                        id: id.clone(),
                        data,
                        owned,
                    });
                    if hears {
                        queued.push(change_of(&type_name, &id, at, None, Some(&after)));
                    }
                    now_rows.push((type_name, id, Some(after)));
                }
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let Some(before) = held else {
                        let read = StoreRead::Row {
                            type_name: type_name.clone(),
                            id: id.clone(),
                        };
                        if answers.get(&read).is_none() {
                            need.push(read);
                            continue;
                        }
                        // The row is gone since the core read it: the
                        // fence moves the command.
                        writes.push(StoreWrite::Update {
                            type_name: type_name.clone(),
                            id: id.clone(),
                            expected: *expected_version,
                            data: own.clone(),
                            owned: owned.clone(),
                        });
                        continue;
                    };
                    let data = merge_patch(&before.data, own);
                    let owned = set_owned(&before.owned, owned);
                    let after = StoredRow {
                        id: id.clone(),
                        version: expected_version + 1,
                        data: data.clone(),
                        owned: owned.clone(),
                    };
                    writes.push(StoreWrite::Update {
                        type_name: type_name.clone(),
                        id: id.clone(),
                        expected: *expected_version,
                        data,
                        owned,
                    });
                    if hears && image(&before) != image(&after) {
                        queued.push(change_of(&type_name, &id, at, Some(&before), Some(&after)));
                    }
                    now_rows.push((type_name, id, Some(after)));
                }
                RowWrite::Delete {
                    expected_version, ..
                } => {
                    writes.push(StoreWrite::Delete {
                        type_name: type_name.clone(),
                        id: id.clone(),
                        expected: Some(*expected_version),
                    });
                    if hears {
                        queued.push(change_of(&type_name, &id, at, held.as_ref(), None));
                    }
                    deleted.insert((type_name.clone(), id.clone()));
                    now_rows.push((type_name, id, None));
                }
                RowWrite::Gone { .. } => {
                    writes.push(StoreWrite::Gone {
                        type_name: type_name.clone(),
                        id: id.clone(),
                    });
                    now_rows.push((type_name, id, None));
                }
                other => {
                    return Ran::Refused(Refusal::internal(format!(
                        "the development store takes no row write of this kind: {other:?}"
                    )))
                }
            }
        }
        if !need.is_empty() {
            return Ran::Read(need);
        }
        if let Action::Deadline {
            key,
            use_id,
            name,
            at: armed_at,
            ..
        } = &command.action
        {
            writes.push(StoreWrite::Spend {
                type_name: spec.name.clone(),
                id: key.clone(),
                use_id: use_id.clone(),
                name: name.clone(),
                at: *armed_at,
            });
        }
        let gone =
            |type_name: &str, id: &str| deleted.contains(&(type_name.to_owned(), id.to_owned()));
        for sub in &changes.sub_rows {
            let Some(id) = sub.row.key.clone() else {
                return Ran::Refused(Refusal::invalid_argument(format!(
                    "a create of {} reached the store without a key",
                    sub.row.type_name
                )));
            };
            if gone(&sub.row.type_name, &id) {
                continue;
            }
            let Some(behavior) = self.behavior_of(spec, &sub.row.type_name, &sub.use_id) else {
                return Ran::Refused(Refusal::internal(format!(
                    "{} has no use {} whose sub-table {} the change set writes",
                    sub.row.type_name, sub.use_id, sub.table
                )));
            };
            let change = match &sub.change {
                SubRowChange::Insert { values } => SubRowChange::Insert {
                    values: values
                        .iter()
                        .filter(|(_, value)| !value.is_null())
                        .map(|(name, value)| (name.clone(), value.clone()))
                        .collect(),
                },
                other => other.clone(),
            };
            writes.push(StoreWrite::SubRow {
                type_name: sub.row.type_name.clone(),
                id,
                behavior,
                use_id: sub.use_id.clone(),
                table: sub.table.clone(),
                key: sub.key.clone(),
                change,
            });
        }
        for event in &changes.events {
            let Some(id) = event.row.key.clone() else {
                return Ran::Refused(Refusal::invalid_argument(format!(
                    "a create of {} reached the store without a key",
                    event.row.type_name
                )));
            };
            writes.push(StoreWrite::Event {
                type_name: event.row.type_name.clone(),
                id,
                use_id: event.use_id.clone(),
                kind: event.kind.clone(),
                at,
                payload: event.payload.clone(),
            });
        }
        for change in &changes.deadlines {
            let row = change.row();
            let Some(id) = row.key.clone() else {
                return Ran::Refused(Refusal::invalid_argument(format!(
                    "a create of {} reached the store without a key",
                    row.type_name
                )));
            };
            if gone(&row.type_name, &id) {
                continue;
            }
            writes.push(match change {
                DeadlineChange::Arm {
                    use_id,
                    name,
                    at,
                    payload,
                    ..
                } => StoreWrite::Arm {
                    type_name: row.type_name.clone(),
                    id,
                    use_id: use_id.clone(),
                    name: name.clone(),
                    at: *at,
                    payload: payload.clone(),
                },
                DeadlineChange::Clear { use_id, name, .. } => StoreWrite::Clear {
                    type_name: row.type_name.clone(),
                    id,
                    use_id: use_id.clone(),
                    name: name.clone(),
                },
                other => {
                    return Ran::Refused(Refusal::internal(format!(
                        "the development store takes no deadline change of this kind: {other:?}"
                    )))
                }
            });
        }
        writes.extend(queued);
        Ran::Changed(Box::new(Changed {
            changes,
            writes,
            rows: now_rows,
        }))
    }
}

/// A write of a row as its change queue holds it (D103): an insert with no
/// row before, a delete with none after, an update otherwise.
pub fn change_of(
    type_name: &str,
    id: &str,
    at: i64,
    before: Option<&StoredRow>,
    after: Option<&StoredRow>,
) -> StoreWrite {
    let write = match (before, after) {
        (None, _) => WriteKind::Insert,
        (Some(_), None) => WriteKind::Delete,
        (Some(_), Some(_)) => WriteKind::Update,
    };
    StoreWrite::Change {
        type_name: type_name.to_owned(),
        id: id.to_owned(),
        change: RowChange {
            write,
            at,
            before: before.map(image),
            after: after.map(image),
        },
    }
}

/// The types whose writes are heard, as the core's behaviors say from the
/// types' live versions: a type one of whose uses' behaviors hears its
/// row's writes, and a type a listener of another type names
/// ([`Core::listeners`]) through a relation of it to that type, as the
/// reference host computes them.
pub fn hearing(core: &Core, specs: &BTreeMap<String, TypeSpec>) -> BTreeSet<String> {
    let mut hearing = BTreeSet::new();
    for (name, spec) in specs {
        let hears = spec.uses.iter().any(|entry| {
            core.declaration(&entry.behavior)
                .is_some_and(|declaration| declaration.hears_changes)
        });
        if hears {
            hearing.insert(name.clone());
        }
        for listen in core.listeners(spec).unwrap_or_default() {
            let points_here = specs
                .get(&listen.type_name)
                .and_then(|heard| heard.relation(&listen.link))
                .and_then(|field| field.relation.as_ref())
                .is_some_and(|relation| relation.type_name == *name);
            if points_here {
                hearing.insert(listen.type_name);
            }
        }
    }
    hearing
}
