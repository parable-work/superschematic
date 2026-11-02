//! The read exchange (D103): the core asks for what it reads, the host
//! answers, and the core runs again from the top with every answer it has
//! so far. The core is a pure function of a type, a command and the
//! answers, so running again is how it resumes, and the same answers give
//! the same step. Before the reads a rule across rows decides on (the last
//! unit of stock, two bookings of one room), the core asks for the lock
//! keys that serialize them, and the host takes each before it answers.
//!
//! Each answer records how many locks the host held when it made the read
//! ([`Answered::held`]), and the core decides on no answer made before the
//! latest lock: it asks for the read again, so every read a rule across
//! rows decides on is made under its locks whatever the host does with the
//! answers it gave before. A host only appends: [`Answers::answer`] and
//! [`Answers::hold`] keep the count, and a host that writes the answers as
//! JSON writes `held` with each.
//!
//! A request names logical targets only: a type by its name, a row by its
//! key, a behavior's fields by use id, a sub-table by its row, its use's id
//! and its declared name. The host maps them onto its layout, as it maps a
//! change set.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::spec::TypeSpec;

/// What the core asks the host to read. The list grows as behaviors need
/// more (a sub-table's rows by a condition, a count), so a host matches it
/// with a fallback arm.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
#[non_exhaustive]
pub enum ReadRequest {
    /// One row of a type by its key: its own fields, its uses' owned
    /// fields and its version ([`Answer::Row`]).
    Row {
        #[serde(rename = "type")]
        type_name: String,
        key: String,
    },
    /// Whether a type has any rows ([`Answer::HasRows`]): what a config
    /// change that would strand a row's state reads.
    HasRows {
        #[serde(rename = "type")]
        type_name: String,
    },
    /// The live version of a type, its uses and their configs
    /// ([`Answer::Spec`]): what a call on another type's row binds, and
    /// what a behavior that reads another type's config reads.
    Spec {
        #[serde(rename = "type")]
        type_name: String,
    },
    /// The rows of one sub-table of one use on one row
    /// ([`Answer::SubRows`]), each by its key, in key order (by code
    /// point): what a behavior that keeps state in a sub-table reads of
    /// its row ([`crate::View::sub_rows`]).
    SubRows {
        #[serde(rename = "type")]
        type_name: String,
        key: String,
        use_id: String,
        table: String,
    },
    /// The rows of a type whose own field holds a key ([`Answer::Linked`]),
    /// in the key column's order, at most `limit`, after the key `after`
    /// when given: what a rule over the rows that link to a row reads
    /// (Reactions' allTerminal). A soft-deletable table's live rows alone.
    Linked {
        #[serde(rename = "type")]
        type_name: String,
        field: String,
        key: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        after: Option<String>,
        limit: usize,
    },
}

/// How many rows a linked read asks for at a time: Links' page (500).
pub const LINKED_PAGE: usize = 500;

/// The key a field's value names, as a command names a row by it: a string
/// itself, and a whole number its decimal text, the form a table keyed by a
/// number has its keys in (a to-one relation's column holds its target's
/// key as the target's key column types it); None for any other value.
pub fn key_of(value: &Value) -> Option<String> {
    match value {
        Value::String(key) => Some(key.clone()),
        Value::Number(n) if n.is_i64() || n.is_u64() => Some(n.to_string()),
        Value::Number(n) => n
            .as_f64()
            .filter(|f| f.fract() == 0.0 && f.abs() < 9_007_199_254_740_992.0)
            .map(|f| format!("{}", f as i64)),
        _ => None,
    }
}

impl ReadRequest {
    /// A row of a type by its key.
    pub fn row(type_name: impl Into<String>, key: impl Into<String>) -> Self {
        ReadRequest::Row {
            type_name: type_name.into(),
            key: key.into(),
        }
    }

    /// Whether a type has any rows.
    pub fn has_rows(type_name: impl Into<String>) -> Self {
        ReadRequest::HasRows {
            type_name: type_name.into(),
        }
    }

    /// The live version of a type.
    pub fn spec(type_name: impl Into<String>) -> Self {
        ReadRequest::Spec {
            type_name: type_name.into(),
        }
    }

