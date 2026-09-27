//! `diff`: the changes that turn one tree into another.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};

use crate::descriptor::Graph;
use crate::tree::{Row, Tree};

/// Rows of one kind by entity key.
pub fn by_key(rows: &[Row]) -> BTreeMap<&str, &Row> {
    rows.iter().map(|row| (row.key.as_str(), row)).collect()
}

/// A row that is present and not a tombstone.
pub fn live<'a>(rows: &BTreeMap<&str, &'a Row>, key: &str) -> Option<&'a Row> {
    rows.get(key).copied().filter(|row| !row.deleted)
}

/// Diff `from` against `to`, per entity. An entity live only in `to` is an
/// `ADD`, one live in both with different content an `UPDATE`, one live only
/// in `from` a `DELETE`. A tombstone and an absent row are the same deleted
/// state. `ADD` and `UPDATE` carry `to`'s row; `DELETE` carries `to`'s
/// tombstone row when it has one. Changes are ordered by kind, in descriptor
/// order, then entity key.
pub fn diff(graph: &Graph, from: &Tree, to: &Tree) -> Value {
    let mut changes = Vec::new();
    for (kind, (from_rows, to_rows)) in graph.kinds.iter().zip(from.kinds.iter().zip(&to.kinds)) {
        let (from_by, to_by) = (by_key(from_rows), by_key(to_rows));
        let keys: BTreeSet<&str> = from_by.keys().chain(to_by.keys()).copied().collect();
        for key in keys {
            let (operation, row) = match (live(&from_by, key), live(&to_by, key)) {
                (None, Some(row)) => ("ADD", Some(row)),
                (Some(a), Some(b)) if a.content(kind) != b.content(kind) => ("UPDATE", Some(b)),
                (Some(_), None) => ("DELETE", to_by.get(key).copied()),
                _ => continue,
            };
            let mut change = Map::new();
            change.insert("kind".to_owned(), Value::String(kind.name.clone()));
            change.insert("entityKey".to_owned(), Value::String(key.to_owned()));
            change.insert("operation".to_owned(), Value::String(operation.to_owned()));
            if let Some(row) = row {
                change.insert("row".to_owned(), Value::Object(row.value.clone()));
            }
            changes.push(Value::Object(change));
        }
    }
    serde_json::json!({ "changes": changes })
}
