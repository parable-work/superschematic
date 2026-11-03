//! What a command changes (D103), in logical names a host maps onto its
//! layout: each row it writes, by its type and key (a create, an update
//! held to the version the host read it at, or a delete), the owned fields
//! each use set and the own fields a patch sets, the sub-table rows to
//! insert, update or delete, the outbox events the behaviors emit, the
//! deadlines to arm or clear, and the value the call returns. A command
//! writes more than one row when a use reaches another row through its
//! operations or creates one (`Cx::call_on`, `Cx::create_row`), as a hold
//! on a stock row or a booking on a calendar does; every entry names the
//! row it belongs to.
//!
//! The host applies a change set in one transaction with the caller's
//! writes, all of it or none: when any row it writes is no longer at the
//! version it expects, it runs the command again from fresh state.
//!
//! A write of a row, whoever made it, comes back to the core as a
//! [`RowChange`] (D103): the table's change trigger writes one to the
//! outbox per insert, update and delete, with the row's images before and
//! after it ([`RowImage`]), and the host hands each to the core as a change
//! command ([`crate::Action::Change`]).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

/// Everything one command changes, and what it returns.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ChangeSet {
    /// The rows the command writes, each held to its own version: the
    /// command's own row first, when it writes it, then each other row in
    /// the order the run reached it. Empty for a command that writes no row
    /// (a read-only operation, the define and publish checks).
    #[serde(default)]
    pub rows: Vec<RowWrite>,
    /// Sub-table row writes, in the order the behaviors made them.
    #[serde(default)]
    pub sub_rows: Vec<SubRowWrite>,
    /// Outbox events, in the order the behaviors emitted them.
    #[serde(default)]
    pub events: Vec<OutboxEvent>,
    /// Deadlines to arm or clear, in order: a later entry for the same row,
    /// use and name replaces an earlier one.
    #[serde(default)]
    pub deadlines: Vec<DeadlineChange>,
    /// What the call returns: an operation's result, null for the rest.
    #[serde(default)]
    pub result: Value,
}

impl ChangeSet {
    /// The write of a row, by its type and key.
    pub fn row(&self, type_name: &str, key: &str) -> Option<&RowWrite> {
        self.rows
            .iter()
            .find(|write| write.type_name() == type_name && write.key() == Some(key))
    }

    /// An owned field the first row write sets, which is the command's own
    /// row's: by use id and field name.
    pub fn owned(&self, use_id: &str, field: &str) -> Option<&Value> {
        self.rows.first().and_then(|row| row.owned(use_id, field))
    }
}

/// A row a change set names: its type and its key. The key is `None` only
/// for the row a create without an id makes, which the host keys as its
/// layout does; every entry that names the row so is that row's.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RowRef {
    #[serde(rename = "type")]
    pub type_name: String,
    #[serde(default)]
    pub key: Option<String>,
}

impl RowRef {
    /// A row of a type by its key; `None` for the row a create without an
    /// id makes.
    pub fn new(type_name: impl Into<String>, key: Option<&str>) -> Self {
        RowRef {
            type_name: type_name.into(),
            key: key.map(str::to_owned),
        }
    }

    /// Whether this names a row of a type by its key.
    pub fn is(&self, type_name: &str, key: &str) -> bool {
        self.type_name == type_name && self.key.as_deref() == Some(key)
    }
}

/// The write of one row. The list of kinds is open, so a host matches it
/// with a fallback arm.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
#[non_exhaustive]
pub enum RowWrite {
    /// A new row of a type with its own fields and the owned fields its
    /// uses' initializers set. `key` is the create's id; `None` leaves it
    /// to the host's layout.
    Create {
        #[serde(rename = "type")]
        type_name: String,
        key: Option<String>,
        own: Map<String, Value>,
        owned: BTreeMap<String, Map<String, Value>>,
    },
    /// An update of a row the host read at `expected_version`: the merge
    /// patch of its own fields (empty when the command patched none) and
    /// the owned fields set, by use id. The host moves the version on.
    Update {
        #[serde(rename = "type")]
        type_name: String,
        key: String,
        expected_version: i64,
        own: Map<String, Value>,
        owned: BTreeMap<String, Map<String, Value>>,
    },
    /// The row's delete, held to the version the host read. Its sub-table
    /// rows and deadlines go with it.
    Delete {
        #[serde(rename = "type")]
        type_name: String,
        key: String,
        expected_version: i64,
    },
    /// A row a change command found gone (D103): no write of the row, which
    /// is not there. Its deadlines go, and so do its sub-table rows when no
    /// row of the key is left at all (a soft-deleted row keeps them, as a
    /// soft delete through the core does).
    Gone {
        #[serde(rename = "type")]
        type_name: String,
        key: String,
    },
}

