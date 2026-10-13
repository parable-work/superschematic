//! Tool argument schemas and their digests, the port of the rest of
//! `src/tools/schema.ts` (D58): an argument schema is a closed object of
//! properties ([`crate::registry::fields`] derives a field's), with the
//! vendor keys at its root, written as `tools/schema.json` writes it and
//! hashed as the Go encoder writes it (`inputSchemaDigest`), held to the Go
//! generators by `runtime/engine/testdata/tool_parameters_parity.json`; and
//! the names the SDK generators give a tool.

use serde_json::{Map, Value};

use crate::errors::{EngineError, Result};
use crate::json::{go_json, go_string};
use crate::registry::document::{ref_kind, RefKind};
use crate::registry::fields::{encode_properties, render_properties, FieldSchemas, Property};

use super::options::ToolKeys;

/// The longest MCP handle @mcp accepts (`ir.MCPHandleMaxLength`).
pub const MCP_HANDLE_MAX_LENGTH: usize = 48;

/// Whether @mcp takes a handle: lowercase snake case starting with a
/// letter (`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`), at most
/// [`MCP_HANDLE_MAX_LENGTH`] long.
pub fn is_mcp_handle(handle: &str) -> bool {
    is_snake_handle(handle) && handle.len() <= MCP_HANDLE_MAX_LENGTH
}

// ^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$
pub(crate) fn is_snake_handle(handle: &str) -> bool {
    let mut parts = handle.split('_');
    let Some(first) = parts.next() else {
        return false;
    };
    let mut chars = first.chars();
    if !chars.next().is_some_and(|c| c.is_ascii_lowercase())
        || !chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit())
    {
        return false;
    }
    parts.all(|part| {
        !part.is_empty()
            && part
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit())
    })
}

/// `codegen.ToKebabCase`: a hyphen at each lower-to-upper boundary, then
/// lowercase.
pub fn kebab_case(name: &str) -> String {
    boundaries(name, '-').to_lowercase()
}

/// `codegen.ToSnakeCase`: hyphens become underscores, an underscore at
/// each lower-to-upper boundary, then lowercase.
pub fn snake_case(name: &str) -> String {
    boundaries(&name.replace('-', "_"), '_').to_lowercase()
}

// boundaries puts `mark` between a lowercase letter or digit and the
// uppercase letter after it, as /([a-z0-9])([A-Z])/g does: each match takes
// its two characters, so a run of capitals splits once.
fn boundaries(name: &str, mark: char) -> String {
    let chars: Vec<char> = name.chars().collect();
    let mut out = String::with_capacity(name.len() + 4);
    let mut index = 0;
    while index < chars.len() {
        let c = chars[index];
        out.push(c);
        if (c.is_ascii_lowercase() || c.is_ascii_digit())
            && chars
                .get(index + 1)
                .is_some_and(|next| next.is_ascii_uppercase())
        {
            out.push(mark);
            out.push(chars[index + 1]);
            index += 2;
            continue;
        }
        index += 1;
    }
    out
}

/// An argument schema: `toolsutil.JSONSchemaObject`, a closed object.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct ArgumentSchema {
    /// Written first: `ToolKeys.parameters`.
    pub vendor: Vec<(String, Value)>,
    pub properties: Vec<(String, Property)>,
    /// Sorted.
    pub required: Vec<String>,
}

/// An argument schema as `tools/schema.json` writes it: type,
/// additionalProperties, the vendor keys, properties, and required when it
/// lists a key.
pub(crate) fn render_arguments(schema: &ArgumentSchema, scalar_key: &str) -> Value {
    let mut out = Map::new();
    out.insert("type".into(), Value::from("object"));
    out.insert("additionalProperties".into(), Value::Bool(false));
    for (key, value) in &schema.vendor {
        out.insert(key.clone(), value.clone());
    }
    out.insert(
        "properties".into(),
        render_properties(&schema.properties, scalar_key),
    );
    if !schema.required.is_empty() {
        out.insert(
            "required".into(),
            Value::Array(
                schema
                    .required
                    .iter()
                    .map(|key| Value::from(key.as_str()))
                    .collect(),
            ),
        );
    }
    Value::Object(out)
}

/// An argument schema as the Go encoder writes a
/// `toolsutil.JSONSchemaObject`: the text `inputSchemaDigest` hashes.
pub(crate) fn encode_arguments(schema: &ArgumentSchema, scalar_key: &str) -> String {
    let mut members: Vec<String> = schema
        .vendor
        .iter()
        .map(|(key, value)| format!("{}:{}", go_string(key), go_json(value)))
        .collect();
    members.push("\"additionalProperties\":false".to_owned());
    members.push("\"type\":\"object\"".to_owned());
    members.push(format!(
        "\"properties\":{}",
        encode_properties(&schema.properties, scalar_key)
    ));
    if !schema.required.is_empty() {
        members.push(format!(
            "\"required\":{}",
            go_json(&Value::Array(
                schema
                    .required
                    .iter()
                    .map(|key| Value::from(key.as_str()))
                    .collect()
            ))
        ));
    }
    format!("{{{}}}", members.join(","))
}

/// `inputSchemaDigest`: `sha256:` and the hex SHA-256 of the Go encoding.
pub(crate) fn arguments_digest(schema: &ArgumentSchema, scalar_key: &str) -> String {
    format!(
        "sha256:{}",
        crate::json::sha256_hex(&encode_arguments(schema, scalar_key))
    )
}

/// The tool argument schema the SDK generators write for an operation
/// whose input is one of a document's types, keyed by its fields' JSON
/// keys, with the digest they write beside it (`tools/schema.json`'s
/// `parameters` and `inputSchemaDigest`).
pub fn type_arguments(
    document: &Value,
    type_name: &str,
    keys: &ToolKeys,
) -> Result<(Value, String)> {
    if ref_kind(document, type_name) != RefKind::Type {
        return Err(EngineError::invalid_argument(format!(
            "{type_name} is not a type of the document"
        ))
        .into());
    }
    let (properties, required) = FieldSchemas::new(document).object(type_name);
    let schema = ArgumentSchema {
        vendor: keys.parameters.clone(),
        properties,
        required,
    };
    Ok((
        render_arguments(&schema, &keys.scalar),
        arguments_digest(&schema, &keys.scalar),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_take_the_go_case_conversions() {
        assert_eq!(kebab_case("LineItem"), "line-item");
        assert_eq!(snake_case("LineItem"), "line_item");
        assert_eq!(snake_case("addNote"), "add_note");
        assert_eq!(kebab_case("HTTPServer"), "httpserver");
        assert_eq!(snake_case("a-bC"), "a_b_c");
        assert!(is_mcp_handle("order_create"));
        assert!(!is_mcp_handle("Order_create"));
        assert!(!is_mcp_handle("order__create"));
        assert!(!is_mcp_handle("order_"));
        assert!(!is_mcp_handle(&"a".repeat(49)));
    }
}
