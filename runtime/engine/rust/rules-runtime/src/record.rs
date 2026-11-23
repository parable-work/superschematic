//! What a runtime database records beside the runner's state (D117): the
//! schema documents its last publish took it to, the layout document the
//! compiler wrote for them beside the plan, each type's version, and the
//! hash of the model the plan ended at. The handler serves the types from
//! it, read once per model: a request reads the model the database is at,
//! and the record again when that hash has moved.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{Caller, Code, Refusal};
use superschematic_rules_adapter::{Column, Layout, Table};
use superschematic_rules_handler::document::{self, Field, Model};
use superschematic_rules_handler::{Context, Declarations, Handler, HandlerStep, Request};

/// The table a runtime database keeps its record in, one row per DB
/// service, beside the runner's `superschematic_schema_state` and
/// `superschematic_migrations`.
pub const RECORD_TABLE: &str = "superschematic_runtime";

/// A runtime database's record of its last publish.
#[derive(Clone, Debug)]
pub struct Record {
    /// The DB service the documents are of, which keys the runner's state.
    pub service: String,
    /// The hash of the model the publish's plan took the database to.
    pub model_hash: String,
    /// The schema documents published, each as `format --to=json` writes
    /// it.
    pub documents: Vec<Value>,
    /// The layout document the compiler wrote for them
    /// (`behaviorlayout.RuntimeOf`).
    pub layout: Layout,
    /// Each type's version: 1 at its first publish, one more at each
    /// publish that changes its document.
    pub versions: BTreeMap<String, i64>,
}

/// One type as the store serves it: the handler's model of its document,
/// its table's layout, and the column of each own field the model holds.
#[derive(Clone, Debug)]
pub(crate) struct Served {
    pub model: Model,
    pub table: Table,
    /// Each own field of the model with its column, in the model's order.
    pub own: Vec<(Field, Column)>,
    /// The type's document as the handler reads it.
    pub document: Value,
    pub version: i64,
}

/// The types of a record, by name, as the store serves them, and their
/// models, which a list's filter compiles against.
#[derive(Clone, Debug, Default)]
pub(crate) struct Types {
    pub model_hash: String,
    pub types: BTreeMap<String, Served>,
    pub models: BTreeMap<String, Model>,
}

impl Types {
    pub fn get(&self, type_name: &str) -> Result<&Served, Refusal> {
        self.types.get(type_name).ok_or_else(|| {
            Refusal::not_found(format!(
                "type {type_name} is no table of the published schemas"
            ))
        })
    }
}

/// The document the handler reads for a type of the published documents:
/// the documents' scalars, enums and types merged, as one service's are,
/// and named after the type, so the handler's reader takes it as the
/// document's instance type.
pub fn type_document(documents: &[Value], type_name: &str) -> Option<Value> {
    let mut merged = Map::new();
    let mut found = false;
    for document in documents {
        let Some(object) = document.as_object() else {
            continue;
        };
        for (member, value) in object {
            match (member.as_str(), value) {
                ("scalars" | "enums" | "types", Value::Object(entries)) => {
                    let slot = merged
                        .entry(member.clone())
                        .or_insert_with(|| Value::Object(Map::new()));
                    if let Value::Object(slot) = slot {
                        for (name, entry) in entries {
                            slot.insert(name.clone(), entry.clone());
                        }
                    }
                    if member == "types" && entries.contains_key(type_name) {
                        found = true;
                    }
                }
                ("kind" | "description", _) => {
                    merged
                        .entry(member.clone())
                        .or_insert_with(|| value.clone());
                }
                _ => {}
            }
        }
    }
    if !found {
        return None;
    }
    merged.insert("name".into(), Value::String(type_name.to_owned()));
    Some(Value::Object(merged))
}

fn invalid(type_name: &str, text: impl std::fmt::Display) -> Refusal {
    Refusal {
        class: "RuntimeLayoutError".to_owned(),
        ..Refusal::new(Code::InvalidSchema, format!("type {type_name}: {text}"))
    }
}

