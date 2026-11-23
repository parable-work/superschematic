//! The development layout (D117) as the handler names it: what it asks a
//! store to read ([`StoreRead`]), what the store answers ([`StoreAnswer`]),
//! and the writes of one unit it returns for the store to apply whole
//! ([`Unit`], [`StoreWrite`]).
//!
//! The layout is fixed, so a publish writes rows and never a table: each
//! type's versions (its schema documents), its rows (an id, a version, the
//! row's own fields as one JSON object and its uses' owned fields as
//! another), one shared sub-table per behavior keyed by type and id, the
//! rows of the uses' shared tables keyed by type, use, table and key, each
//! with a version of its own (D103's step 4), the armed deadlines, the
//! event queue and the change queue. Every name here
//! is the type's, as a table's would be: a store keys its rows by the type
//! a schema document defines.
//!
//! A store answers each read from the state its transaction sees. The
//! handler asks coarsely (every row of a type, every row of a sub-table)
//! and answers the core's reads, a list's filters and a publish's checks
//! from them itself, so a store learns no rule: development mode promises
//! a table's answers and not a table's speed. A store over each type's own
//! table (runtime mode, D117) answers a list's page, a unique key's holders
//! and the rows a delete reaches in queries of its own instead, when its
//! handler reads by [`Reading::Queries`].

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use superschematic_rules::{Refusal, RowChange, SharedRow, SharedRowChange, SubRow, SubRowChange};

use crate::filter::Sort;

/// How a handler asks a store for what a list, a unique field and a
/// relation's delete read (D117).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Reading {
    /// Every row of the type, which the handler filters, sorts, pages and
    /// checks itself, so the store learns no rule: development mode.
    #[default]
    Rows,
    /// The page a list answers, a unique key's holders and the rows a
    /// delete reaches, each of which the store answers in a query of its
    /// own ([`StoreRead::Page`], [`StoreRead::Holders`],
    /// [`StoreRead::Reached`]): runtime mode, over each type's table, its
    /// columns and its indexes. Each answer is the one the handler makes
    /// from every row by [`Reading::Rows`].
    Queries,
}

/// What the handler asks a store to read.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum StoreRead {
    /// Every type's live version: its last published one
    /// ([`StoreAnswer::Live`]).
    Live,
    /// A type's version defined and not published yet
    /// ([`StoreAnswer::Draft`]).
    Draft {
        #[serde(rename = "type")]
        type_name: String,
    },
    /// One row of a type by its id ([`StoreAnswer::Row`]).
    Row {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
    },
    /// Every row of a type, in id order by code point
    /// ([`StoreAnswer::Rows`]).
    Rows {
        #[serde(rename = "type")]
        type_name: String,
    },
    /// The rows of one sub-table of one use on one row, in key order by
    /// code point ([`StoreAnswer::SubRows`]).
    SubRows {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        table: String,
    },
    /// The rows of one sub-table of one use on every row of a type, in id
    /// and then key order by code point ([`StoreAnswer::TableSubRows`]).
    TableSubRows {
        #[serde(rename = "type")]
        type_name: String,
        use_id: String,
        table: String,
    },
    /// The row of a key of a use's shared table, kept for the type's table
    /// as a whole rather than per row (D103's step 4), with its version
    /// ([`StoreAnswer::SharedRow`]).
    SharedRow {
        #[serde(rename = "type")]
        type_name: String,
        use_id: String,
        table: String,
        key: String,
    },
    /// A deadline armed on a row ([`StoreAnswer::Armed`]).
    Armed {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        name: String,
    },
    /// One page of a list ([`StoreAnswer::Rows`]): at most `limit` of the
    /// rows of a type a filter keeps, in a sort's order, after a cursor,
    /// as [`crate::filter`] answers them over every row: a field with no
    /// value last in either direction, the key breaking ties. Asked by
    /// [`Reading::Queries`] alone.
    Page {
        #[serde(rename = "type")]
        type_name: String,
        #[serde(default)]
        filter: Value,
        #[serde(default)]
        sort: Vec<Sort>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        cursor: Option<String>,
        limit: usize,
    },
    /// The rows of a type whose own fields hold every one of the values,
    /// each compared as a unique index compares it: a unique key's
    /// holders, at most two ([`StoreAnswer::Rows`]). Asked by
    /// [`Reading::Queries`] alone.
    Holders {
        #[serde(rename = "type")]
        type_name: String,
        values: Map<String, Value>,
    },
    /// The rows of a referring type that a delete of a row of `target`
    /// reaches through one relation, `member`: the ones whose member holds
    /// the row's key, as the store's own delete of the row reaches them
    /// through its foreign key, and none when that delete is a soft
    /// delete, which fires no foreign key ([`StoreAnswer::Rows`]). Asked
    /// by [`Reading::Queries`] alone.
    Reached {
        #[serde(rename = "type")]
        type_name: String,
        member: String,
        target: String,
        id: String,
    },
}

