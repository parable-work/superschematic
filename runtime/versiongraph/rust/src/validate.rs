//! `validate`: list what is wrong with a tree.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde_json::Value;

use crate::compose::{parent_of, Entity};
use crate::descriptor::Graph;
use crate::finding::Finding;
use crate::tree::{self, Row, Tree};

/// Check a tree per kind: one row per entity key, at most one live row of a
/// singleton kind, every live row's parent live in the tree, no parent cycles,
/// and every order inside the range a JavaScript number holds exactly.
/// Tombstone rows count for the key rule only.
pub fn validate(graph: &Graph, tree: &Tree) -> Value {
    let mut findings: Vec<(usize, Option<String>, Finding)> = Vec::new();
    let mut push = |kind: usize, finding: Finding| {
        findings.push((kind, finding.entity_key.clone(), finding));
    };

    let mut live: BTreeMap<Entity, &Row> = BTreeMap::new();
    for (index, (kind, rows)) in graph.kinds.iter().zip(&tree.kinds).enumerate() {
        let mut seen = BTreeSet::new();
        let mut reported = BTreeSet::new();
        let mut live_rows = 0usize;
        for row in rows {
            if !seen.insert(row.key.as_str()) && reported.insert(row.key.as_str()) {
                push(
                    index,
                    Finding::new(
                        "duplicate_entity_key",
                        graph,
                        index,
                        Some(&row.key),
                        "more than one row has this entity key".to_owned(),
                    ),
                );
            }
            if !row.order_in_range {
                push(
                    index,
                    Finding::new(
                        "order_out_of_range",
                        graph,
                        index,
                        Some(&row.key),
                        tree::order_message("tree", kind, row),
                    ),
                );
            }
            if !row.deleted {
                live_rows += 1;
                live.entry((index, row.key.as_str())).or_insert(row);
            }
        }
        if kind.singleton && live_rows > 1 {
            push(
                index,
                Finding::new(
                    "singleton",
                    graph,
                    index,
                    None,
                    format!("{live_rows} live rows; a singleton kind has at most one"),
                ),
            );
        }
    }

    for (&(kind, key), row) in &live {
        if let Some(parent) = parent_of(graph, kind, row) {
            if !live.contains_key(&parent) {
                push(kind, Finding::absent_parent(graph, kind, key, parent));
            }
        }
    }

    for (kind, key) in cycles(graph, &live) {
        push(
            kind,
            Finding::new(
                "parent_cycle",
                graph,
                kind,
                Some(key),
                "the entity is its own ancestor".to_owned(),
            ),
        );
    }

    findings.sort_by(|a, b| (a.0, &a.1, a.2.code).cmp(&(b.0, &b.1, b.2.code)));
    let findings: Vec<Finding> = findings.into_iter().map(|(_, _, f)| f).collect();
    serde_json::json!({ "findings": findings })
}

/// Every live entity that lies on a parent cycle.
fn cycles<'a>(graph: &Graph, live: &BTreeMap<Entity<'a>, &'a Row>) -> BTreeSet<Entity<'a>> {
    #[derive(PartialEq)]
    enum Mark {
        OnPath,
        Done,
    }
    let mut marks: HashMap<Entity, Mark> = HashMap::new();
    let mut cyclic = BTreeSet::new();
    for &start in live.keys() {
        let mut path: Vec<Entity> = Vec::new();
        let mut current = start;
        loop {
            match marks.get(&current) {
                Some(Mark::OnPath) => {
                    let from = path.iter().position(|e| *e == current).unwrap_or(0);
                    cyclic.extend(path[from..].iter().copied());
                    break;
                }
                Some(Mark::Done) => break,
                None => {}
            }
            marks.insert(current, Mark::OnPath);
            path.push(current);
            match live
                .get(&current)
                .and_then(|row| parent_of(graph, current.0, row))
            {
                Some(parent) if live.contains_key(&parent) => current = parent,
                _ => break,
            }
        }
        for entity in path {
            marks.insert(entity, Mark::Done);
        }
    }
    cyclic
}
