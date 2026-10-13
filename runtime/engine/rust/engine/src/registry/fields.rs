//! The JSON Schema of a schema's fields, as the SDK generators write it
//! for tool arguments (`internal/generator/toolsutil` and the api
//! generator's scalar table, `apigen/scalars_jsonschema.go`): the port of
//! `src/tools/schema.ts`'s `FieldSchemas` (D58), which the registry reads to
//! know which of an instance type's fields hold a string, a number or a
//! boolean (an index's keys), a behavior's `parse_config` reads as
//! `ConfigTarget::field_schemas`, and the describe and tools documents
//! write (`crate::tools`).
//!
//! - a builtin primitive is `string`, `number` or `boolean`, with the
//!   generators' descriptions (`Int` an integer);
//! - a scalar takes its type from its `json_schema` type mapping, or from
//!   its primitive (an integer when its name or a mapping says so), its
//!   format from its declaration or from its name and mappings, its
//!   description, pattern, lengths and range, and names itself under the
//!   scalar key (`x-superschematic-scalar` by default; `ConfigTarget`'s
//!   field schemas leave the key out, as the TypeScript engine's do);
//!   `Generic.JSON`'s mapping is `any`: every JSON type but null;
//! - an enum is a string listing its serialized values;
//! - a type of the document is a closed object of its fields, sorted
//!   `required`; a type already being expanded is a plain reference, so
//!   recursive types end;
//! - a list is `items`, a list of lists `items` of `items`, with the
//!   field's value rules on the innermost items and its list bounds on the
//!   outer list;
//! - a field that is not required is nullable.
//!
//! A [`Property`] renders two ways: as `tools/schema.json` writes it (keys
//! sorted, `any` as the list of every JSON type but null, a nullable type
//! with `"null"` last; [`render_property`]), and as the Go encoder writes
//! it for `inputSchemaDigest` ([`encode_property`]: struct field order,
//! the scalar key first, the type of a nullable or an `any` property and a
//! nullable property's enum last, HTML characters escaped).

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

/// One property of an argument schema, as `tools/schema.ts`'s Property
/// holds it (`toolsutil.JSONSchemaProperty`): what [`FieldSchemas`] derives
/// of a field, or a JSON Schema the engine did not derive (`raw`, a
/// behavior's parameters), written as it is.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct Property {
    pub raw: Option<Value>,
    /// The scalar's schema name, written under the scalar key.
    pub canonical_scalar: Option<String>,
    pub nullable: bool,
    /// `additionalProperties: false`.
    pub closed: bool,
    /// A JSON Schema type; `any` for every JSON type.
    pub kind: Option<String>,
    pub format: Option<String>,
    pub description: Option<String>,
    pub pattern: Option<String>,
    pub values: Option<Vec<String>>,
    pub min_length: Option<f64>,
    pub max_length: Option<f64>,
    pub minimum: Option<f64>,
    pub maximum: Option<f64>,
    pub min_items: Option<f64>,
    pub max_items: Option<f64>,
    pub items: Option<Box<Property>>,
    pub properties: Option<Vec<(String, Property)>>,
    pub required: Option<Vec<String>>,
    /// JSON Schemas an object also satisfies, written as they are: what a
    /// behavior's validate holds an instance's own fields to.
    pub all_of: Option<Vec<Value>>,
}

impl Property {
    /// A JSON Schema written as it is.
    pub fn raw(schema: Value) -> Self {
        Property {
            raw: Some(schema),
            ..Property::default()
        }
    }

    /// A plain property of a type with a description.
    pub fn of(kind: &str, description: &str) -> Self {
        Property {
            kind: Some(kind.to_owned()),
            description: Some(description.to_owned()),
            ..Property::default()
        }
    }
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
        .map(|(key, property)| (key.clone(), render_property(property, "")))
        .collect()
}

/// The argument schemas of one document's types (`FieldSchemas`).
pub(crate) struct FieldSchemas<'d> {
    document: &'d Value,
}

impl<'d> FieldSchemas<'d> {
    pub fn new(document: &'d Value) -> Self {
        FieldSchemas { document }
    }

    /// The closed object schema of a type's fields, keyed by their JSON
    /// keys, and its required keys, sorted.
    pub fn object(&self, type_name: &str) -> (Vec<(String, Property)>, Vec<String>) {
        object_fields(self.document, type_name, &[])
    }

