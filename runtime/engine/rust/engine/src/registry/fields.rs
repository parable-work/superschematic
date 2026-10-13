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

use serde_json::{Map, Value};

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

// One property of a type's JSON Schema, as tools/schema.ts's Property holds
// it: what FieldSchemas derives of a field.
#[derive(Clone, Debug, Default)]
struct Property {
    // Written under the scalar key, which the tools document names
    // (milestone 6); ConfigTarget's field schemas leave it out.
    #[allow(dead_code)]
    canonical_scalar: Option<String>,
    nullable: bool,
    closed: bool,
    kind: Option<String>,
    format: Option<String>,
    description: Option<String>,
    pattern: Option<String>,
    values: Option<Vec<String>>,
    min_length: Option<f64>,
    max_length: Option<f64>,
    minimum: Option<f64>,
    maximum: Option<f64>,
    min_items: Option<f64>,
    max_items: Option<f64>,
    items: Option<Box<Property>>,
    properties: Option<Vec<(String, Property)>>,
    required: Option<Vec<String>>,
}

// A scalar's JSON Schema facts: apigen.ScalarJSONSchemaInfo.
struct ScalarInfo {
    canonical_name: String,
    kind: String,
    format: String,
    description: String,
    pattern: String,
    min_length: Option<f64>,
    max_length: Option<f64>,
    minimum: Option<f64>,
    maximum: Option<f64>,
}

/// The JSON Schema of each of a type's own fields, by JSON key, as the
/// describe document writes an instance's properties without the scalar
/// key (tools/schema.ts: `new FieldSchemas(document).object(type)`, each
/// property through `renderProperty(property, '')`): what a behavior's
/// parseConfig reads as `ConfigTarget::field_schemas`.
pub(crate) fn field_schemas(document: &Value, type_name: &str) -> Map<String, Value> {
    let (properties, _) = object_fields(document, type_name, &[]);
    properties
        .iter()
        .map(|(key, property)| (key.clone(), render_property(property)))
        .collect()
}

// object_fields is FieldSchemas.fields: a type's fields by JSON key, and
// the required ones, sorted.
fn object_fields(
    document: &Value,
    type_name: &str,
    stack: &[String],
) -> (Vec<(String, Property)>, Vec<String>) {
    let mut properties = Vec::new();
    let mut required = Vec::new();
    for field in super::document::fields_of(document, type_name) {
        let key = super::document::json_key(field).to_owned();
        properties.push((key.clone(), field_property(document, field, stack)));
        if super::document::flag(field, "required") {
            required.push(key);
        }
    }
    required.sort();
    (properties, required)
}

// field_property is FieldSchemas.field: the schema of one field's value.
fn field_property(document: &Value, field: &Value, stack: &[String]) -> Property {
    let name = type_name(field);
    let mut value = type_property(document, name);
    if scalar_info(document, name).is_none()
        && ref_kind(document, name) == RefKind::Type
        && !stack.iter().any(|seen| seen == name)
    {
        let mut nested = stack.to_vec();
        nested.push(name.to_owned());
        let (properties, required) = object_fields(document, name, &nested);
        value = Property {
            kind: Some("object".to_owned()),
            description: Some(format!("{name} object")),
            closed: true,
            properties: Some(properties),
            required: Some(required),
            ..Property::default()
        };
    }
    let depth = array_depth(field);
    if depth > 0 {
        value = with_rules(value, field, false);
        for level in 1..=depth {
            value = Property {
                kind: Some("array".to_owned()),
                description: Some(format!(
                    "Array of {}{name} values",
                    if level > 1 { "arrays of " } else { "" }
                )),
                items: Some(Box::new(value)),
                ..Property::default()
            };
        }
        value.min_items = super::document::number(field, "validateListMin");
        value.max_items = super::document::number(field, "validateListMax");
    } else {
        value = with_rules(value, field, true);
    }
    value.nullable = !super::document::flag(field, "required");
    value
}

// non_empty is a string the Go encoder writes: an empty one is omitted.
fn non_empty(text: &str) -> Option<String> {
    (!text.is_empty()).then(|| text.to_owned())
}