impl RowWrite {
    /// The row's type.
    pub fn type_name(&self) -> &str {
        match self {
            RowWrite::Create { type_name, .. }
            | RowWrite::Update { type_name, .. }
            | RowWrite::Delete { type_name, .. }
            | RowWrite::Gone { type_name, .. } => type_name,
        }
    }

    /// The row's key; `None` for a create the host keys.
    pub fn key(&self) -> Option<&str> {
        match self {
            RowWrite::Create { key, .. } => key.as_deref(),
            RowWrite::Update { key, .. }
            | RowWrite::Delete { key, .. }
            | RowWrite::Gone { key, .. } => Some(key),
        }
    }

    /// The row the write is of.
    pub fn row(&self) -> RowRef {
        RowRef::new(self.type_name(), self.key())
    }

    /// The version the row must be at; `None` for a create and a row
    /// found gone, which nothing fences.
    pub fn expected_version(&self) -> Option<i64> {
        match self {
            RowWrite::Create { .. } | RowWrite::Gone { .. } => None,
            RowWrite::Update {
                expected_version, ..
            }
            | RowWrite::Delete {
                expected_version, ..
            } => Some(*expected_version),
        }
    }

    /// An owned field the write sets.
    pub fn owned(&self, use_id: &str, field: &str) -> Option<&Value> {
        match self {
            RowWrite::Create { owned, .. } | RowWrite::Update { owned, .. } => {
                owned.get(use_id).and_then(|fields| fields.get(field))
            }
            RowWrite::Delete { .. } | RowWrite::Gone { .. } => None,
        }
    }
}

/// What a write did to a row (D103).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
#[non_exhaustive]
pub enum WriteKind {
    Insert,
    Update,
    Delete,
}

impl WriteKind {
    /// The write as the outbox names it: `insert`, `update` or `delete`.
    pub fn as_str(self) -> &'static str {
        match self {
            WriteKind::Insert => "insert",
            WriteKind::Update => "update",
            WriteKind::Delete => "delete",
        }
    }
}

/// A row's fields as a write found or left them: its own fields by name and
/// its uses' owned fields by use id and declared name, a field with no
/// value absent.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RowImage {
    #[serde(default)]
    pub own: Map<String, Value>,
    #[serde(default)]
    pub owned: BTreeMap<String, Map<String, Value>>,
}

impl RowImage {
    /// An own field's value; `None` when it has none.
    pub fn own_field(&self, name: &str) -> Option<&Value> {
        self.own.get(name).filter(|value| !value.is_null())
    }

    /// An owned field's value, by use id and declared name; `None` when it
    /// has none.
    pub fn owned(&self, use_id: &str, field: &str) -> Option<&Value> {
        self.owned
            .get(use_id)
            .and_then(|fields| fields.get(field))
            .filter(|value| !value.is_null())
    }
}

/// A write of a row the table's change trigger wrote to the outbox (D103):
/// what it did, when (epoch milliseconds, the database's clock), and the row
/// before it (none for an insert) and after it (none for a delete).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RowChange {
    pub write: WriteKind,
    pub at: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub before: Option<RowImage>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub after: Option<RowImage>,
}

/// One write of a sub-table row: the row that owns it, the use that owns
/// the sub-table, the sub-table's declared name, and the row's key in it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SubRowWrite {
    pub row: RowRef,
    pub use_id: String,
    pub table: String,
    pub key: String,
    pub change: SubRowChange,
}

/// What happens to a sub-table row. The list of kinds is open, so a host
/// matches it with a fallback arm.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
#[non_exhaustive]
pub enum SubRowChange {
    /// A new row with its values, by column's logical name.
    Insert {
        values: Map<String, Value>,
    },
    /// The values it sets on a row that exists.
    Update {
        values: Map<String, Value>,
    },
    Delete,
}

/// An event a behavior emits, which the host writes to the outbox in the
/// change set's transaction (D103): the row it is about, its kind, the use
/// that emitted it and its payload.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OutboxEvent {
    pub row: RowRef,
    pub kind: String,
    pub use_id: String,
    pub payload: Value,
}

/// A deadline of a row a use arms or clears. A deadline is named by its
/// row, its use and its name; arming one that is armed moves it. The list
/// of kinds is open, so a host matches it with a fallback arm.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
#[non_exhaustive]
pub enum DeadlineChange {
    /// Falls due at `at` (epoch milliseconds), when the host runs the
    /// deadline command with the time and the payload.
    Arm {
        row: RowRef,
        use_id: String,
        name: String,
        at: i64,
        payload: Value,
    },
    Clear {
        row: RowRef,
        use_id: String,
        name: String,
    },
}