    /// The rows of one sub-table of one use on a row of a type.
    pub fn sub_rows(
        type_name: impl Into<String>,
        key: impl Into<String>,
        use_id: impl Into<String>,
        table: impl Into<String>,
    ) -> Self {
        ReadRequest::SubRows {
            type_name: type_name.into(),
            key: key.into(),
            use_id: use_id.into(),
            table: table.into(),
        }
    }

    /// One page of the rows of a type whose own field holds a key:
    /// [`LINKED_PAGE`] at most, after a key when given.
    pub fn linked(
        type_name: impl Into<String>,
        field: impl Into<String>,
        key: impl Into<String>,
        after: Option<&str>,
    ) -> Self {
        ReadRequest::Linked {
            type_name: type_name.into(),
            field: field.into(),
            key: key.into(),
            after: after.map(str::to_owned),
            limit: LINKED_PAGE,
        }
    }
}

/// One row as the host read it: its own fields, each use's owned fields by
/// use id, and the version its writes are held to (D103: the column
/// `@optimistic` keeps).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Row {
    /// The author's fields, by field name.
    #[serde(default)]
    pub own: Map<String, Value>,
    /// The behaviors' fields: by use id, then by the field's declared name.
    /// A field with no value is absent or null.
    #[serde(default)]
    pub owned: BTreeMap<String, Map<String, Value>>,
    pub version: i64,
}

impl Row {
    /// An owned field's value; `None` when it has none.
    pub fn owned(&self, use_id: &str, field: &str) -> Option<&Value> {
        self.owned
            .get(use_id)
            .and_then(|fields| fields.get(field))
            .filter(|value| !value.is_null())
    }
}

/// One sub-table row as the host read it: its key and its values by the
/// declared field's name, the key's field left out, a NULL column absent.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SubRow {
    pub key: String,
    #[serde(default)]
    pub values: Map<String, Value>,
}

impl SubRow {
    /// A row by its key with its values, a null among them left out.
    pub fn new(key: impl Into<String>, values: Map<String, Value>) -> Self {
        SubRow {
            key: key.into(),
            values: values
                .into_iter()
                .filter(|(_, value)| !value.is_null())
                .collect(),
        }
    }

    /// A value; `None` when it has none.
    pub fn value(&self, field: &str) -> Option<&Value> {
        self.values.get(field).filter(|value| !value.is_null())
    }
}

/// The host's answer to one request, of the variant the request asks for.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
#[non_exhaustive]
pub enum Answer {
    /// The row, or `None` when the type has no row with the key.
    Row(Option<Row>),
    HasRows(bool),
    /// The type's live version, or `None` when no type has the name.
    Spec(Option<TypeSpec>),
    /// The sub-table's rows of the row, in key order; none for a row that
    /// has none, or that does not exist.
    SubRows(Vec<SubRow>),
    /// The rows a [`ReadRequest::Linked`] asks for, and whether more follow.
    Linked(LinkedRows),
}

/// One page of the rows of a type whose own field holds a key, each with
/// its key, in key order, and whether more follow it. A type the host does
/// not have answers no rows. The core's rules never depend on the order; a
/// host orders by its key column (the reference host by code point).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LinkedRows {
    #[serde(default)]
    pub rows: Vec<KeyedRow>,
    #[serde(default)]
    pub more: bool,
}

/// A row with its key, as a linked read answers it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct KeyedRow {
    pub key: String,
    pub row: Row,
}

/// A key the host locks before it answers the reads a rule across rows
/// decides on: on Postgres a transaction's advisory lock, on SQLite and D1
/// the one writer that already holds (D103). The text is the behavior's
/// logical name for what it serializes (`Stock/sku-1`); the host hashes or
/// maps it as its locks need.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct LockKey(pub String);