// type_property is toolsutil.typeToJSONSchemaProperty: a scalar's facts, an
// enum's values, or a string described by the type's name.
fn type_property(document: &Value, name: &str) -> Property {
    if let Some(info) = scalar_info(document, name) {
        return Property {
            canonical_scalar: non_empty(&info.canonical_name),
            kind: non_empty(&info.kind),
            format: non_empty(&info.format),
            description: non_empty(&info.description),
            pattern: non_empty(&info.pattern),
            min_length: info.min_length,
            max_length: info.max_length,
            minimum: info.minimum,
            maximum: info.maximum,
            ..Property::default()
        };
    }
    let mut property = Property {
        kind: Some("string".to_owned()),
        description: Some(format!("A {name} value")),
        ..Property::default()
    };
    if ref_kind(document, name) == RefKind::Enum {
        let values: Vec<String> = document
            .get("enums")
            .and_then(|enums| enums.get(name))
            .and_then(|definition| definition.get("values"))
            .and_then(Value::as_array)
            .map(|values| {
                values
                    .iter()
                    .map(|value| {
                        let serialized = super::document::text(value, "serializedAs").unwrap_or("");
                        if serialized.is_empty() {
                            super::document::text(value, "name")
                                .unwrap_or("")
                                .to_owned()
                        } else {
                            serialized.to_owned()
                        }
                    })
                    .collect()
            })
            .unwrap_or_default();
        if !values.is_empty() {
            property.values = Some(values);
        }
    }
    property
}

// with_rules is toolsutil.applyFieldValidation: a field's own lengths,
// pattern and range replace its scalar's, a range truncated to an integer;
// its list bounds apply only where the field is not a list.
fn with_rules(mut property: Property, field: &Value, list_bounds: bool) -> Property {
    let number = |key: &str| super::document::number(field, key);
    if let Some(min) = number("validateMinLength") {
        property.min_length = Some(min);
    }
    if let Some(max) = number("validateMaxLength") {
        property.max_length = Some(max);
    }
    if list_bounds {
        if let Some(min) = number("validateListMin") {
            property.min_items = Some(min);
        }
        if let Some(max) = number("validateListMax") {
            property.max_items = Some(max);
        }
    }
    if let Some(pattern) = super::document::text(field, "validatePattern").filter(|p| !p.is_empty())
    {
        property.pattern = Some(pattern.to_owned());
    }
    if let Some(min) = number("validateMin") {
        property.minimum = Some(min.trunc());
    }
    if let Some(max) = number("validateMax") {
        property.maximum = Some(max.trunc());
    }
    property
}

// scalar_info is the apigen scalar table's row for a type name: a builtin
// primitive, or a scalar the document declares or the catalog holds.
fn scalar_info(document: &Value, name: &str) -> Option<ScalarInfo> {
    let primitive = |kind: &str, description: &str| ScalarInfo {
        canonical_name: String::new(),
        kind: kind.to_owned(),
        format: String::new(),
        description: description.to_owned(),
        pattern: String::new(),
        min_length: None,
        max_length: None,
        minimum: None,
        maximum: None,
    };
    match name {
        "string" | "String" | "ID" => return Some(primitive("string", "A string value")),
        "number" | "Float" => return Some(primitive("number", "A numeric value")),
        "boolean" | "Boolean" => {
            return Some(primitive("boolean", "A boolean value (true or false)"))
        }
        "Int" => return Some(primitive("integer", "An integer value")),
        _ => {}
    }
    if ref_kind(document, name) != RefKind::Scalar {
        return None;
    }
    let kind = scalar_type(document, name).unwrap_or_else(|| "string".to_owned());
    let (_, mappings) = scalar_definition(document, name);
    let own = document
        .get("scalars")
        .and_then(|scalars| scalars.get(name));
    let row = builtin_scalars().get(&scalar_key(name));
    let own_text = |key: &str| {
        own.and_then(|scalar| super::document::text(scalar, key))
            .unwrap_or("")
            .to_owned()
    };
    let own_number = |key: &str| own.and_then(|scalar| super::document::number(scalar, key));
    let (description, comment, min_length, max_length, minimum, maximum, pattern, format) =
        match row {
            Some(row) => (
                row.description.clone(),
                own_text("comment"),
                row.min_length,
                row.max_length,
                row.minimum,
                row.maximum,
                row.pattern.clone(),
                row.format.clone(),
            ),
            None => (
                own_text("description"),
                own_text("comment"),
                own_number("minLength").unwrap_or(0.0),
                own_number("maxLength").unwrap_or(0.0),
                own_number("minimum"),
                own_number("maximum"),
                own_text("pattern"),
                own_text("format"),
            ),
        };
    let format = if format.is_empty() {
        traits_format(name, &mappings)
    } else {
        format
    };
    let description = if description.trim().is_empty() {
        comment
    } else {
        description
    };
    Some(ScalarInfo {
        canonical_name: name.to_owned(),
        kind,
        format,
        description: if description.is_empty() {
            format!("A {name} value")
        } else {
            description
        },
        pattern,
        min_length: (min_length > 0.0).then_some(min_length),
        max_length: (max_length > 0.0).then_some(max_length),
        minimum,
        maximum,
    })
}

