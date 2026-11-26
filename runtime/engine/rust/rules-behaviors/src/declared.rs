//! A behavior's declaration as the core reads it (D103), from the file the
//! compiler registers and the engine embeds
//! (`runtime/engine/rust/behaviors/declarations`): its name, the fields it
//! owns and the one that holds a field anchor's value (`value`), the ones it
//! computes at a read (a field with no `type`, which no column holds, or a
//! `volatile` one), its sub-tables (`tables`: each one's name, key and
//! fields), whether it arms deadlines (`armsDeadlines`) and whether it hears
//! its row's writes (`hearsChanges`), its operations and whether each
//! writes, the kinds of the events it emits about a row (`events`, D104),
//! its veto codes, whether a create gives it parameters (a
//! createParamsSchema), whether a caller presents it a precondition (a
//! preconditionSchema), and the contracts it implements and asks
//! (`contracts`, each `{ name, role }`, D104). An operation whose `scope` is
//! `schema` is one of the table as a whole (Lease's expireHolder, Pool's
//! pick), which a command without a row runs (D104); one whose `scope` is
//! `namespace` (Comments' inbox) is one of the namespace as a whole, which a
//! command on no type runs (D77's primitive 3); and its signals, each with
//! its access (D77's primitive 2). The file stays the one declaration; the
//! core reads
//! only what it holds a behavior to, and the rest (descriptions, schemas,
//! an event's payloadSchema among them,
//! each field's column type and whether a value field is `optional`, which
//! a table's layout reads) is the file's.

