//! `compose`: lay one ref's rows over a base tree.

use std::collections::{BTreeMap, BTreeSet, HashMap, VecDeque};

use serde_json::Value;

use crate::descriptor::Graph;
use crate::finding::Finding;
use crate::tree::{self, Row, Tree};

/// An entity's identity across kinds: (kind index, entity key).
pub type Entity<'a> = (usize, &'a str);

/// Compose `overlay` over `base`. A live overlay row replaces the base row with
/// its entity key; a tombstone removes the entity. A removed entity removes its
/// descendants. A row whose parent is absent without having been removed is
/// kept and reported as an `absent_parent` finding.
pub fn compose(graph: &Graph, base: &Tree, overlay: &Tree) -> Value {
    let mut winner: HashMap<Entity, &Row> = HashMap::new();
    let mut tombstoned: BTreeSet<Entity> = BTreeSet::new();
    for layer in [base, overlay] {
        for (kind, rows) in layer.kinds.iter().enumerate() {
            for row in rows {
                let entity = (kind, row.key.as_str());
                if row.deleted {
                    winner.remove(&entity);
                    tombstoned.insert(entity);
                } else {
                    winner.insert(entity, row);
                }
            }
        }
    }

    // Removed entities: those tombstoned and not brought back, then every
    // descendant of one, through the winning rows' parent edges.
    let mut children: HashMap<Entity, Vec<Entity>> = HashMap::new();
    for (&entity, row) in &winner {
        if let Some(parent) = parent_of(graph, entity.0, row) {
            children.entry(parent).or_default().push(entity);
        }
    }
    let mut queue: VecDeque<Entity> = tombstoned
        .into_iter()
        .filter(|entity| !winner.contains_key(entity))
        .collect();
    while let Some(gone) = queue.pop_front() {
        for child in children.remove(&gone).unwrap_or_default() {
            if winner.remove(&child).is_some() {
                queue.push_back(child);
            }
        }
    }

    let mut findings = Vec::new();
    let mut kinds: Vec<Vec<&Row>> = vec![Vec::new(); graph.kinds.len()];
    let ordered: BTreeMap<Entity, &Row> = winner.iter().map(|(e, r)| (*e, *r)).collect();
    for ((kind, key), row) in ordered {
        if let Some(parent) = parent_of(graph, kind, row) {
            if !winner.contains_key(&parent) {
                findings.push(Finding::absent_parent(graph, kind, key, parent));
            }
        }
        kinds[kind].push(row);
    }
    serde_json::json!({
        "tree": tree::render(graph, kinds),
        "findings": findings,
    })
}

/// The entity a row's parent key names, when its kind has a parent edge and
/// the row sets it.
pub fn parent_of<'a>(graph: &Graph, kind: usize, row: &'a Row) -> Option<Entity<'a>> {
    let edge = graph.kinds[kind].parent.as_ref()?;
    row.parent.as_deref().map(|key| (edge.kind, key))
}
