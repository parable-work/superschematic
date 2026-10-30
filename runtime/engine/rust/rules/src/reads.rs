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
//! key, a behavior's fields by use id. The host maps them onto its layout,
//! as it maps a change set.

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
    }
}