/// The types a record serves: each table of its layout read through the
/// handler's reader from its document, checked by the core's define check,
/// and each own field of the model matched to its column. A field the
/// layout has no column of, a key that is not the layout's, or a use the
/// layout does not lay out refuses the record: the handler could not serve
/// the type over the tables the plan wrote.
pub(crate) fn serve(
    record: &Record,
    core: &superschematic_rules::Core,
    declarations: &Declarations,
) -> Result<Types, Refusal> {
    let handler = Handler::new(core, declarations);
    let context = Context {
        caller: Caller::new("runtime-publish", Vec::<String>::new()),
        now: 0,
        request: String::new(),
        id: 0,
    };
    let mut types = BTreeMap::new();
    for (type_name, table) in &record.layout.types {
        let document = type_document(&record.documents, type_name).ok_or_else(|| {
            invalid(
                type_name,
                "the layout names a table no published document defines",
            )
        })?;
        // The core's define check of its uses and configs, and the
        // handler's reading of its document.
        if let HandlerStep::Done(done) = handler.run(
            &Request::Define {
                document: document.clone(),
            },
            &context,
            &Default::default(),
        ) {
            done.response?;
        }
        let mut model = document::read(&document, declarations)?;
        resolve(&mut model, &record.documents, declarations);
        if model.key.name != table.key.field {
            return Err(invalid(
                type_name,
                format!(
                    "its key is {}, and the table's is {}",
                    model.key.name, table.key.field
                ),
            ));
        }
        let mut own = Vec::with_capacity(model.fields.len());
        for field in &model.fields {
            let column = table
                .own
                .iter()
                .find(|column| column.field == field.name)
                .ok_or_else(|| {
                    invalid(
                        type_name,
                        format!(
                            "the field {} has no column of {} in the layout",
                            field.name, table.table
                        ),
                    )
                })?;
            own.push((field.clone(), column.clone()));
        }
        for used in &model.uses {
            if !table.uses.iter().any(|laid| laid.id == used.id()) {
                return Err(invalid(
                    type_name,
                    format!(
                        "its use {} ({}) is not in the layout of {}",
                        used.id(),
                        used.spec.behavior,
                        table.table
                    ),
                ));
            }
        }
        types.insert(
            type_name.clone(),
            Served {
                model,
                table: table.clone(),
                own,
                document,
                version: record.versions.get(type_name).copied().unwrap_or(1),
            },
        );
    }
    let models = types
        .iter()
        .map(|(name, served)| (name.clone(), served.model.clone()))
        .collect();
    Ok(Types {
        model_hash: record.model_hash.clone(),
        types,
        models,
    })
}

/// Gives each relation's member its target's key type, as the handler's
/// live models have it: the member's column holds the target's key.
fn resolve(model: &mut Model, documents: &[Value], declarations: &Declarations) {
    for field in &mut model.fields {
        let Some(relation) = &field.relation else {
            continue;
        };
        let target = type_document(documents, &relation.target)
            .and_then(|document| document::read(&document, declarations).ok());
        if let Some(target) = target {
            field.type_name.clone_from(&target.key.type_name);
            field.kind = target.key.kind.clone();
        }
    }
}

/// What a type's version follows: its own definition and the scalars and
/// enums of the documents, which its fields may name, and not the other
/// tables'.
fn definition(documents: &[Value], type_name: &str) -> Option<Value> {
    let document = type_document(documents, type_name)?;
    Some(serde_json::json!([
        document.get("types").and_then(|types| types.get(type_name)),
        document.get("scalars"),
        document.get("enums"),
    ]))
}

/// Each type's version after a publish of `documents` with `layout`: a
/// type's version stays while its definition does, moves on by one when it
/// changes, and starts at 1; a type the layout no longer holds has none.
pub(crate) fn versions_after(
    previous: Option<&Record>,
    documents: &[Value],
    layout: &Layout,
) -> BTreeMap<String, i64> {
    let mut out = BTreeMap::new();
    for type_name in layout.types.keys() {
        let now = definition(documents, type_name);
        let version = match previous {
            Some(previous) => match previous.versions.get(type_name) {
                Some(version) if definition(&previous.documents, type_name) == now => *version,
                Some(version) => version + 1,
                None => 1,
            },
            None => 1,
        };
        out.insert(type_name.clone(), version);
    }
    out
}

/// The DB service the documents are of: each one's `name`, the same in
/// every one, as a plan from documents requires.
pub(crate) fn service_of(documents: &[Value]) -> Result<String, Refusal> {
    let mut service: Option<&str> = None;
    for document in documents {
        let name = document
            .get("name")
            .and_then(Value::as_str)
            .filter(|name| !name.is_empty())
            .ok_or_else(|| {
                Refusal::invalid_argument(
                    "a published document names no service (name), as format --to=json writes it",
                )
            })?;
        match service {
            Some(known) if known != name => {
                return Err(Refusal::invalid_argument(format!(
                    "the documents are of {known} and of {name}: a publish takes one DB service's documents"
                )))
            }
            _ => service = Some(name),
        }
    }
    service
        .map(str::to_owned)
        .ok_or_else(|| Refusal::invalid_argument("a publish names no schema document"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_type_document_merges_the_services_documents() {
        let documents = [
            json!({"name": "shop-db", "kind": "DB", "enums": {"Size": {"name": "Size"}},
                   "types": {"Order": {"name": "Order", "fields": []}}}),
            json!({"name": "shop-db", "kind": "DB", "types": {"Customer": {"name": "Customer", "fields": []}}}),
        ];
        let order = type_document(&documents, "Order").unwrap();
        assert_eq!(order["name"], "Order");
        assert_eq!(order["kind"], "DB");
        assert!(order["enums"].get("Size").is_some());
        assert!(order["types"].get("Customer").is_some());
        assert!(type_document(&documents, "Invoice").is_none());
    }
}