/// A store's answer to one read, of the variant the read asks for.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum StoreAnswer {
    Live(Vec<Version>),
    Draft(Option<Draft>),
    Row(Option<StoredRow>),
    Rows(Vec<StoredRow>),
    SubRows(Vec<SubRow>),
    TableSubRows(Vec<HeldSubRow>),
    SharedRow(Option<SharedRow>),
    Armed(Option<Armed>),
}

/// A published version of a type: its number, from 1, and the schema
/// document it was published from.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Version {
    #[serde(rename = "type")]
    pub type_name: String,
    pub version: i64,
    pub document: Value,
}

/// A type's version defined and not published yet.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Draft {
    #[serde(rename = "type")]
    pub type_name: String,
    pub document: Value,
}

/// One row as a store keeps it: its id, the version its writes are held
/// to (1 at the create, one more with each write), its own fields as one
/// JSON object (`data`) and its uses' owned fields by use id and declared
/// name (`owned`), a field with no value absent.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct StoredRow {
    pub id: String,
    pub version: i64,
    #[serde(default)]
    pub data: Map<String, Value>,
    #[serde(default)]
    pub owned: BTreeMap<String, Map<String, Value>>,
}

/// A sub-table row with the id of the row it belongs to.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct HeldSubRow {
    pub id: String,
    pub row: SubRow,
}

/// When an armed deadline falls due, in epoch milliseconds, and what it was
/// armed with.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Armed {
    pub at: i64,
    #[serde(default)]
    pub payload: Value,
}

/// One write of a unit. A store applies a unit's writes in order, all of
/// them or none.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "write",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum StoreWrite {
    /// Keeps a type's draft, replacing one kept before.
    PutDraft {
        #[serde(rename = "type")]
        type_name: String,
        document: Value,
    },
    /// Keeps a type's next version, which is live from the unit's commit,
    /// and drops its draft. Moved when the type's live version is not
    /// `version - 1` (0 for none).
    PutVersion {
        #[serde(rename = "type")]
        type_name: String,
        version: i64,
        document: Value,
    },
    /// A new row at version 1. Refused as `conflict` when the type holds a
    /// row of the id.
    Insert {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        data: Map<String, Value>,
        owned: BTreeMap<String, Map<String, Value>>,
    },
    /// A row's own and owned fields, whole, with its version moved on by
    /// one. Moved when the row is not at `expected`.
    Update {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        expected: i64,
        data: Map<String, Value>,
        owned: BTreeMap<String, Map<String, Value>>,
    },
    /// A row's own and owned fields, whole, its version kept: a publish's
    /// rewrite of a row it keeps, as a table's DDL drops a column without
    /// moving a row's version. Moved when the row is gone.
    Rewrite {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        data: Map<String, Value>,
        owned: BTreeMap<String, Map<String, Value>>,
    },
    /// A row's delete, its sub-table rows and deadlines with it. Moved when
    /// the row is gone, or not at `expected` when one is given.
    Delete {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        expected: Option<i64>,
    },
    /// A row a change command found gone (D103): its deadlines and its
    /// sub-table rows go, unless a row of the id is there again.
    Gone {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
    },
    /// A write of one sub-table row in its behavior's shared sub-table: an
    /// insert (moved when the key is held), an update that sets the values
    /// given, a null clearing one (moved when the key is not held), or a
    /// delete (moved when the key is not held).
    SubRow {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        behavior: String,
        use_id: String,
        table: String,
        key: String,
        change: SubRowChange,
    },
    /// A write of one row of a use's shared table (D103's step 4): an
    /// insert at version 1 (moved when the key is held), or an update that
    /// sets the values given, a null clearing one, and moves the version on
    /// by one (moved when the row is not at the version it expects). A
    /// handler puts these first in its unit.
    SharedRow {
        #[serde(rename = "type")]
        type_name: String,
        use_id: String,
        table: String,
        key: String,
        change: SharedRowChange,
    },
    /// Every sub-table row of one use of a type, in its behavior's shared
    /// sub-table, and every row of the use's shared tables: what a publish
    /// that removes the use drops.
    DropUse {
        #[serde(rename = "type")]
        type_name: String,
        behavior: String,
        use_id: String,
    },
    /// Arms a deadline, replacing its arming.
    Arm {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        name: String,
        at: i64,
        payload: Value,
    },
    /// Clears a deadline.
    Clear {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        name: String,
    },
    /// Clears every deadline of one use of a type: what a publish that
    /// removes the use drops.
    ClearUse {
        #[serde(rename = "type")]
        type_name: String,
        use_id: String,
    },
    /// Spends the arming a deadline command ran on. Moved when the deadline
    /// is not armed at `at`.
    Spend {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        name: String,
        at: i64,
    },
    /// Holds a fire a guard vetoed back: the deadline stays armed at its
    /// time and falls due again at `due`, a minute on (D103's release).
    Release {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        name: String,
        due: i64,
    },
    /// An event a behavior emitted about a row (D104), at the command's
    /// time, to the event queue.
    Event {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        use_id: String,
        kind: String,
        at: i64,
        payload: Value,
    },
    /// A write of a row whose writes a behavior hears, to the change queue
    /// (D103), which the host runs as a change command after the commit.
    Change {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        change: RowChange,
    },
    /// Spends a message of the change queue a change command ran, by the
    /// number the queue gave it. Moved when the queue holds none of it.
    Dequeue { seq: i64 },
}

