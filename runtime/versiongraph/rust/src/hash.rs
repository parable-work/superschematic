//! `content_hash`: a digest of a tree's content that ignores bookkeeping.

use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::descriptor::Graph;
use crate::tree::Tree;

/// The canonical document the hash covers: for each kind with live rows,
/// `{"<kind>": [{"entityKey": ..., "content": {...}}, ...]}` with rows sorted
/// by entity key and only content columns kept. Tombstone rows are left out,
/// as an absent row is. Objects serialize with sorted keys and no whitespace.
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
    let digest = Sha256::digest(canonical(graph, tree).to_string());
    let hex: String = digest.iter().map(|byte| format!("{byte:02x}")).collect();
    serde_json::json!({ "contentHash": hex })
}
