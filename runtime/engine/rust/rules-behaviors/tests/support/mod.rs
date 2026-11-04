//! The host the tests of the ported behaviors share: a source
//! in memory that keeps rows, their sub-table rows and the deadlines the
//! core arms, as a table's adapter keeps them, and the define, command and
//! read helpers each file's cases are written with.

#![allow(dead_code)]

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    merge_patch, Action, Answer, Caller, ChangeSet, Command, Core, DeadlineChange, Outcome,
    ReadRequest, Refusal, Row, RowWrite, Source, SubRow, SubRowChange, TypeSpec,
};

pub const START: i64 = 1_000_000;

// ---- the source ----

/// Rows, their sub-table rows and the deadlines armed on them, by row key;
/// one type's.
#[derive(Default)]
pub struct Memory {
    pub rows: BTreeMap<String, Row>,
    /// By row key, use id, table and key.
    pub sub_rows: BTreeMap<(String, String, String, String), Map<String, Value>>,
    /// By row key, use id and name: the time it falls due.
    pub armed: BTreeMap<(String, String, String), i64>,
}

impl Source for Memory {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        match request {
            ReadRequest::Row { key, .. } => Ok(Answer::Row(self.rows.get(key).cloned())),
            ReadRequest::HasRows { .. } => Ok(Answer::HasRows(!self.rows.is_empty())),
            ReadRequest::SubRows {
                key, use_id, table, ..
            } => Ok(Answer::SubRows(self.table(key, use_id, table))),
            other => Err(Refusal::internal(format!(
                "this host answers no read of this kind: {other:?}"
            ))),
        }
    }
}

impl Memory {
    /// A row's sub-table rows, in key order.
    pub fn table(&self, key: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        self.sub_rows
            .iter()
            .filter(|((row, used, named, _), _)| row == key && used == use_id && named == table)
            .map(|((_, _, _, sub_key), values)| SubRow::new(sub_key.clone(), values.clone()))
            .collect()
    }

