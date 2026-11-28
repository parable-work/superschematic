//! Development to runtime with rows (D117): a publish of a development
//! store's schemas to an empty runtime database, its rows replayed after
//! it as creates through each type's rules.
//!
//! A development store is any [`Store`] of the development layout:
//! `superschematic-rules-dev`'s `SqliteStore` over a file, one the
//! TypeScript library wrote among them, or its `PgStore` over a Postgres
//! schema. The move reads it in one transaction it rolls back, so it
//! changes nothing there:
//!
//! - its schemas are each type's live version, each document once, a
//!   document that is itself a type (`{ "name", "fields", "behaviors" }`)
//!   as the General document whose only type it is;
//! - each row carries its own fields and its id, and nothing a behavior
//!   keeps: a Workflow starts in its initial state, a Numbering takes the
//!   next number, and comments, leases and deadlines are not carried
//!   (decision 5);
//! - a row is created after the rows its to-one relations point at, and a
//!   row of a cycle of relations, or that points at a row the store does
//!   not hold, is refused as the create refuses it;
//! - each create runs as the host's runner, the caller its queues run as,
//!   and a refused one is listed with its refusal, so the rest still move.
//!
//! Rows replay into an empty runtime database alone: one no publish has
//! reached, whose first plan starts from an empty database, so a replayed
//! row keeps its id and meets no row of the runtime's own.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde_json::{json, Map, Value};
use superschematic_rules::{Code, Refusal};
use superschematic_rules_handler::document;
use superschematic_rules_handler::{
    Declarations, Store, StoreAnswer, StoreRead, StoredRow, Version,
};

use crate::record::type_document;
use crate::values::internal;

/// What a development store holds: each type's live version and its rows,
/// by type.
pub(crate) struct Development {
    pub versions: Vec<Version>,
    pub rows: BTreeMap<String, Vec<StoredRow>>,
}

/// Reads a development store in a transaction it rolls back.
pub(crate) fn read<S: Store>(dev: &mut S) -> Result<Development, Refusal> {
    dev.begin()?;
    let read = read_in(dev);
    let rolled = dev.rollback();
    let read = read?;
    rolled?;
    Ok(read)
}

fn read_in<S: Store>(dev: &mut S) -> Result<Development, Refusal> {
    let StoreAnswer::Live(versions) = dev.read(&StoreRead::Live)? else {
        return Err(internal("the development store answered no live versions"));
    };
    let mut rows = BTreeMap::new();
    for version in &versions {
        let StoreAnswer::Rows(held) = dev.read(&StoreRead::Rows {
            type_name: version.type_name.clone(),
        })?
        else {
            return Err(internal(format!(
                "the development store answered no rows of {}",
                version.type_name
            )));
        };
        rows.insert(version.type_name.clone(), held);
    }
    Ok(Development { versions, rows })
}

/// The schema documents a development store's types are live at, each
/// once, in its types' order; a document that is itself a type as the
/// General document whose only type it is, as development mode reads it.
pub fn development_documents<S: Store>(dev: &mut S) -> Result<Vec<Value>, Refusal> {
    Ok(documents_of(&read(dev)?.versions))
}

pub(crate) fn documents_of(versions: &[Version]) -> Vec<Value> {
    let mut documents: Vec<Value> = Vec::new();
    for version in versions {
        let document = schema_document(&version.document, &version.type_name);
        if !documents.contains(&document) {
            documents.push(document);
        }
    }
    documents
}

/// A development document as a schema document: one that is itself a
/// type, with no `types`, is the General document of that one type.
fn schema_document(document: &Value, type_name: &str) -> Value {
    if document.get("types").is_some() {
        return document.clone();
    }
    let mut types = Map::new();
    types.insert(type_name.to_owned(), document.clone());
    json!({"kind": "General", "name": type_name, "types": types})
}

/// A row by its type and its id.
type Node = (String, String);

/// One row to replay: its type, its id and the create's own fields, the
/// key among them.
pub(crate) struct Replayed {
    pub type_name: String,
    pub id: String,
    pub data: Map<String, Value>,
}