    /// The schema of a value of one of the document's types: a closed
    /// object of its fields, as a field of that type is written, not
    /// nullable.
    pub fn type_schema(&self, type_name: &str) -> Property {
        let (properties, required) =
            object_fields(self.document, type_name, &[type_name.to_owned()]);
        Property {
            kind: Some("object".to_owned()),
            description: Some(format!("{type_name} object")),
            closed: true,
            properties: Some(properties),
            required: Some(required),
            ..Property::default()
        }
    }
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

/// A property as `tools/schema.json` writes it
/// (`toolsutil.JSONSchemaPropertyLiteral`): keys sorted, the scalar name
/// under `scalar_key` when it is not empty, `any` as every JSON type but
/// null, and a nullable type, enum or list with null.
pub(crate) fn render_property(property: &Property, scalar_key: &str) -> Value {
    if let Some(raw) = &property.raw {
        return raw.clone();
    }
    let mut out: Vec<(String, Value)> = Vec::new();
    let mut put = |key: &str, value: Value| out.push((key.to_owned(), value));
    let any = property.kind.as_deref() == Some("any");
    let null = property.kind.as_deref() == Some("null");
    let nullable = property.nullable && !any && !null;
    if property.closed {
        put("additionalProperties", Value::Bool(false));
    }
    if let Some(description) = &property.description {
        put("description", Value::from(description.as_str()));
    }
    if let Some(values) = property.values.as_ref().filter(|values| !values.is_empty()) {
        let mut listed: Vec<Value> = values
            .iter()
            .map(|value| Value::from(value.as_str()))
            .collect();
        if nullable {
            listed.push(Value::Null);
        }
        put("enum", Value::Array(listed));
    }
    if let Some(format) = &property.format {
        put("format", Value::from(format.as_str()));
    }
    if let Some(items) = &property.items {
        put("items", render_property(items, scalar_key));
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
            put(key, js_number_value(number));
        }
    }
    if let Some(pattern) = &property.pattern {
        put("pattern", Value::from(pattern.as_str()));
    }
    if let Some(properties) = property.properties.as_ref().filter(|p| !p.is_empty()) {
        put("properties", render_properties(properties, scalar_key));
    }
    if let Some(required) = property.required.as_ref().filter(|r| !r.is_empty()) {
        put(
            "required",
            Value::Array(
                required
                    .iter()
                    .map(|key| Value::from(key.as_str()))
                    .collect(),
            ),
        );
    }
    if let Some(all_of) = property.all_of.as_ref().filter(|all| !all.is_empty()) {
        put("allOf", Value::Array(all_of.clone()));
    }
    put("type", type_value(property));
    if !scalar_key.is_empty() {
        if let Some(canonical) = &property.canonical_scalar {
            put(scalar_key, Value::from(canonical.as_str()));
        }
    }
    out.sort_by(|a, b| crate::json::compare_utf16(&a.0, &b.0));
    Value::Object(out.into_iter().collect())
}

/// Properties by key, sorted.
pub(crate) fn render_properties(properties: &[(String, Property)], scalar_key: &str) -> Value {
    let mut sorted: Vec<&(String, Property)> = properties.iter().collect();
    sorted.sort_by(|a, b| crate::json::compare_utf16(&a.0, &b.0));
    Value::Object(
        sorted
            .into_iter()
            .map(|(key, nested)| (key.clone(), render_property(nested, scalar_key)))
            .collect(),
    )
}

// type_value is a property's JSON Schema type: `any` as every JSON type
// but null, and a nullable type with null after it.
fn type_value(property: &Property) -> Value {
    let kind = property.kind.clone().unwrap_or_default();
    let nullable = property.nullable && kind != "null";
    if kind == "any" {
        let mut kinds = vec!["object", "array", "string", "number", "boolean"];
        if nullable {
            kinds.push("null");
        }
        return Value::Array(kinds.into_iter().map(Value::from).collect());
    }
    if nullable {
        Value::Array(vec![Value::from(kind), Value::from("null")])
    } else {
        Value::from(kind)
    }
}