use serde_json::Value;
use superschematic_rules::{Declaration, SignalAccess, TableDecl};

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
        // A value field a create gives is the use's value at a create
        // (D147's first primitive: Aliased's name).
        let given = listed(&file, "fields").any(|field| {
            field.get("name").and_then(Value::as_str) == Some(value)
                && field.get("fromCreate") == Some(&Value::Bool(true))
        });
        if given {
            declaration = declaration.value_from_create();
        }
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
                // A table whose scope is schema is kept for the table as a
                // whole, its rows shared by every row of the type (D103's
                // step 4: Numbering's counts).
                shared: table.get("scope").and_then(Value::as_str) == Some("schema"),
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
        // An operation of the table as a whole runs as a command without a
        // row (D104), and one of the namespace as a whole as a command on
        // no type (D77's primitive 3).
        let scope = operation.get("scope").and_then(Value::as_str);
        if let Some(name) = operation.get("name").and_then(Value::as_str) {
            // An operation writes only when it says so, as the engine reads
            // the file.
            let writes = operation.get("writes") == Some(&Value::Bool(true));
            // A writing operation of a row that says beside writes only its
            // use's own tables (D77's primitive 1, Comments').
            let beside = operation.get("beside") == Some(&Value::Bool(true));
            declaration = if scope == Some("namespace") {
                declaration.namespace_operation(name, writes)
            } else if scope == Some("schema") {
                declaration.table_operation(name, writes)
            } else if writes && beside {
                declaration.beside_operation(name)
            } else {
                declaration.operation(name, writes)
            };
        }
    }
    // The signals it declares, each with who sends it (D77's primitive 2);
    // one of another access is passed over, which the registry refuses.
    for signal in listed(&file, "signals") {
        let name = signal.get("name").and_then(Value::as_str);
        let access = signal
            .get("access")
            .and_then(Value::as_str)
            .and_then(SignalAccess::parse);
        if let (Some(name), Some(access)) = (name, access) {
            declaration = declaration.signal(name, access);
        }
    }
    // The kinds of the events it emits, which an emit is held to (D104).
    for event in listed(&file, "events") {
        if let Some(kind) = event.get("kind").and_then(Value::as_str) {
            declaration = declaration.event(kind);
        }
    }
    // The contracts it implements and asks (D104); an entry of another
    // role is passed over, which the registry refuses.
    for contract in listed(&file, "contracts") {
        let Some(name) = contract.get("name").and_then(Value::as_str) else {
            continue;
        };
        match contract.get("role").and_then(Value::as_str) {
            Some("implements") => declaration = declaration.contract(name),
            Some("asks") => declaration = declaration.asking(name),
            _ => {}
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
        // A table whose scope is schema is shared by the table's rows.
        let shared = declaration(
            r#"{"name": "Tally",
                "tables": [{"name": "counts", "key": "scope", "scope": "schema", "fields": [{"name": "scope", "type": "string"}, {"name": "last", "type": "Generic.Int64"}]},
                           {"name": "notes", "key": "at", "scope": "instance", "fields": [{"name": "at", "type": "string"}]}]}"#,
        );
        assert_eq!(
            shared,
            Declaration::new("Tally")
                .sub_table(TableDecl::new("counts", "scope", ["scope", "last"]).shared())
                .sub_table(TableDecl::new("notes", "at", ["at"]))
        );
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
    fn a_file_declares_its_signals_with_their_access() {
        let declared = declaration(
            r#"{"name": "Tally",
                "signals": [{"name": "typing", "access": "read"}, {"name": "draft", "access": "operation"},
                            {"name": "odd", "access": "maybe"}, {"access": "write"}]}"#,
        );
        assert_eq!(
            declared,
            Declaration::new("Tally")
                .signal("typing", SignalAccess::Read)
                .signal("draft", SignalAccess::Operation)
        );
    }

    #[test]
    fn a_file_declares_table_operations_and_namespace_ones() {
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
                .table_operation("sweep", true)
                .namespace_operation("purge", true)
                .operation("peek", false)
                .preconditions()
        );
        assert!(fenced.find_operation("sweep").is_none());
        assert!(fenced.find_table_operation("sweep").is_some());
        assert!(
            fenced.find_operation("purge").is_none()
                && fenced.find_table_operation("purge").is_none()
                && fenced
                    .find_namespace_operation("purge")
                    .is_some_and(|op| op.writes)
        );
        assert!(!declaration(r#"{"name": "Tally", "preconditionSchema": null}"#).preconditions);
        // A writing operation beside its row says so; beside on one that
        // writes nothing declares a plain read.
        let beside = declaration(
            r#"{"name": "Tally",
                "operations": [{"name": "note", "writes": true, "beside": true}, {"name": "look", "beside": true}]}"#,
        );
        assert_eq!(
            beside,
            Declaration::new("Tally")
                .beside_operation("note")
                .operation("look", false)
        );
        assert!(beside.find_operation("note").is_some_and(|op| op.beside));
    }

    #[test]
    fn a_file_declares_the_contracts_it_implements_and_asks() {
        let declared = declaration(
            r#"{"name": "Tally",
                "contracts": [{"name": "Capacity", "role": "implements", "description": "x"},
                              {"name": "Supply", "role": "asks"},
                              {"name": "Odd", "role": "offers"}, {"role": "asks"},
                              {"name": "Capacity", "role": "implements"}]}"#,
        );
        assert_eq!(
            declared,
            Declaration::new("Tally")
                .contract("Capacity")
                .asking("Supply")
        );
        assert!(declared.implements("Capacity") && !declared.implements("Odd"));
        assert!(declared.asks_contract("Supply"));
        assert!(
            declaration(r#"{"name": "Tally", "contracts": {"name": "Capacity"}}"#)
                .contracts
                .is_empty()
        );
    }

    #[test]
    fn a_file_declares_the_kinds_of_the_events_it_emits() {
        let emitting = declaration(
            r#"{"name": "Tally",
                "events": [{"kind": "counted", "description": "x", "payloadSchema": {"type": "object"}},
                           {"description": "no kind"}, {"kind": "reset"}, {"kind": "counted"}]}"#,
        );
        assert_eq!(
            emitting,
            Declaration::new("Tally").event("counted").event("reset")
        );
        assert!(
            declaration(r#"{"name": "Tally", "events": {"kind": "counted"}}"#)
                .events
                .is_empty()
        );
    }

    #[test]
    fn a_file_that_does_not_parse_declares_nothing() {
        assert_eq!(declaration("{"), Declaration::new(""));
    }
}