impl DeadlineChange {
    /// The row the deadline is of.
    pub fn row(&self) -> &RowRef {
        match self {
            DeadlineChange::Arm { row, .. } | DeadlineChange::Clear { row, .. } => row,
        }
    }

    /// The use whose deadline it is.
    pub fn use_id(&self) -> &str {
        match self {
            DeadlineChange::Arm { use_id, .. } | DeadlineChange::Clear { use_id, .. } => use_id,
        }
    }

    /// The deadline's name.
    pub fn name(&self) -> &str {
        match self {
            DeadlineChange::Arm { name, .. } | DeadlineChange::Clear { name, .. } => name,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_change_set_reads_as_json() {
        let mut owned = BTreeMap::new();
        owned.insert(
            "Workflow".to_owned(),
            json!({"status": "open"}).as_object().cloned().unwrap(),
        );
        let changes = ChangeSet {
            rows: vec![
                RowWrite::Update {
                    type_name: "Order".into(),
                    key: "o1".into(),
                    expected_version: 1,
                    own: Map::new(),
                    owned,
                },
                RowWrite::Update {
                    type_name: "Stock".into(),
                    key: "s1".into(),
                    expected_version: 4,
                    own: Map::new(),
                    owned: BTreeMap::new(),
                },
            ],
            deadlines: vec![DeadlineChange::Clear {
                row: RowRef::new("Order", Some("o1")),
                use_id: "Workflow".into(),
                name: "expire".into(),
            }],
            result: json!({"from": "draft", "to": "open"}),
            ..ChangeSet::default()
        };
        assert_eq!(changes.owned("Workflow", "status"), Some(&json!("open")));
        assert_eq!(
            changes
                .row("Stock", "s1")
                .and_then(RowWrite::expected_version),
            Some(4)
        );
        assert!(changes.row("Stock", "s2").is_none());
        assert_eq!(
            changes.deadlines[0].row(),
            &RowRef::new("Order", Some("o1"))
        );
        assert_eq!(
            serde_json::to_value(RowWrite::Gone {
                type_name: "Task".into(),
                key: "t1".into()
            })
            .unwrap(),
            json!({"kind": "gone", "type": "Task", "key": "t1"})
        );
        let gone: RowWrite =
            serde_json::from_value(json!({"kind": "gone", "type": "Task", "key": "t1"})).unwrap();
        assert_eq!(gone.key(), Some("t1"));
        assert_eq!(gone.expected_version(), None);
        assert_eq!(gone.owned("Workflow", "status"), None);
        assert_eq!(
            serde_json::to_value(&changes).unwrap(),
            json!({
                "rows": [
                    {"kind": "update", "type": "Order", "key": "o1", "expectedVersion": 1, "own": {}, "owned": {"Workflow": {"status": "open"}}},
                    {"kind": "update", "type": "Stock", "key": "s1", "expectedVersion": 4, "own": {}, "owned": {}}
                ],
                "subRows": [],
                "events": [],
                "deadlines": [{"kind": "clear", "row": {"type": "Order", "key": "o1"}, "useId": "Workflow", "name": "expire"}],
                "result": {"from": "draft", "to": "open"}
            })
        );
    }

    #[test]
    fn a_row_change_reads_as_the_outbox_writes_it() {
        let change: RowChange = serde_json::from_value(json!({
            "write": "update", "at": 1001000,
            "before": {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "todo", "enteredAt": 1000000}}},
            "after": {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "doing", "enteredAt": 1001000, "x": null}}}
        }))
        .unwrap();
        assert_eq!(change.write, WriteKind::Update);
        assert_eq!(change.write.as_str(), "update");
        let after = change.after.as_ref().expect("an after image");
        assert_eq!(after.owned("Workflow", "status"), Some(&json!("doing")));
        assert_eq!(after.owned("Workflow", "x"), None);
        assert_eq!(after.own_field("projectId"), Some(&json!("p1")));
        // An insert has no before, a delete no after, and neither writes one.
        let insert = RowChange {
            write: WriteKind::Insert,
            at: 5,
            before: None,
            after: Some(RowImage::default()),
        };
        assert_eq!(
            serde_json::to_value(&insert).unwrap(),
            json!({"write": "insert", "at": 5, "after": {"own": {}, "owned": {}}})
        );
        let delete: RowChange =
            serde_json::from_value(json!({"write": "delete", "at": 6, "before": {}})).unwrap();
        assert_eq!(delete.before, Some(RowImage::default()));
        assert_eq!(delete.after, None);
    }
}