/// The rows to replay in relation order: each after the rows its to-one
/// relations point at, by the models the published documents read as.
/// Types come in name order and rows in the store's id order otherwise.
pub(crate) fn ordered(
    rows: &BTreeMap<String, Vec<StoredRow>>,
    documents: &[Value],
    declarations: &Declarations,
) -> Vec<Replayed> {
    // Each type's key and the members that hold a to-one relation's key.
    let mut keys: HashMap<&str, String> = HashMap::new();
    let mut links: HashMap<&str, Vec<(String, String)>> = HashMap::new();
    for type_name in rows.keys() {
        let model = type_document(documents, type_name)
            .and_then(|document| document::read(&document, declarations).ok());
        let Some(model) = model else {
            continue;
        };
        keys.insert(type_name, model.key.name.clone());
        links.insert(
            type_name,
            model
                .fields
                .iter()
                .filter_map(|field| {
                    let relation = field.relation.as_ref()?;
                    Some((field.name.clone(), relation.target.clone()))
                })
                .collect(),
        );
    }
    let held: BTreeSet<(&str, &str)> = rows
        .iter()
        .flat_map(|(type_name, held)| {
            held.iter()
                .map(move |row| (type_name.as_str(), row.id.as_str()))
        })
        .collect();
    let by_node: HashMap<(&str, &str), &StoredRow> = rows
        .iter()
        .flat_map(|(type_name, held)| {
            held.iter()
                .map(move |row| ((type_name.as_str(), row.id.as_str()), row))
        })
        .collect();
    // The rows a row's relations point at that the store holds.
    let points_at = |type_name: &str, row: &StoredRow| -> Vec<Node> {
        links
            .get(type_name)
            .into_iter()
            .flatten()
            .filter_map(|(member, target)| {
                let id = row.data.get(member)?.as_str()?;
                held.contains(&(target.as_str(), id))
                    .then(|| (target.clone(), id.to_owned()))
            })
            .collect()
    };

    // A depth-first walk, each row after what it points at; a row met
    // again while its walk is open closes a cycle, and is left where it
    // falls, for its create to refuse what it points at.
    let mut out = Vec::new();
    let mut done: BTreeSet<Node> = BTreeSet::new();
    let mut open: BTreeSet<Node> = BTreeSet::new();
    for (type_name, held_rows) in rows {
        for row in held_rows {
            let start = (type_name.clone(), row.id.clone());
            if done.contains(&start) {
                continue;
            }
            // Each frame is a row and the rows it points at still to walk.
            let mut stack: Vec<(Node, Vec<Node>)> =
                vec![(start.clone(), points_at(type_name, row))];
            open.insert(start);
            while let Some((_, pending)) = stack.last_mut() {
                match pending.pop() {
                    Some(next) => {
                        if done.contains(&next) || open.contains(&next) {
                            continue;
                        }
                        let Some(next_row) = by_node.get(&(next.0.as_str(), next.1.as_str()))
                        else {
                            continue;
                        };
                        let next_points = points_at(&next.0, next_row);
                        open.insert(next.clone());
                        stack.push((next, next_points));
                    }
                    None => {
                        let Some((node, _)) = stack.pop() else {
                            break;
                        };
                        open.remove(&node);
                        done.insert(node.clone());
                        let row = by_node[&(node.0.as_str(), node.1.as_str())];
                        let mut data = row.data.clone();
                        if let Some(key) = keys.get(node.0.as_str()) {
                            data.insert(key.clone(), Value::String(row.id.clone()));
                        }
                        out.push(Replayed {
                            type_name: node.0,
                            id: node.1,
                            data,
                        });
                    }
                }
            }
        }
    }
    out
}

/// A replayed row the runtime refused, as a move lists it.
pub(crate) fn refused(row: &Replayed, refusal: &Refusal) -> Value {
    json!({
        "type": row.type_name,
        "id": row.id,
        "code": refusal.code,
        "message": refusal.message,
    })
}

/// The refusal of a move into a database a publish has reached.
pub(crate) fn not_empty(service: &str, model: &str) -> Refusal {
    Refusal::new(
        Code::Conflict,
        format!(
            "the runtime database holds {service} at model {model}: a development store's rows replay into an empty runtime database alone, at its first publish (D117)"
        ),
    )
}
