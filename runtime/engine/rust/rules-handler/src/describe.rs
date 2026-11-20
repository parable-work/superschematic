//! A type's live version as development mode runs it: its key, its own
//! fields with what a filter and a sort take of each, the owned fields its
//! uses add, each use with its behavior's operations, sub-tables and
//! events, its unique keys, and the document it was published from.

use serde_json::{json, Value};

use crate::declarations::Declarations;
use crate::document::{Kind, Model, OnDelete};

fn ops(kind: &Kind, list: bool) -> Value {
    if list {
        return json!([]);
    }
    json!(kind.filter_ops())
}

/// The describe document of a type's live version.
pub fn describe(model: &Model, version: i64, declarations: &Declarations) -> Value {
    let fields: Vec<Value> = model
        .fields
        .iter()
        .map(|field| {
            let mut entry = json!({
                "name": field.name,
                "type": field.type_name,
                "list": field.list,
                "required": field.required,
                "unique": field.unique,
                "filter": ops(&field.kind, field.list),
                "sorts": !field.list && field.kind.sorts(),
            });
            if let Some(relation) = &field.relation {
                entry["relation"] = json!({
                    "name": relation.name,
                    "type": relation.target,
                    "onDelete": match relation.on_delete {
                        OnDelete::Cascade => "CASCADE",
                        OnDelete::SetNull => "SET NULL",
                        OnDelete::Restrict => "RESTRICT",
                    },
                });
            }
            if !field.description.is_empty() {
                entry["description"] = Value::String(field.description.clone());
            }
            entry
        })
        .collect();
    let owned: Vec<Value> = model
        .owned
        .iter()
        .map(|owned| {
            json!({
                "name": owned.field,
                "use": owned.use_id,
                "field": owned.name,
                "type": owned.type_name,
                "value": owned.value,
                "optional": owned.optional,
                "filter": ops(&owned.kind(), false),
                "sorts": owned.kind().sorts(),
            })
        })
        .collect();
    let uses: Vec<Value> = model
        .uses
        .iter()
        .map(|used| {
            let declared = declarations.get(&used.spec.behavior);
            let operations: Vec<Value> = declared
                .map(|declared| {
                    declared
                        .operations
                        .iter()
                        .map(|operation| {
                            json!({
                                "name": operation.name,
                                "writes": operation.writes,
                                "table": operation.table,
                                "description": operation.description,
                                "params": operation.params,
                            })
                        })
                        .collect()
                })
                .unwrap_or_default();
            let tables: Vec<Value> = declared
                .map(|declared| {
                    declared
                        .tables
                        .iter()
                        .map(|table| {
                            json!({
                                "name": table.name,
                                "key": table.key,
                                "fields": table.fields.iter().map(|field| json!({"name": field.name, "type": field.type_name})).collect::<Vec<_>>(),
                            })
                        })
                        .collect()
                })
                .unwrap_or_default();
            let anchor = match used.spec.field() {
                Some(field) => json!({"field": field}),
                None => json!("type"),
            };
            json!({
                "id": used.id(),
                "behavior": used.spec.behavior,
                "anchor": anchor,
                "config": used.spec.config_or_empty(),
                "operations": operations,
                "tables": tables,
                "events": declared.map(|declared| declared.events.clone()).unwrap_or_default(),
                "armsDeadlines": declared.is_some_and(|declared| declared.arms_deadlines),
                "hearsChanges": declared.is_some_and(|declared| declared.hears_changes),
            })
        })
        .collect();
    json!({
        "type": model.name,
        "version": version,
        "mode": "development",
        "key": {
            "name": model.key.name,
            "type": model.key.type_name,
            "auto": model.key.auto,
        },
        "fields": fields,
        "owned": owned,
        "uses": uses,
        "uniques": model.uniques,
        "document": model.document,
    })
}
