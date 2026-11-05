//! A behavior's declaration as the core reads it (D103), from the file the
//! compiler registers and the engine embeds
//! (`runtime/engine/rust/behaviors/declarations`): its name, the fields it
//! owns and the one that holds a field anchor's value (`value`), the ones it
//! computes at a read (a field with no `type`, which no column holds, or a
//! `volatile` one), its sub-tables (`tables`: each one's name, key and
//! fields), whether it arms deadlines (`armsDeadlines`) and whether it hears
//! its row's writes (`hearsChanges`), its operations and whether each
//! writes, its veto codes, whether a create gives it parameters (a
//! createParamsSchema), and whether a caller presents it a precondition (a
//! preconditionSchema). An operation of a table or a namespace as a whole
//! (`scope` `schema` or `namespace`, Lease's expireHolder) is left out: a
//! row's core runs the operations of a row (D104). The file stays the one declaration; the core reads
//! only what it holds a behavior to, and the rest (descriptions, schemas,
//! each field's column type and whether a value field is `optional`, which
//! a table's layout reads) is the file's.

use serde_json::Value;
use superschematic_rules::{Declaration, TableDecl};

/// The core's declaration of a declaration file's text. A member the file
/// lacks reads as none, so a file that does not parse declares a behavior
/// with no name, which a test of each behavior refuses.
pub(crate) fn declaration(text: &str) -> Declaration {
    let file: Value = serde_json::from_str(text).unwrap_or(Value::Null);
    let mut declaration =
        Declaration::new(file.get("name").and_then(Value::as_str).unwrap_or_default());
    for field in listed(&file, "fields") {
        if let Some(name) = field.get("name").and_then(Value::as_str) {
            declaration = declaration.field(name);
            // A field no column holds, or one that moves at a read, is one
            // the behavior computes, which a write of the row does not carry.
            let volatile = field.get("volatile") == Some(&Value::Bool(true));
            if field.get("type").is_none() || volatile {
                declaration = declaration.computed(name);
            }
        }
    }
    if let Some(value) = file.get("value").and_then(Value::as_str) {
        declaration = declaration.value(value);
    }
    for table in listed(&file, "tables") {
        if let Some(name) = table.get("name").and_then(Value::as_str) {
            declaration = declaration.sub_table(TableDecl {
                name: name.to_owned(),
                key: table.get("key").and_then(Value::as_str).map(str::to_owned),
                fields: listed(table, "fields")
                    .filter_map(|field| field.get("name").and_then(Value::as_str))
                    .map(str::to_owned)
                    .collect(),
            });
        }
    }
    // A behavior arms deadlines only when its file says so.
    if file.get("armsDeadlines") == Some(&Value::Bool(true)) {
        declaration = declaration.arms_deadlines();
    }
    // And hears its row's writes only when its file says so.
    if file.get("hearsChanges") == Some(&Value::Bool(true)) {
        declaration = declaration.hears_changes();
    }
    for operation in listed(&file, "operations") {
        // A row's core runs the operations of a row; one of a table or a
        // namespace waits for a command without a row (D104).
        let scope = operation.get("scope").and_then(Value::as_str);
        if matches!(scope, Some("schema" | "namespace")) {
            continue;
        }
        if let Some(name) = operation.get("name").and_then(Value::as_str) {
            // An operation writes only when it says so, as the engine reads
            // the file.
            let writes = operation.get("writes") == Some(&Value::Bool(true));
            declaration = declaration.operation(name, writes);
        }
    }
    for veto in listed(&file, "vetoes") {
        if let Some(code) = veto.get("code").and_then(Value::as_str) {
            declaration = declaration.veto(code);
        }
    }
    // A create gives the behavior parameters when its file has a
    // createParamsSchema, as the engine reads it.
    if file
        .get("createParamsSchema")
        .is_some_and(|schema| !schema.is_null())
    {
        declaration = declaration.create_params();
    }
    // A caller presents it a precondition when its file has a
    // preconditionSchema (D16, D104).
    if file
        .get("preconditionSchema")
        .is_some_and(|schema| !schema.is_null())
    {
        declaration = declaration.preconditions();
    }
    declaration
}