// traits_format is the format codegen.BuildScalarTraits gives a scalar that
// declares none: from its name's last segment and its type mappings, an
// email, a date-time, a date or a URI.
fn traits_format(name: &str, mappings: &[(String, String)]) -> String {
    let last = name
        .trim()
        .rsplit('.')
        .next()
        .unwrap_or_default()
        .to_lowercase();
    let email = last == "email";
    let mut date_time = last == "datetime";
    let date = last == "date";
    let url = last == "url" || last == "uri";
    for (_, mapped) in mappings {
        let normalized = mapped.trim().to_lowercase();
        if ["time.time", "datetime", "datetime.datetime", "jsdate"].contains(&normalized.as_str())
            || normalized.contains("time.time")
        {
            date_time = true;
        }
    }
    if email {
        "email"
    } else if date_time {
        "date-time"
    } else if date {
        "date"
    } else if url {
        "uri"
    } else {
        ""
    }
    .to_owned()
}

// js_number_value is a number as JavaScript writes it in JSON.
fn js_number_value(value: f64) -> Value {
    serde_json::from_str(&crate::json::js_number(value)).unwrap_or(Value::Null)
}

// render_property is renderProperty(property, ''): keys sorted, `any` as
// every JSON type but null, a nullable type, enum or list with null.
fn render_property(property: &Property) -> Value {
    let mut out: Vec<(&str, Value)> = Vec::new();
    let any = property.kind.as_deref() == Some("any");
    let null = property.kind.as_deref() == Some("null");
    let nullable = property.nullable && !any && !null;
    if property.closed {
        out.push(("additionalProperties", Value::Bool(false)));
    }
    if let Some(description) = &property.description {
        out.push(("description", Value::from(description.as_str())));
    }
    if let Some(values) = property.values.as_ref().filter(|values| !values.is_empty()) {
        let mut listed: Vec<Value> = values
            .iter()
            .map(|value| Value::from(value.as_str()))
            .collect();
        if nullable {
            listed.push(Value::Null);
        }
        out.push(("enum", Value::Array(listed)));
    }
    if let Some(format) = &property.format {
        out.push(("format", Value::from(format.as_str())));
    }
    if let Some(items) = &property.items {
        out.push(("items", render_property(items)));
    }
    for (key, number) in [
        ("maxItems", property.max_items),
        ("maxLength", property.max_length),
        ("maximum", property.maximum),
        ("minItems", property.min_items),
        ("minLength", property.min_length),
        ("minimum", property.minimum),
    ] {
        if let Some(number) = number {
            out.push((key, js_number_value(number)));
        }
    }
    if let Some(pattern) = &property.pattern {
        out.push(("pattern", Value::from(pattern.as_str())));
    }
    if let Some(properties) = property.properties.as_ref().filter(|p| !p.is_empty()) {
        let mut sorted: Vec<&(String, Property)> = properties.iter().collect();
        sorted.sort_by(|a, b| crate::json::compare_utf16(&a.0, &b.0));
        out.push((
            "properties",
            Value::Object(
                sorted
                    .into_iter()
                    .map(|(key, nested)| (key.clone(), render_property(nested)))
                    .collect(),
            ),
        ));
    }
    if let Some(required) = property.required.as_ref().filter(|r| !r.is_empty()) {
        out.push((
            "required",
            Value::Array(
                required
                    .iter()
                    .map(|key| Value::from(key.as_str()))
                    .collect(),
            ),
        ));
    }
    let kind = property.kind.clone().unwrap_or_default();
    let type_nullable = property.nullable && !null;
    let type_value = if any {
        let mut kinds = vec!["object", "array", "string", "number", "boolean"];
        if type_nullable {
            kinds.push("null");
        }
        Value::Array(kinds.into_iter().map(Value::from).collect())
    } else if type_nullable {
        Value::Array(vec![Value::from(kind), Value::from("null")])
    } else {
        Value::from(kind)
    };
    out.push(("type", type_value));
    out.sort_by(|a, b| crate::json::compare_utf16(a.0, b.0));
    Value::Object(
        out.into_iter()
            .map(|(key, value)| (key.to_owned(), value))
            .collect(),
    )
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