    // apply writes a change set as an adapter does: a create at version 1,
    // an update held to its version, a delete that takes the row's
    // sub-table rows and deadlines; a deadline command spends the arming it
    // ran for before the change set's own deadline changes.
    fn apply(&mut self, command: &Command, changes: &ChangeSet) {
        let mut deleted = Vec::new();
        for write in &changes.rows {
            let key = write.key().expect("a keyed row").to_owned();
            match write {
                RowWrite::Create { own, owned, .. } => {
                    let row = Row {
                        own: own.clone(),
                        owned: owned.clone(),
                        version: 1,
                    };
                    self.rows.insert(key, row);
                }
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let row = self.rows.get_mut(&key).expect("the row");
                    assert_eq!(row.version, *expected_version);
                    row.own = merge_patch(&row.own, own);
                    for (use_id, fields) in owned {
                        let held = row.owned.entry(use_id.clone()).or_default();
                        for (field, value) in fields {
                            held.insert(field.clone(), value.clone());
                        }
                    }
                    row.version += 1;
                }
                RowWrite::Delete { .. } => {
                    self.rows.remove(&key);
                    self.sub_rows.retain(|(row, ..), _| *row != key);
                    self.armed.retain(|(row, ..), _| *row != key);
                    deleted.push(key);
                }
                other => panic!("a write this host does not take: {other:?}"),
            }
        }
        if let Action::Deadline {
            key, use_id, name, ..
        } = &command.action
        {
            self.armed
                .remove(&(key.clone(), use_id.clone(), name.clone()));
        }
        for write in &changes.sub_rows {
            let key = write.row.key.clone().expect("a keyed row");
            if deleted.contains(&key) {
                continue;
            }
            let at = (
                key,
                write.use_id.clone(),
                write.table.clone(),
                write.key.clone(),
            );
            match &write.change {
                SubRowChange::Insert { values } => {
                    assert!(!self.sub_rows.contains_key(&at), "a new row: {at:?}");
                    self.sub_rows.insert(at, present(values));
                }
                SubRowChange::Update { values } => {
                    let row = self.sub_rows.get_mut(&at).expect("the row it updates");
                    for (name, value) in values {
                        if value.is_null() {
                            row.remove(name);
                        } else {
                            row.insert(name.clone(), value.clone());
                        }
                    }
                }
                SubRowChange::Delete => {
                    assert!(self.sub_rows.remove(&at).is_some(), "the row it deletes");
                }
                other => panic!("a sub-table change this host does not take: {other:?}"),
            }
        }
        for change in &changes.deadlines {
            let key = change.row().key.clone().expect("a keyed row");
            if deleted.contains(&key) {
                continue;
            }
            let arming = (key, change.use_id().to_owned(), change.name().to_owned());
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

// ---- the host ----

/// A type's rows on a core.
pub struct Host {
    pub core: Core,
    pub spec: TypeSpec,
    pub memory: Memory,
}

impl Host {
    pub fn new(core: Core, spec: TypeSpec) -> Self {
        Host {
            core,
            spec,
            memory: Memory::default(),
        }
    }

    /// Drives a command to its outcome and applies its change set.
    pub fn run(&mut self, caller: &Caller, now: i64, action: Action) -> Outcome {
        self.run_presenting(caller, now, action, Map::new())
    }

    /// Drives a command that presents the caller's preconditions, by use id
    /// (D104), to its outcome and applies its change set.
    pub fn run_presenting(
        &mut self,
        caller: &Caller,
        now: i64,
        action: Action,
        preconditions: Map<String, Value>,
    ) -> Outcome {
        let command = Command::new(caller.clone(), now, action)
            .with_request("r1")
            .with_preconditions(preconditions);
        let outcome = self.core.drive(&self.spec, &command, &mut self.memory);
        if let Outcome::Changed(changes) = &outcome {
            self.memory.apply(&command, changes);
        }
        outcome
    }

    pub fn create(&mut self, caller: &Caller, key: &str, own: Value) -> Outcome {
        self.run(
            caller,
            START,
            Action::Create {
                id: Some(key.to_owned()),
                own: object(own),
                params: Map::new(),
            },
        )
    }

    pub fn update(&mut self, caller: &Caller, key: &str, patch: Value) -> Outcome {
        self.update_at(caller, START, key, patch)
    }

    pub fn update_at(&mut self, caller: &Caller, now: i64, key: &str, patch: Value) -> Outcome {
        self.run(
            caller,
            now,
            Action::Update {
                key: key.to_owned(),
                patch: object(patch),
            },
        )
    }

    pub fn invoke(
        &mut self,
        caller: &Caller,
        key: &str,
        use_id: &str,
        operation: &str,
        params: Value,
    ) -> Outcome {
        self.invoke_at(caller, START, key, use_id, operation, params)
    }

    pub fn invoke_at(
        &mut self,
        caller: &Caller,
        now: i64,
        key: &str,
        use_id: &str,
        operation: &str,
        params: Value,
    ) -> Outcome {
        self.run(
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

    /// Runs every deadline armed and due at a time, as the API's worker
    /// does, as the runner: what each came to.
    pub fn fall_due(&mut self, now: i64, runner: &Caller) -> Vec<(String, Outcome)> {
        let due: Vec<((String, String, String), i64)> = self
            .memory
            .armed
            .iter()
            .filter(|(_, at)| **at <= now)
            .map(|(arming, at)| (arming.clone(), *at))
            .collect();
        due.into_iter()
            .map(|((key, use_id, name), at)| {
                let outcome = self.run(
                    runner,
                    now.max(at),
                    Action::Deadline {
                        key,
                        use_id,
                        name: name.clone(),
                        at,
                        payload: Value::Null,
                    },
                );
                (name, outcome)
            })
            .collect()
    }

    pub fn row(&self, key: &str) -> &Row {
        self.memory.rows.get(key).expect("the row")
    }

    /// A row's sub-table rows, in key order.
    pub fn sub_rows(&self, key: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        self.memory.table(key, use_id, table)
    }

    /// The fields a read of a row shows for a use beyond its owned columns.
    pub fn read_fields(&self, key: &str, use_id: &str, table: &str) -> Map<String, Value> {
        let mut tables = BTreeMap::new();
        tables.insert(table.to_owned(), self.sub_rows(key, use_id, table));
        self.core
            .read_fields(&self.spec, use_id, self.row(key), &tables)
            .expect("its fields")
    }

    /// The fields a read of a row at a time shows for a use beyond its
    /// owned columns (D104).
    pub fn read_fields_at(
        &self,
        key: &str,
        use_id: &str,
        table: &str,
        now: i64,
    ) -> Map<String, Value> {
        let mut tables = BTreeMap::new();
        tables.insert(table.to_owned(), self.sub_rows(key, use_id, table));
        self.core
            .read_fields_at(&self.spec, use_id, self.row(key), &tables, now)
            .expect("its fields")
    }
}

// ---- the define check ----

/// The core's define check of a type.
pub fn define(core: &Core, spec: &TypeSpec) -> Result<(), Refusal> {
    let command = Command::new(caller("alice", &[]), START, Action::Define);
    core.drive(spec, &command, &mut Memory::default())
        .into_result()
        .map(|_| ())
}

/// The reason a define refuses a behavior's config for, after the core's
/// prefix (`type <T>: behavior <B> config: `).
pub fn rule(core: &Core, spec: &TypeSpec, behavior: &str) -> String {
    let refusal = define(core, spec).expect_err("refused");
    refusal
        .message
        .strip_prefix(&format!("type {}: behavior {behavior} config: ", spec.name))
        .unwrap_or(&refusal.message)
        .to_owned()
}

/// What a define refuses a config's shape for: each issue as `<path into
/// the use's entry>: <message>`.
pub fn shape(core: &Core, spec: &TypeSpec) -> Vec<String> {
    let refusal = define(core, spec).expect_err("refused");
    refusal.details.expect("details")["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            format!(
                "{}: {}",
                issue["path"].as_str().unwrap(),
                issue["message"].as_str().unwrap()
            )
        })
        .collect()
}

/// A schema with every description left out, at any depth: what the checks
/// implement, and nothing else.
pub fn undescribed(schema: &Value) -> Value {
    match schema {
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(key, _)| key.as_str() != "description")
                .map(|(key, member)| (key.clone(), undescribed(member)))
                .collect(),
        ),
        Value::Array(items) => Value::Array(items.iter().map(undescribed).collect()),
        other => other.clone(),
    }
}

// ---- values ----

pub fn caller(subject: &str, permissions: &[&str]) -> Caller {
    Caller::new(subject, permissions.iter().copied())
}

pub fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

/// The change set of a command that was applied.
pub fn changed(outcome: Outcome) -> ChangeSet {
    match outcome.into_result() {
        Ok(changes) => changes,
        Err(refusal) => panic!("refused: {}", refusal.error_json()),
    }
}

/// An applied operation's result.
pub fn result(outcome: Outcome) -> Value {
    changed(outcome).result
}

/// A refusal as the engine's error JSON.
pub fn refused(outcome: Outcome) -> Value {
    match outcome.into_result() {
        Ok(changes) => panic!("not refused: {changes:?}"),
        Err(refusal) => refusal.error_json(),
    }
}

/// A refusal's veto: its code and its details, `null` for none.
pub fn veto_of(outcome: Outcome) -> (String, Value) {
    let error = refused(outcome);
    let details = &error["details"];
    (
        details["vetoCode"].as_str().unwrap_or_default().to_owned(),
        details["vetoDetails"].clone(),
    )
}