impl LockKey {
    pub fn new(key: impl Into<String>) -> Self {
        LockKey(key.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl From<&str> for LockKey {
    fn from(key: &str) -> Self {
        LockKey(key.to_owned())
    }
}

impl From<String> for LockKey {
    fn from(key: String) -> Self {
        LockKey(key)
    }
}

/// One answered request, and how many locks the host held when it made the
/// read.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Answered {
    pub request: ReadRequest,
    pub answer: Answer,
    /// The number of locks the host held when it made the read. An answer
    /// made under fewer locks than the host holds now was made before a
    /// lock, and the core asks for the read again.
    #[serde(default)]
    pub held: usize,
}

/// What the host has answered so far for one command: each request with
/// its answer, and the lock keys it holds, in the order it took them. A
/// host starts a command with none, adds what each step asks for, and
/// starts again with none when it runs the command again from fresh state
/// (a version that did not match). The core decides only on the answers
/// made under every lock the host holds ([`Answers::get`]).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Answers {
    #[serde(default)]
    pub reads: Vec<Answered>,
    #[serde(default)]
    pub locks: Vec<LockKey>,
}

impl Answers {
    pub fn new() -> Self {
        Self::default()
    }

    /// Records an answer made under the locks held now, replacing one
    /// recorded for the same request.
    pub fn answer(&mut self, request: ReadRequest, answer: Answer) {
        let held = self.locks.len();
        match self.reads.iter_mut().find(|known| known.request == request) {
            Some(known) => {
                known.answer = answer;
                known.held = held;
            }
            None => self.reads.push(Answered {
                request,
                answer,
                held,
            }),
        }
    }

    /// The answers with one more.
    pub fn with(mut self, request: ReadRequest, answer: Answer) -> Self {
        self.answer(request, answer);
        self
    }

    /// Records a lock the host took. Every answer recorded before it was
    /// made before the lock, so the core asks for its read again.
    pub fn hold(&mut self, key: LockKey) {
        if !self.locks.contains(&key) {
            self.locks.push(key);
        }
    }

    /// The answer to a request made under every lock the host holds; `None`
    /// when the host gave none, or gave it before a lock it took since.
    pub fn get(&self, request: &ReadRequest) -> Option<&Answer> {
        self.reads
            .iter()
            .find(|known| known.request == *request && known.held >= self.locks.len())
            .map(|known| &known.answer)
    }

    /// Whether the host holds a lock.
    pub fn holds(&self, key: &LockKey) -> bool {
        self.locks.contains(key)
    }

    /// Forgets every answer and lock, for a run again from fresh state.
    pub fn clear(&mut self) {
        self.reads.clear();
        self.locks.clear();
    }

