//! The JSON type of a field's value, as the SDK generators write it for a
//! tool's arguments: the part of `src/tools/schema.ts` (`FieldSchemas`)
//! the registry reads, to know which of an instance type's fields hold a
//! string, a number or a boolean (an index's keys). The rest of that file,
//! the argument schemas and their digests, comes with the tools document
//! (milestone 6).
//!
//! A builtin primitive is `string`, `number`, `boolean` or `integer`
//! (`Int`); a scalar takes its type from its `json_schema` type mapping, or
//! from its language primitive (an integer when its name or a mapping says
//! so), `any` for every JSON type; a type of the document is an `object`;
//! an enum or any other name is a `string`; a list is an `array`.

use serde_json::Value;

use superschematic_schema_ir::builtin_scalars;

use super::document::{array_depth, ref_kind, scalar_key, type_name, RefKind};

/// The JSON type a scalar's or primitive's values have, as a field of it
/// is written; `None` for a name that is neither.
pub(crate) fn scalar_type(document: &Value, name: &str) -> Option<String> {
    match name {
        "string" | "String" | "ID" => return Some("string".to_owned()),
        "number" | "Float" => return Some("number".to_owned()),
        "boolean" | "Boolean" => return Some("boolean".to_owned()),
        "Int" => return Some("integer".to_owned()),
        _ => {}
    }
    if ref_kind(document, name) != RefKind::Scalar {
        return None;
    }
    let (language_primitive, mappings) = scalar_definition(document, name);
    if let Some(mapped) = mappings
        .iter()
        .find(|(language, _)| language == "json_schema")
        .map(|(_, mapped)| mapped.as_str())
        .filter(|mapped| !mapped.is_empty())
    {
        return Some(mapped.to_owned());
    }
    Some(
        match language_primitive.as_str() {
            "number" if integer_trait(name, &mappings) => "integer",
            "number" => "number",
            "boolean" => "boolean",
            _ => "string",
        }
        .to_owned(),
    )
}

/// The JSON type of a field's value: a list's `array`, else its type's.
pub(crate) fn field_type(document: &Value, field: &Value) -> String {
    if array_depth(field) > 0 {
        return "array".to_owned();
    }
    let name = type_name(field);
    if let Some(scalar) = scalar_type(document, name) {
        return scalar;
    }
    if ref_kind(document, name) == RefKind::Type {
        "object".to_owned()
    } else {
        "string".to_owned()
    }
}

// A scalar as the Go loader leaves it: a catalog scalar's row wins over
// what the document says about it, and the document's type mappings stay
// beside the catalog's. Its language primitive and type mappings.
fn scalar_definition(document: &Value, name: &str) -> (String, Vec<(String, String)>) {
    let own = document
        .get("scalars")
        .and_then(|scalars| scalars.get(name));
    let own_mappings: Vec<(String, String)> = own
        .and_then(|scalar| scalar.get("typeMappings"))
        .and_then(Value::as_object)
        .map(|mappings| {
            mappings
                .iter()
                .filter_map(|(language, mapped)| {
                    mapped
                        .as_str()
                        .map(|mapped| (language.clone(), mapped.to_owned()))
                })
                .collect()
        })
        .unwrap_or_default();
    if let Some(row) = builtin_scalars().get(&scalar_key(name)) {
        let mut mappings = own_mappings;
        for (language, mapped) in &row.type_mappings {
            match mappings.iter_mut().find(|(known, _)| known == language) {
                Some(entry) => entry.1 = mapped.clone(),
                None => mappings.push((language.clone(), mapped.clone())),
            }
        }
        return (language_primitive_of(&row.primitive).to_owned(), mappings);
    }
    let language_primitive = own
        .and_then(|scalar| scalar.get("languagePrimitive"))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    (language_primitive, own_mappings)
}

// The language primitive the Go loader gives a scalar it fills in from a
// catalog row (ir.CatalogLanguagePrimitive).
fn language_primitive_of(primitive: &str) -> &'static str {
    match primitive.trim().to_lowercase().as_str() {
        "string" | "str" => "string",
        "number" | "float" | "float64" | "int" | "int32" | "int64" | "integer" => "number",
        "bool" | "boolean" => "boolean",
        _ => "object",
    }
}

// Whether a scalar is an integer, from its name's last segment and its
// type mappings (codegen.BuildScalarTraits).
fn integer_trait(name: &str, mappings: &[(String, String)]) -> bool {
    const INTEGERS: [&str; 4] = ["int", "int32", "int64", "integer"];
    let last = name
        .trim()
        .rsplit('.')
        .next()
        .unwrap_or_default()
        .to_lowercase();
    INTEGERS.contains(&last.as_str())
        || mappings
            .iter()
            .any(|(_, mapped)| INTEGERS.contains(&mapped.trim().to_lowercase().as_str()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_fields_json_type_is_the_generators() {
        let document = json!({
            "enums": {"Tier": {"name": "Tier", "values": [{"name": "FREE"}]}},
            "types": {"Address": {"name": "Address"}},
            "scalars": {
                "Local.Count": {"name": "Local.Count", "languagePrimitive": "number", "typeMappings": {"go": "int64"}},
                "Local.Ratio": {"name": "Local.Ratio", "languagePrimitive": "number"},
                "Local.Blob": {"name": "Local.Blob", "languagePrimitive": "object", "typeMappings": {"json_schema": "object"}}
            }
        });
        let field = |name: &str, extra: Value| {
            let mut field = json!({"name": "f", "typeRef": {"name": name}});
            if let Value::Object(extra) = extra {
                field["typeRef"].as_object_mut().unwrap().extend(extra);
            }
            field_type(&document, &field)
        };
        assert_eq!(field("string", json!({})), "string");
        assert_eq!(field("Int", json!({})), "integer");
        assert_eq!(field("Generic.Int64", json!({})), "integer");
        assert_eq!(field("Generic.JSON", json!({})), "any");
        assert_eq!(field("Contact.Email", json!({})), "string");
        assert_eq!(field("Local.Count", json!({})), "integer");
        assert_eq!(field("Local.Ratio", json!({})), "number");
        assert_eq!(field("Local.Blob", json!({})), "object");
        assert_eq!(field("Tier", json!({})), "string");
        assert_eq!(field("Address", json!({})), "object");
        assert_eq!(field("string", json!({"isArray": true})), "array");
    }
}
