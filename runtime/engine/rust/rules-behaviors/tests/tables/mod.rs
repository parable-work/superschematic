//! The host the tests of Presence and Budget share: a source in memory that
//! keeps rows of several types, their specs, their sub-table rows and the
//! deadlines the core arms, as a database's adapter keeps its tables, so a
//! command that reaches another row (`Cx::call_on`, a linked read, a
//! holding read, another row's sub-table rows) runs as it does on a table.

#![allow(dead_code)]

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    key_of, merge_patch, Action, Answer, Caller, ChangeSet, Command, Core, DeadlineChange,
    KeyedRow, LinkedRows, Outcome, ReadRequest, Refusal, Row, RowWrite, Source, SubRow,
    SubRowChange, TypeSpec,
};

/// A row's place: its type and its key.
pub type At = (String, String);

/// Rows of several types, their specs, their sub-table rows and the
/// deadlines armed on them.
#[derive(Default)]
pub struct Tables {
    pub specs: BTreeMap<String, TypeSpec>,
    pub rows: BTreeMap<At, Row>,
    /// By row, use id, table and key.
    pub sub_rows: BTreeMap<(At, String, String, String), Map<String, Value>>,
    /// By row, use id and name: the time it falls due.
    pub armed: BTreeMap<(At, String, String), i64>,
}

fn at(type_name: &str, key: &str) -> At {
    (type_name.to_owned(), key.to_owned())
}

impl Source for Tables {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        match request {
            ReadRequest::Row { type_name, key } => {
                Ok(Answer::Row(self.rows.get(&at(type_name, key)).cloned()))
            }
            ReadRequest::Spec { type_name } => Ok(Answer::Spec(self.specs.get(type_name).cloned())),
            ReadRequest::HasRows { type_name } => Ok(Answer::HasRows(
                self.rows.keys().any(|(name, _)| name == type_name),
            )),
            ReadRequest::SubRows {
                type_name,
                key,
                use_id,
                table,
            } => Ok(Answer::SubRows(self.table(type_name, key, use_id, table))),
            ReadRequest::Linked {
                type_name,
                field,
                key,
                after,
                limit,
            } => Ok(Answer::Linked(self.page(
                type_name,
                after.as_deref(),
                *limit,
                |row| row.own.get(field).and_then(key_of).as_deref() == Some(key.as_str()),
            ))),
            ReadRequest::Holding {
                type_name,
                use_id,
                field,
                value,
                after,
                limit,
            } => Ok(Answer::Linked(self.page(
                type_name,
                after.as_deref(),
                *limit,
                |row| row.owned.get(use_id).and_then(|fields| fields.get(field)) == Some(value),
            ))),
            other => Err(Refusal::internal(format!(
                "this host answers no read of this kind: {other:?}"
            ))),
        }
    }
}

impl Tables {
    /// A row's sub-table rows, in key order.
    pub fn table(&self, type_name: &str, key: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        let row = at(type_name, key);
        self.sub_rows
            .iter()
            .filter(|((owner, used, named, _), _)| {
                *owner == row && used == use_id && named == table
            })
            .map(|((_, _, _, sub_key), values)| SubRow::new(sub_key.clone(), values.clone()))
            .collect()
    }

    // page is one page of a type's rows that a test holds, in key order.
    fn page(
        &self,
        type_name: &str,
        after: Option<&str>,
        limit: usize,
        holds: impl Fn(&Row) -> bool,
    ) -> LinkedRows {
        let mut rows: Vec<KeyedRow> = self
            .rows
            .iter()
            .filter(|((name, key), row)| {
                name == type_name && after.is_none_or(|after| key.as_str() > after) && holds(row)
            })
            .map(|((_, key), row)| KeyedRow {
                key: key.clone(),
                row: row.clone(),
            })
            .collect();
        let more = rows.len() > limit;
        rows.truncate(limit);
        LinkedRows { rows, more }
    }