    pub fn is_empty(&self) -> bool {
        self.reads.is_empty() && self.locks.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn an_answer_replaces_the_one_before_it() {
        let mut answers = Answers::new();
        answers.answer(ReadRequest::has_rows("Order"), Answer::HasRows(false));
        answers.answer(ReadRequest::has_rows("Order"), Answer::HasRows(true));
        assert_eq!(answers.reads.len(), 1);
        assert_eq!(
            answers.get(&ReadRequest::has_rows("Order")),
            Some(&Answer::HasRows(true))
        );
        assert_eq!(answers.get(&ReadRequest::row("Order", "o1")), None);
        answers.hold(LockKey::from("Stock/1"));
        answers.hold(LockKey::from("Stock/1"));
        assert_eq!(answers.locks.len(), 1);
        answers.clear();
        assert!(answers.is_empty());
    }

    #[test]
    fn an_answer_made_before_a_lock_is_not_decided_on() {
        let mut answers = Answers::new();
        let row = ReadRequest::row("Stock", "s1");
        answers.answer(row.clone(), Answer::Row(None));
        assert_eq!(answers.get(&row), Some(&Answer::Row(None)));
        // The host takes a lock and forgets nothing: the answer before it
        // is stale, and the core asks again.
        answers.hold(LockKey::from("Stock/s1"));
        assert_eq!(answers.get(&row), None);
        // Answered again under the lock, it holds.
        answers.answer(row.clone(), Answer::Row(None));
        assert_eq!(answers.get(&row), Some(&Answer::Row(None)));
        assert_eq!(answers.reads.len(), 1);
        assert_eq!(answers.reads[0].held, 1);
        // An answer a host wrote as JSON without `held` was made before
        // any lock.
        let written: Answers = serde_json::from_value(json!({
            "reads": [{"request": {"kind": "hasRows", "type": "Shelf"}, "answer": {"hasRows": true}}],
            "locks": ["Stock/s1"]
        }))
        .unwrap();
        assert_eq!(written.get(&ReadRequest::has_rows("Shelf")), None);
    }

    #[test]
    fn requests_and_answers_read_as_json() {
        assert_eq!(
            serde_json::to_value(ReadRequest::row("Order", "o1")).unwrap(),
            json!({"kind": "row", "type": "Order", "key": "o1"})
        );
        assert_eq!(
            serde_json::to_value(ReadRequest::has_rows("Order")).unwrap(),
            json!({"kind": "hasRows", "type": "Order"})
        );
        assert_eq!(
            serde_json::to_value(ReadRequest::spec("Stock")).unwrap(),
            json!({"kind": "spec", "type": "Stock"})
        );
        assert_eq!(
            serde_json::to_value(Answer::Spec(Some(TypeSpec::new("Stock")))).unwrap(),
            json!({"spec": {"name": "Stock", "uses": []}})
        );
        assert_eq!(
            serde_json::to_value(ReadRequest::sub_rows("Ticket", "t1", "Timers", "deadlines"))
                .unwrap(),
            json!({"kind": "subRows", "type": "Ticket", "key": "t1", "useId": "Timers", "table": "deadlines"})
        );
        let answer: Answer = serde_json::from_value(json!({"subRows": [
            {"key": "respond", "values": {"baseAt": 1791536400000_i64, "armedFrom": "1791536400000", "dueAt": 1791536401500_i64, "fired": false}},
            {"key": "idle"}
        ]}))
        .unwrap();
        let Answer::SubRows(rows) = answer else {
            panic!("sub-table rows");
        };
        assert_eq!(rows[0].value("fired"), Some(&json!(false)));
        assert!(rows[1].values.is_empty());
        assert_eq!(
            SubRow::new(
                "idle",
                json!({"dueAt": null, "fired": true})
                    .as_object()
                    .cloned()
                    .unwrap()
            ),
            SubRow {
                key: "idle".into(),
                values: json!({"fired": true}).as_object().cloned().unwrap()
            }
        );
        let answer: Answer = serde_json::from_value(json!({"row": {
            "own": {"title": "Desk"},
            "owned": {"Workflow": {"status": "draft", "enteredAt": null}},
            "version": 3
        }}))
        .unwrap();
        let Answer::Row(Some(row)) = answer else {
            panic!("a row");
        };
        assert_eq!(row.owned("Workflow", "status"), Some(&json!("draft")));
        assert_eq!(row.owned("Workflow", "enteredAt"), None);
        assert_eq!(
            serde_json::to_value(Answer::Row(None)).unwrap(),
            json!({"row": null})
        );
        assert_eq!(
            serde_json::to_value(ReadRequest::linked("Task", "projectId", "p1", Some("t9")))
                .unwrap(),
            json!({"kind": "linked", "type": "Task", "field": "projectId", "key": "p1", "after": "t9", "limit": 500})
        );
        assert_eq!(
            serde_json::to_value(ReadRequest::linked("Task", "projectId", "p1", None)).unwrap(),
            json!({"kind": "linked", "type": "Task", "field": "projectId", "key": "p1", "limit": 500})
        );
        let answer: Answer = serde_json::from_value(json!({"linked": {"rows": [
            {"key": "t1", "row": {"own": {"projectId": "p1"}, "owned": {"Workflow": {"status": "done"}}, "version": 3}}
        ], "more": false}}))
        .unwrap();
        let Answer::Linked(page) = answer else {
            panic!("a linked page");
        };
        assert_eq!(page.rows[0].key, "t1");
        assert_eq!(page.rows[0].row.version, 3);
        assert!(!page.more);
        assert_eq!(
            serde_json::to_value(Answer::Linked(LinkedRows::default())).unwrap(),
            json!({"linked": {"rows": [], "more": false}})
        );
    }
}
