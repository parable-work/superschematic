//! `content_hash`: a digest of a tree's content that ignores bookkeeping.

use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::descriptor::Graph;
use crate::tree::Tree;

/// The canonical document the hash covers: for each kind with live rows,
/// `{"<kind>": [{"entityKey": ..., "content": {...}}, ...]}` with rows sorted
/// by entity key and only content columns kept, every one the descriptor
/// declares among them, null where the row lacks it. Tombstone rows are left
/// out, as an absent row is. [`content_hash`] writes it with every object's
/// keys sorted and no whitespace.
pub fn canonical(graph: &Graph, tree: &Tree) -> Value {
    let mut out = Map::new();
    for (kind, rows) in graph.kinds.iter().zip(&tree.kinds) {
        let mut live: Vec<_> = rows.iter().filter(|row| !row.deleted).collect();
        if live.is_empty() {
            continue;
        }
        live.sort_by(|a, b| a.key.cmp(&b.key));
        let entries = live
            .into_iter()
            .map(|row| {
                let content: Map<String, Value> = row.content(kind).into_iter().collect();
                serde_json::json!({ "entityKey": row.key, "content": content })
            })
            .collect();
        out.insert(kind.name.clone(), Value::Array(entries));
    }
    Value::Object(out)
}

/// SHA-256, as lowercase hex, of the canonical document's JSON bytes.
pub fn content_hash(graph: &Graph, tree: &Tree) -> Value {
    let mut text = String::new();
    write_sorted(&mut text, &canonical(graph, tree));
    let digest = Sha256::digest(text);
    let hex: String = digest.iter().map(|byte| format!("{byte:02x}")).collect();
    serde_json::json!({ "contentHash": hex })
}

/// Writes a JSON value with no whitespace and every object's keys sorted, at
/// every depth, so the bytes hashed do not depend on the order `serde_json`'s
/// map keeps: with its `preserve_order` feature on, which another crate in a
/// build can turn on, a map keeps insertion order instead of sorting. Keys
/// sort by code point (their UTF-8 bytes), as a `BTreeMap<String, _>` sorts
/// them.
fn write_sorted(out: &mut String, value: &Value) {
    match value {
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            out.push('{');
            for (i, key) in keys.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&Value::String(key.clone()).to_string());
                out.push(':');
                write_sorted(out, &members[key.as_str()]);
            }
            out.push('}');
        }
        Value::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_sorted(out, item);
            }
            out.push(']');
        }
        scalar => out.push_str(&scalar.to_string()),
    }
}