/// The writes a store applies as one unit, in order, all of them or none.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Unit {
    #[serde(default)]
    pub writes: Vec<StoreWrite>,
}

impl Unit {
    /// Whether the unit writes nothing.
    pub fn is_empty(&self) -> bool {
        self.writes.is_empty()
    }
}

/// What a store answered so far for one request, each read with its
/// answer. A host starts a request with none and starts again with none
/// when it runs the request again from fresh state.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct StoreAnswers {
    #[serde(default)]
    pub reads: Vec<(StoreRead, StoreAnswer)>,
}

impl StoreAnswers {
    pub fn new() -> Self {
        Self::default()
    }

    /// Records an answer, replacing one recorded for the same read.
    pub fn answer(&mut self, read: StoreRead, answer: StoreAnswer) {
        match self.reads.iter_mut().find(|(known, _)| *known == read) {
            Some((_, known)) => *known = answer,
            None => self.reads.push((read, answer)),
        }
    }

    /// The answer to a read; `None` when the store gave none.
    pub fn get(&self, read: &StoreRead) -> Option<&StoreAnswer> {
        self.reads
            .iter()
            .find(|(known, _)| known == read)
            .map(|(_, answer)| answer)
    }

    /// Forgets every answer, for a run again from fresh state.
    pub fn clear(&mut self) {
        self.reads.clear();
    }
}

/// A deadline that fell due, as a host's runner reads it from the store:
/// its row, its use and name, the time it was armed to fall due at, and its
/// payload.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DueDeadline {
    #[serde(rename = "type")]
    pub type_name: String,
    pub id: String,
    pub use_id: String,
    pub name: String,
    pub at: i64,
    #[serde(default)]
    pub payload: Value,
}

/// A message of the change queue, as a host's runner reads it: the number
/// the queue gave it, its row, and the write.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct QueuedChange {
    pub seq: i64,
    #[serde(rename = "type")]
    pub type_name: String,
    pub id: String,
    pub change: RowChange,
}

/// An event of the event queue (D104), as a host reads it: the number the
/// queue gave it, its row, the use that emitted it, its kind, the time of
/// the command that emitted it and its payload.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct QueuedEvent {
    pub seq: i64,
    #[serde(rename = "type")]
    pub type_name: String,
    pub id: String,
    pub use_id: String,
    pub kind: String,
    pub at: i64,
    #[serde(default)]
    pub payload: Value,
}

/// What a store does for a host that drives the handler in one process
/// ([`crate::serve`]): each request in one transaction, which holds the
/// store's one writer from its start, so every lock a behavior asks for is
/// held from the request's start too, and the unit's fences hold against
/// any other writer.
pub trait Store {
    /// The deadlines due at a time, earliest first: what the runner runs.
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal>;
    /// The change queue's messages, first in first out.
    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal>;
    /// The event queue's messages after a number, in the order written:
    /// what an event stream reads.
    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal>;
    /// Begins the request's transaction.
    fn begin(&mut self) -> Result<(), Refusal>;
    /// Commits it.
    fn commit(&mut self) -> Result<(), Refusal>;
    /// Rolls it back.
    fn rollback(&mut self) -> Result<(), Refusal>;
    /// Answers one read in the transaction.
    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal>;
    /// Applies a unit in the transaction: true when every write applied,
    /// false, with none applied, when a fence did not hold (a row moved),
    /// and a refusal, with none applied, when the unit cannot apply as it
    /// stands (a row of the id exists).
    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal>;
}

/// The refusal of a store that reads rows, development mode's, of a query
/// read only a handler that reads by [`Reading::Queries`] asks.
pub fn unqueried(read: &StoreRead) -> Refusal {
    Refusal::internal(format!(
        "this store answers every row of a type and no query of its own, and the handler asked {}: serve it with Reading::Rows",
        serde_json::to_string(read).unwrap_or_default()
    ))
}
