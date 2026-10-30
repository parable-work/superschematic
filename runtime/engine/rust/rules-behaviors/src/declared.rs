//! A behavior's declaration as the core reads it (D103), from the file the
//! compiler registers and the engine embeds
//! (`runtime/engine/rust/behaviors/declarations`): its name, the fields it
//! owns and the one that holds a field anchor's value (`value`), its
//! operations and whether each writes, its veto codes, and whether a create
//! gives it parameters (a createParamsSchema). The file stays the one
//! declaration; the core reads only what it holds a behavior to, and the
//! rest (descriptions, schemas, each field's column type, which a table's
//! layout reads) is the file's.

use serde_json::Value;
use superschematic_rules::Declaration;

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
        }
    }
    if let Some(value) = file.get("value").and_then(Value::as_str) {
        declaration = declaration.value(value);
    }
    for operation in listed(&file, "operations") {
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
                "fields": [{"name": "count"}, {"description": "no name"}],
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
        let valued =
            declaration(r#"{"name": "Tally", "fields": [{"name": "count"}], "value": "count"}"#);
        assert_eq!(valued, Declaration::new("Tally").value("count"));
        assert_eq!(valued.fields, ["count"]);
    }

    #[test]
    fn a_file_that_does_not_parse_declares_nothing() {
        assert_eq!(declaration("{"), Declaration::new(""));
    }
}