    // apply writes a change set as an adapter does: a create at version 1,
    // an update held to its version, a delete that takes the row's
    // sub-table rows and deadlines; a deadline command spends the arming it
    // ran for before the change set's own deadline changes.
    fn apply(&mut self, command: &Command, top: &str, changes: &ChangeSet) {
        let mut deleted = Vec::new();
        for write in &changes.rows {
            let row = at(write.type_name(), write.key().expect("a keyed row"));
            match write {
                RowWrite::Create { own, owned, .. } => {
                    self.rows.insert(
                        row,
                        Row {
                            own: own.clone(),
                            owned: owned.clone(),
                            version: 1,
                        },
                    );
                }
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let held = self.rows.get_mut(&row).expect("the row");
                    assert_eq!(held.version, *expected_version, "{row:?}");
                    held.own = merge_patch(&held.own, own);
                    for (use_id, fields) in owned {
                        let kept = held.owned.entry(use_id.clone()).or_default();
                        for (field, value) in fields {
                            kept.insert(field.clone(), value.clone());
                        }
                    }
                    held.version += 1;
                }
                RowWrite::Delete { .. } => {
                    self.rows.remove(&row);
                    self.sub_rows.retain(|(owner, ..), _| *owner != row);
                    self.armed.retain(|(owner, ..), _| *owner != row);
                    deleted.push(row);
                }
                other => panic!("a write this host does not take: {other:?}"),
            }
        }
        if let Action::Deadline {
            key, use_id, name, ..
        } = &command.action
        {
            self.armed
                .remove(&(at(top, key), use_id.clone(), name.clone()));
        }
        for write in &changes.sub_rows {
            let row = at(
                &write.row.type_name,
                write.row.key.as_deref().expect("a key"),
            );
            if deleted.contains(&row) {
                continue;
            }
            let place = (
                row,
                write.use_id.clone(),
                write.table.clone(),
                write.key.clone(),
            );
            match &write.change {
                SubRowChange::Insert { values } => {
                    assert!(!self.sub_rows.contains_key(&place), "a new row: {place:?}");
                    self.sub_rows.insert(place, present(values));
                }
                SubRowChange::Update { values } => {
                    let kept = self.sub_rows.get_mut(&place).expect("the row it updates");
                    for (name, value) in values {
                        if value.is_null() {
                            kept.remove(name);
                        } else {
                            kept.insert(name.clone(), value.clone());
                        }
                    }
                }
                SubRowChange::Delete => {
                    assert!(self.sub_rows.remove(&place).is_some(), "the row it deletes");
                }
                other => panic!("a sub-table change this host does not take: {other:?}"),
            }
        }
        for change in &changes.deadlines {
            let row = at(
                &change.row().type_name,
                change.row().key.as_deref().expect("a key"),
            );
            if deleted.contains(&row) {
                continue;
            }
            let arming = (row, change.use_id().to_owned(), change.name().to_owned());
            match change {
                DeadlineChange::Arm { at, .. } => {
                    self.armed.insert(arming, *at);
                }
                _ => {
                    self.armed.remove(&arming);
                }
            }
        }
    }
}

fn present(values: &Map<String, Value>) -> Map<String, Value> {
    values
        .iter()
        .filter(|(_, value)| !value.is_null())
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect()
}

/// Rows of several types on a core.
pub struct Host {
    pub core: Core,
    pub tables: Tables,
    requests: usize,
}

impl Host {
    /// A host of the types given, each held to the core's define check.
    pub fn new(core: Core, specs: impl IntoIterator<Item = TypeSpec>) -> Self {
        let mut tables = Tables::default();
        for spec in specs {
            tables.specs.insert(spec.name.clone(), spec);
        }
        let host = Host {
            core,
            tables,
            requests: 0,
        };
        for spec in host.tables.specs.values() {
            let command = Command::new(
                Caller::new("alice", Vec::<String>::new()),
                0,
                Action::Define,
            );
            if let Err(refusal) = host
                .core
                .drive(spec, &command, &mut Tables::default())
                .into_result()
            {
                panic!("{} does not define: {}", spec.name, refusal.error_json());
            }
        }
        host
    }

    pub fn spec(&self, type_name: &str) -> TypeSpec {
        self.tables.specs.get(type_name).cloned().expect("a type")
    }

    /// Drives a command on a type to its outcome and applies its change
    /// set.
    pub fn run(&mut self, type_name: &str, caller: &Caller, now: i64, action: Action) -> Outcome {
        self.run_with(type_name, caller, now, action, Map::new())
    }

    /// [`Host::run`] with the caller's preconditions, by use id.
    pub fn run_with(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
        action: Action,
        preconditions: Map<String, Value>,
    ) -> Outcome {
        self.requests += 1;
        let command = Command::new(caller.clone(), now, action)
            .with_request(format!("r{}", self.requests))
            .with_preconditions(preconditions);
        let spec = self.spec(type_name);
        let outcome = self.core.drive(&spec, &command, &mut self.tables);
        if let Outcome::Changed(changes) = &outcome {
            self.tables.apply(&command, type_name, changes);
        }
        outcome
    }

    pub fn create(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
        key: &str,
        own: Value,
    ) -> Outcome {
        self.run(
            type_name,
            caller,
            now,
            Action::Create {
                id: Some(key.to_owned()),
                own: own.as_object().cloned().expect("an object"),
                params: Map::new(),
            },
        )
    }