// listed is a member of the file that is a list, or none.
fn listed<'f>(file: &'f Value, key: &str) -> impl Iterator<Item = &'f Value> {
    file.get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_file_declares_its_name_fields_operations_and_vetoes() {
        let declared = declaration(
            r#"{"name": "Tally", "description": "x",
                "fields": [{"name": "count", "type": "integer"}, {"description": "no name"}],
                "operations": [{"name": "add", "writes": true}, {"name": "peek"}, {"name": "odd", "writes": "yes"}],
                "vetoes": [{"code": "over_max"}],
                "createParamsSchema": {"type": "object"}}"#,
        );
        assert_eq!(
            declared,
            Declaration::new("Tally")
                .field("count")
                .operation("add", true)
                .operation("peek", false)
                .operation("odd", false)
                .veto("over_max")
                .create_params()
        );
        assert!(!declaration(r#"{"name": "Tally", "createParamsSchema": null}"#).create_params);
        // A file that names its value field declares it.
        let valued = declaration(
            r#"{"name": "Tally", "fields": [{"name": "count", "type": "integer"}], "value": "count"}"#,
        );
        assert_eq!(valued, Declaration::new("Tally").value("count"));
        assert_eq!(valued.fields, ["count"]);
        // A value field the behavior does not set at a create says it is
        // optional (D104), which a table's layout reads and the core does
        // not: it is a field like any other.
        let optional = declaration(
            r#"{"name": "Tally", "fields": [{"name": "count", "type": "string", "optional": true}], "value": "count"}"#,
        );
        assert_eq!(optional, valued);
        // A file that declares sub-tables and arms deadlines says so.
        let timed = declaration(
            r#"{"name": "Tally",
                "tables": [{"name": "entries", "key": "name", "fields": [{"name": "name", "type": "string"}, {"name": "at"}]}, {"description": "no name"}],
                "armsDeadlines": true}"#,
        );
        assert_eq!(
            timed,
            Declaration::new("Tally")
                .sub_table(TableDecl::new("entries", "name", ["name", "at"]))
                .arms_deadlines()
        );
        assert!(!declaration(r#"{"name": "Tally", "armsDeadlines": "yes"}"#).arms_deadlines);
        // A field with no type, or a volatile one, is computed; a file that
        // hears its row's writes says so.
        let heard = declaration(
            r#"{"name": "Tally",
                "fields": [{"name": "total"}, {"name": "age", "type": "integer", "volatile": true}, {"name": "count", "type": "integer"}],
                "hearsChanges": true}"#,
        );
        assert_eq!(
            heard,
            Declaration::new("Tally")
                .computed("total")
                .computed("age")
                .field("count")
                .hears_changes()
        );
        assert!(!declaration(r#"{"name": "Tally", "hearsChanges": 1}"#).hears_changes);
    }

    #[test]
    fn a_file_leaves_out_row_less_operations_and_says_it_takes_preconditions() {
        let fenced = declaration(
            r#"{"name": "Tally",
                "operations": [{"name": "add", "writes": true}, {"name": "sweep", "writes": true, "scope": "schema"},
                               {"name": "purge", "writes": true, "scope": "namespace"}, {"name": "peek", "scope": "instance"}],
                "preconditionSchema": {"type": "object"}}"#,
        );
        assert_eq!(
            fenced,
            Declaration::new("Tally")
                .operation("add", true)
                .operation("peek", false)
                .preconditions()
        );
        assert!(!declaration(r#"{"name": "Tally", "preconditionSchema": null}"#).preconditions);
    }

    #[test]
    fn a_file_that_does_not_parse_declares_nothing() {
        assert_eq!(declaration("{"), Declaration::new(""));
    }
}