/// A property as the Go encoder writes a `toolsutil.JSONSchemaProperty`
/// (`MarshalJSON`): the text `inputSchemaDigest` hashes. A nullable
/// property is a struct that embeds the plain one and redeclares type and
/// enum, and an `any` one a struct that redeclares type, which Go writes
/// after every embedded member.
pub(crate) fn encode_property(property: &Property, scalar_key: &str) -> String {
    use crate::json::{go_json, go_string};
    if let Some(raw) = &property.raw {
        return go_json(raw);
    }
    let mut members: Vec<String> = Vec::new();
    if !scalar_key.is_empty() {
        if let Some(canonical) = &property.canonical_scalar {
            members.push(format!(
                "{}:{}",
                go_string(scalar_key),
                go_string(canonical)
            ));
        }
    }
    let any = property.kind.as_deref() == Some("any");
    let nullable = property.nullable && !any && property.kind.as_deref() != Some("null");
    if property.closed {
        members.push("\"additionalProperties\":false".to_owned());
    }
    if !nullable && !any {
        members.push(format!(
            "\"type\":{}",
            go_string(property.kind.as_deref().unwrap_or_default())
        ));
    }
    if let Some(format) = &property.format {
        members.push(format!("\"format\":{}", go_string(format)));
    }
    if let Some(description) = &property.description {
        members.push(format!("\"description\":{}", go_string(description)));
    }
    if let Some(pattern) = &property.pattern {
        members.push(format!("\"pattern\":{}", go_string(pattern)));
    }
    let values = property.values.as_ref().filter(|values| !values.is_empty());
    let listed = |values: &Vec<String>, null: bool| {
        let mut items: Vec<Value> = values.iter().map(|v| Value::from(v.as_str())).collect();
        if null {
            items.push(Value::Null);
        }
        go_json(&Value::Array(items))
    };
    if !nullable {
        if let Some(values) = values {
            members.push(format!("\"enum\":{}", listed(values, false)));
        }
    }
    for (key, number) in [
        ("minLength", property.min_length),
        ("maxLength", property.max_length),
        ("minimum", property.minimum),
        ("maximum", property.maximum),
        ("minItems", property.min_items),
        ("maxItems", property.max_items),
    ] {
        if let Some(number) = number {
            members.push(format!("\"{key}\":{}", crate::json::js_number(number)));
        }
    }
    if let Some(items) = &property.items {
        members.push(format!("\"items\":{}", encode_property(items, scalar_key)));
    }
    if let Some(properties) = property.properties.as_ref().filter(|p| !p.is_empty()) {
        members.push(format!(
            "\"properties\":{}",
            encode_properties(properties, scalar_key)
        ));
    }
    if let Some(required) = property.required.as_ref().filter(|r| !r.is_empty()) {
        members.push(format!(
            "\"required\":{}",
            go_json(&Value::Array(
                required
                    .iter()
                    .map(|key| Value::from(key.as_str()))
                    .collect()
            ))
        ));
    }
    if nullable || any {
        members.push(format!("\"type\":{}", go_json(&type_value(property))));
    }
    if nullable {
        if let Some(values) = values {
            members.push(format!("\"enum\":{}", listed(values, true)));
        }
    }
    if let Some(all_of) = property.all_of.as_ref().filter(|all| !all.is_empty()) {
        members.push(format!(
            "\"allOf\":{}",
            go_json(&Value::Array(all_of.clone()))
        ));
    }
    format!("{{{}}}", members.join(","))
}

/// Properties as the Go encoder writes them, by key, sorted.
pub(crate) fn encode_properties(properties: &[(String, Property)], scalar_key: &str) -> String {
    let mut sorted: Vec<&(String, Property)> = properties.iter().collect();
    sorted.sort_by(|a, b| crate::json::compare_utf16(&a.0, &b.0));
    format!(
        "{{{}}}",
        sorted
            .into_iter()
            .map(|(key, property)| format!(
                "{}:{}",
                crate::json::go_string(key),
                encode_property(property, scalar_key)
            ))
            .collect::<Vec<_>>()
            .join(",")
    )
}

/// The schema of a merge patch of an object: its properties with none
/// required, down through nested objects, which a patch merges. A list is
/// replaced whole, so its items keep theirs.
pub(crate) fn patch_of(property: &Property, description: Option<&str>) -> Property {
    let Some(properties) = &property.properties else {
        return property.clone();
    };
    let mut out = property.clone();
    out.properties = Some(
        properties
            .iter()
            .map(|(key, child)| (key.clone(), patch_of(child, None)))
            .collect(),
    );
    out.all_of = None;
    if let Some(description) = description {
        out.description = Some(description.to_owned());
    }
    out.required = Some(Vec::new());
    out
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