    pub fn update(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
        key: &str,
        patch: Value,
    ) -> Outcome {
        self.run(
            type_name,
            caller,
            now,
            Action::Update {
                key: key.to_owned(),
                patch: patch.as_object().cloned().expect("an object"),
            },
        )
    }

    pub fn delete(&mut self, type_name: &str, caller: &Caller, now: i64, key: &str) -> Outcome {
        self.run(
            type_name,
            caller,
            now,
            Action::Delete {
                key: key.to_owned(),
            },
        )
    }

    #[allow(clippy::too_many_arguments)]
    pub fn invoke(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
        key: &str,
        use_id: &str,
        operation: &str,
        params: Value,
    ) -> Outcome {
        self.run(
            type_name,
            caller,
            now,
            Action::Operation {
                key: key.to_owned(),
                use_id: use_id.to_owned(),
                operation: operation.to_owned(),
                params,
            },
        )
    }

    /// An operation presenting a lease's token as Lease's precondition.
    #[allow(clippy::too_many_arguments)]
    pub fn invoke_fenced(
        &mut self,
        type_name: &str,
        caller: &Caller,
        now: i64,
        key: &str,
        use_id: &str,
        operation: &str,
        params: Value,
        token: i64,
    ) -> Outcome {
        let mut preconditions = Map::new();
        preconditions.insert("Lease".into(), serde_json::json!({ "token": token }));
        self.run_with(
            type_name,
            caller,
            now,
            Action::Operation {
                key: key.to_owned(),
                use_id: use_id.to_owned(),
                operation: operation.to_owned(),
                params,
            },
            preconditions,
        )
    }

    /// Runs every deadline armed and due at a time, earliest first, as the
    /// API's worker does, as the caller given: what each came to.
    pub fn fall_due(&mut self, now: i64, runner: &Caller) -> Vec<(At, String, Outcome)> {
        let mut due: Vec<((At, String, String), i64)> = self
            .tables
            .armed
            .iter()
            .filter(|(_, at)| **at <= now)
            .map(|(arming, at)| (arming.clone(), *at))
            .collect();
        due.sort_by_key(|(_, at)| *at);
        due.into_iter()
            .map(|(((type_name, key), use_id, name), at)| {
                let outcome = self.run(
                    &type_name,
                    runner,
                    now.max(at),
                    Action::Deadline {
                        key: key.clone(),
                        use_id,
                        name: name.clone(),
                        at,
                        payload: Value::Null,
                    },
                );
                ((type_name, key), name, outcome)
            })
            .collect()
    }

    pub fn row(&self, type_name: &str, key: &str) -> &Row {
        self.tables.rows.get(&at(type_name, key)).expect("the row")
    }

    /// One of a use's owned fields on a row; null for none.
    pub fn owned(&self, type_name: &str, key: &str, use_id: &str, field: &str) -> Value {
        self.row(type_name, key)
            .owned
            .get(use_id)
            .and_then(|fields| fields.get(field))
            .cloned()
            .unwrap_or(Value::Null)
    }

    /// A row's sub-table rows, in key order, as their keys and values.
    pub fn sub_rows(&self, type_name: &str, key: &str, use_id: &str, table: &str) -> Value {
        serde_json::to_value(self.tables.table(type_name, key, use_id, table)).expect("rows")
    }

    /// When a use's deadline is armed on a row; None when it is not.
    pub fn armed(&self, type_name: &str, key: &str, use_id: &str, name: &str) -> Option<i64> {
        self.tables
            .armed
            .get(&(at(type_name, key), use_id.to_owned(), name.to_owned()))
            .copied()
    }

    /// The fields a read of a row shows for a use at a time, beyond its
    /// owned columns, from every sub-table its behavior declares.
    pub fn read_fields_at(
        &self,
        type_name: &str,
        key: &str,
        use_id: &str,
        now: i64,
    ) -> Map<String, Value> {
        let spec = self.spec(type_name);
        let behavior = &spec
            .uses
            .iter()
            .find(|entry| entry.id() == use_id)
            .expect("the use")
            .behavior;
        let declaration = self.core.declaration(behavior).expect("a behavior");
        let tables: BTreeMap<String, Vec<SubRow>> = declaration
            .tables
            .iter()
            .map(|table| {
                (
                    table.name.clone(),
                    self.tables.table(type_name, key, use_id, &table.name),
                )
            })
            .collect();
        self.core
            .read_fields_at(&spec, use_id, self.row(type_name, key), &tables, now)
            .expect("its fields")
    }
}
