//! What a schema is to the engine, the port of `src/registry/document.ts`
//! (D16). A schema is one JSON schema-file document (the form `superschematic
//! format --to=json` writes) that the strict loader of
//! `superschematic-schema-ir` reads against the deployment's meta-schema.
//! On top of the loader, the engine requires:
//!
//! - `kind: General` and a `name` that fits in a URL path segment;
//! - no `imports` (a type in another schema is reached through a link
//!   behavior) and no `operationSets` (the engine serves create, get, list,
//!   update and delete, and behaviors add their own);
//! - an instance type: the type named like the schema, or its only type;
//! - field types the schema runtime validates: a builtin primitive, a
//!   scalar, an enum or a type of the document, alone, as a list or a list
//!   of lists. A union or a map is refused;
//! - each scalar's map key is its name, as the Go data-form reader
//!   requires;
//! - a scalar the document declares and the builtin catalog does not hold,
//!   whose language primitive is `object`, is one the schema runtime holds
//!   to JSON ([`object_scalar_issue`]);
//! - behaviors composed as the compiler's loader requires (`composition`),
//!   and @unique, @key and @index only where the engine indexes them
//!   (`indexes`), which the caller checks through [`read_schema`]'s
//!   `compose` argument.
//!
//! The document stays the loader's JSON: the engine reads its types,
//! fields and scalars as the TypeScript engine reads the same object, and
//! the schema runtime reads it again, through the IR reader, to validate.

use std::borrow::Cow;
use std::collections::BTreeSet;

use serde_json::{Map, Value};

use superschematic_schema_ir::schemafile::SchemaFileLoader;
use superschematic_schema_ir::{
    builtin_scalars, is_any_json_scalar, parse_schema_ir, structured_json_type,
};

use crate::errors::{EngineError, Error, SchemaIssue};
use crate::json::{self, js_keys};

/// What a schema name is, as a regular expression: a letter, then letters,
/// digits, `_` and `-`, at most 128 characters.
pub const SCHEMA_NAME: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";

/// Whether a name matches [`SCHEMA_NAME`].
pub fn is_schema_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 128
        && bytes[0].is_ascii_alphabetic()
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'_' || *b == b'-')
}

/// Refuses a schema name the engine would never store.
pub(crate) fn check_schema_name(name: &str) -> Result<(), EngineError> {
    if is_schema_name(name) {
        return Ok(());
    }
    Err(EngineError::invalid_argument(format!(
        "schema name \"{name}\" must match {SCHEMA_NAME}"
    )))
}

/// A schema document the engine accepted.
#[derive(Clone, Debug, PartialEq)]
pub struct SchemaModel {
    pub name: String,
    /// The type that holds instances.
    pub instance_type: String,
    /// The document as the loader decoded it.
    pub document: Value,
    /// The document as the loader's canonical JSON: compact, keys sorted.
    pub canonical: String,
}

/// What a field's type name refers to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RefKind {
    Primitive,
    Scalar,
    Enum,
    Type,
    Union,
    Unknown,
}

// The builtin names the schema runtime checks: the IR's primitives and the
// GraphQL names its JSON Schema reader produces.
const PRIMITIVES: [&str; 8] = [
    "string", "number", "boolean", "String", "ID", "Int", "Float", "Boolean",
];

/// Loads a schema document from its JSON text and applies the engine's
/// rules, and `compose`'s to its behaviors when it has an instance type and
/// a name. It refuses with every issue it finds.
pub(crate) fn read_schema(
    loader: &SchemaFileLoader,
    text: &str,
    source: &str,
    compose: impl FnOnce(&SchemaModel) -> Vec<SchemaIssue>,
) -> Result<SchemaModel, EngineError> {
    let loaded = loader.load(text, source).map_err(|error| {
        EngineError::schema_document(
            error.source(),
            error
                .issues()
                .iter()
                .map(|issue| SchemaIssue::new(issue.path.clone(), issue.message.clone()))
                .collect(),
        )
    })?;
    let document = loaded.document;
    let canonical = loaded.canonical;
    let mut issues = Vec::new();

    match document.get("kind").and_then(Value::as_str) {
        Some("General") => {}
        Some(kind) => issues.push(SchemaIssue::new(
            "/kind",
            format!("a schema is a document of kind General, not {kind}"),
        )),
        None => issues.push(SchemaIssue::new(
            "/kind",
            "a schema is a document of kind General; this file has no kind",
        )),
    }
    let name = document.get("name").and_then(Value::as_str);
    match name {
        None | Some("") => issues.push(SchemaIssue::new("/name", "a schema needs a name")),
        Some(name) if !is_schema_name(name) => issues.push(SchemaIssue::new(
            "/name",
            format!("schema name \"{name}\" must match {SCHEMA_NAME}"),
        )),
        Some(_) => {}
    }
    if non_empty_array(&document, "imports") {
        issues.push(SchemaIssue::new(
            "/imports",
            "a schema has no imports; reach a type in another schema through a link behavior",
        ));
    }
    if non_empty_array(&document, "operationSets") {
        issues.push(SchemaIssue::new(
            "/operationSets",
            "a schema declares no operations; the engine serves create, get, list, update and delete, and behaviors add their own",
        ));
    }
    // The Go data-form reader refuses a scalar whose map key is not its
    // name, and the schema runtime keys a scalar by its name, not its key.
    let mut mismatched = BTreeSet::new();
    if let Some(scalars) = document.get("scalars").and_then(Value::as_object) {
        for key in js_keys(scalars) {
            let declared = scalars[key].get("name").and_then(Value::as_str);
            if let Some(declared) =
                declared.filter(|declared| !declared.is_empty() && *declared != key)
            {
                mismatched.insert(key.to_owned());
                issues.push(SchemaIssue::new(
                    json::pointer(&["scalars", key]),
                    format!(
                        "scalar map key {} does not match the definition name {}",
                        json::quote(key),
                        json::quote(declared)
                    ),
                ));
            }
        }
    }
    // A catalog scalar is the catalog's, whatever the document declares for
    // it, and no builtin row is an object scalar without a JSON mapping.
    let runtime = runtime_document(&document);
    if let Some(scalars) = runtime.get("scalars").and_then(Value::as_object) {
        for key in js_keys(scalars) {
            if mismatched.contains(key) {
                continue;
            }
            if let Some(issue) = object_scalar_issue(key, &scalars[key]) {
                issues.push(SchemaIssue::new(json::pointer(&["scalars", key]), issue));
            }
        }
    }
    let instance_type = name.and_then(|name| instance_type_of(&document, name));
    if name.is_some() && instance_type.is_none() {
        let found: Vec<&str> = types_of(&document).map(js_keys).unwrap_or_default();
        issues.push(SchemaIssue::new(
            "/types",
            format!(
                "no instance type: declare a type named {} or exactly one type (found: {})",
                name.unwrap_or_default(),
                if found.is_empty() {
                    "none".to_owned()
                } else {
                    found.join(", ")
                }
            ),
        ));
    }
    if let Some(instance_type) = &instance_type {
        for type_name in reachable_types(&document, instance_type) {
            for (index, field) in fields_of(&document, &type_name).iter().enumerate() {
                if let Some(issue) = field_type_issue(&document, &type_name, field) {
                    issues.push(SchemaIssue::new(
                        format!(
                            "{}/fields/{index}/typeRef",
                            json::pointer(&["types", type_name.as_str()])
                        ),
                        issue,
                    ));
                }
            }
        }
        if let Some(name) = name.filter(|name| is_schema_name(name)) {
            let candidate = SchemaModel {
                name: name.to_owned(),
                instance_type: instance_type.clone(),
                document: document.clone(),
                canonical: canonical.clone(),
            };
            issues.extend(compose(&candidate));
        }
    }

    if !issues.is_empty() {
        return Err(EngineError::schema_document(source, issues));
    }
    Ok(SchemaModel {
        name: name.unwrap_or_default().to_owned(),
        instance_type: instance_type.unwrap_or_default(),
        document,
        canonical,
    })
}

fn non_empty_array(document: &Value, key: &str) -> bool {
    document
        .get(key)
        .and_then(Value::as_array)
        .is_some_and(|items| !items.is_empty())
}

/// Rebuilds the model of a document the engine already accepted, from its
/// canonical JSON.
pub(crate) fn model_of(canonical: &str) -> Result<SchemaModel, Error> {
    let document: Value = serde_json::from_str(canonical)
        .map_err(|error| Error::internal(format!("a stored schema is not JSON: {error}")))?;
    let name = document
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let instance_type = instance_type_of(&document, &name)
        .ok_or_else(|| Error::internal(format!("stored schema {name} has no instance type")))?;
    Ok(SchemaModel {
        name,
        instance_type,
        document,
        canonical: canonical.to_owned(),
    })
}

/// The document's types, by name.
pub(crate) fn types_of(document: &Value) -> Option<&Map<String, Value>> {
    document.get("types").and_then(Value::as_object)
}

/// One type of the document.
pub(crate) fn type_of<'d>(document: &'d Value, name: &str) -> Option<&'d Value> {
    types_of(document).and_then(|types| types.get(name))
}

/// A type's fields; none for a type the document does not have.
pub(crate) fn fields_of<'d>(document: &'d Value, type_name: &str) -> &'d [Value] {
    type_of(document, type_name)
        .and_then(|type_def| type_def.get("fields"))
        .and_then(Value::as_array)
        .map_or(&[], Vec::as_slice)
}

/// The type the schema's instances are: the type named like the schema, or
/// its only type.
pub(crate) fn instance_type_of(document: &Value, name: &str) -> Option<String> {
    let types = types_of(document)?;
    if types.contains_key(name) {
        return Some(name.to_owned());
    }
    if types.len() == 1 {
        return types.keys().next().cloned();
    }
    None
}

/// What a field's type name refers to in a document.
pub(crate) fn ref_kind(document: &Value, name: &str) -> RefKind {
    if PRIMITIVES.contains(&name) {
        return RefKind::Primitive;
    }
    let holds = |key: &str| {
        document
            .get(key)
            .and_then(Value::as_object)
            .is_some_and(|members| members.contains_key(name))
    };
    if holds("enums") {
        RefKind::Enum
    } else if holds("types") {
        RefKind::Type
    } else if holds("unions") {
        RefKind::Union
    } else if holds("scalars") || builtin_scalars().contains_key(&scalar_key(name)) {
        RefKind::Scalar
    } else {
        RefKind::Unknown
    }
}

/// The name the schema runtime keys a scalar by: dots become underscores.
pub(crate) fn scalar_key(name: &str) -> String {
    name.replace('.', "_")
}

/// A field's name.
pub(crate) fn field_name(field: &Value) -> &str {
    field
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// A field's type reference.
pub(crate) fn type_ref(field: &Value) -> &Value {
    field.get("typeRef").unwrap_or(&Value::Null)
}

/// The name of a field's type.
pub(crate) fn type_name(field: &Value) -> &str {
    type_ref(field)
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// Whether a member of a JSON object is `true`.
pub(crate) fn flag(value: &Value, key: &str) -> bool {
    value.get(key).and_then(Value::as_bool) == Some(true)
}

/// A member of a JSON object read as a number, as JavaScript reads it.
pub(crate) fn number(value: &Value, key: &str) -> Option<f64> {
    match value.get(key) {
        Some(Value::Number(number)) => Some(json::number_value(number)),
        _ => None,
    }
}

/// A member of a JSON object read as a string; `None` when absent.
pub(crate) fn text<'v>(value: &'v Value, key: &str) -> Option<&'v str> {
    value.get(key).and_then(Value::as_str)
}

/// Whether a field's type is a map.
pub(crate) fn is_map(field: &Value) -> bool {
    flag(type_ref(field), "isMap")
}

/// 0 for a single value, 1 for `T[]`, 2 for `T[][]`.
pub(crate) fn array_depth(field: &Value) -> usize {
    let type_ref = type_ref(field);
    if !flag(type_ref, "isArray") {
        0
    } else if flag(type_ref, "isArrayOfArrays") {
        2
    } else {
        1
    }
}

/// The key a field's value has in an instance: its JSON tag, else its
/// name.
pub(crate) fn json_key(field: &Value) -> &str {
    match text(field, "jsonTag") {
        Some(tag) if !tag.is_empty() => tag,
        _ => field_name(field),
    }
}

/// The types an instance can hold, from `root` through its fields, in the
/// order a breadth-first walk meets them.
pub(crate) fn reachable_types(document: &Value, root: &str) -> Vec<String> {
    let Some(types) = types_of(document) else {
        return Vec::new();
    };
    let mut seen: Vec<String> = Vec::new();
    let mut queue = std::collections::VecDeque::from([root.to_owned()]);
    while let Some(type_name) = queue.pop_front() {
        if seen.contains(&type_name) || !types.contains_key(&type_name) {
            continue;
        }
        for field in fields_of(document, &type_name) {
            if ref_kind(document, self::type_name(field)) == RefKind::Type {
                queue.push_back(self::type_name(field).to_owned());
            }
        }
        seen.push(type_name);
    }
    seen
}

/// The document as the schema runtime reads it to validate: a scalar the
/// builtin catalog holds is the catalog's, whatever the document declares
/// for it, as the Go loader fills it in. The form `superschematic format
/// --to=json` writes declares each catalog scalar its fields use, which the
/// schema runtime would otherwise read as the whole scalar.
pub(crate) fn runtime_document(document: &Value) -> Cow<'_, Value> {
    let Some(scalars) = document.get("scalars").and_then(Value::as_object) else {
        return Cow::Borrowed(document);
    };
    let own: Map<String, Value> = scalars
        .iter()
        .filter(|(name, _)| !builtin_scalars().contains_key(&scalar_key(name)))
        .map(|(name, scalar)| (name.clone(), scalar.clone()))
        .collect();
    if own.len() == scalars.len() {
        return Cow::Borrowed(document);
    }
    let mut copy = document.clone();
    copy["scalars"] = Value::Object(own);
    Cow::Owned(copy)
}

/// Why a scalar whose language primitive is `object` but which the schema
/// runtime does not hold to JSON is refused, or `None` for any other
/// scalar: the Go IR's `ScalarDef.ObjectJSONError`, word for word
/// (`ir/testdata/object_scalar_errors.json` holds the messages).
pub(crate) fn object_scalar_issue(name: &str, scalar: &Value) -> Option<String> {
    if text(scalar, "languagePrimitive") != Some("object") {
        return None;
    }
    // A file part is no JSON, whatever the mapping says.
    if scalar
        .get("fileUpload")
        .is_some_and(|upload| !upload.is_null())
    {
        return Some(format!(
            "scalar {name} is a file-upload scalar with language primitive object: a file part is not JSON, so an upload scalar takes the string primitive (languagePrimitive: string; Primitive String in a catalog row)"
        ));
    }
    // The runtime keys a parsed scalar by its definition's name.
    let key = scalar_key(match text(scalar, "name") {
        Some(declared) if !declared.is_empty() => declared,
        _ => name,
    });
    let mut scalars = Map::new();
    scalars.insert(name.to_owned(), scalar.clone());
    let mut wrapper = Map::new();
    wrapper.insert("scalars".to_owned(), Value::Object(scalars));
    if let Ok(schema) = parse_schema_ir(&Value::Object(wrapper)) {
        if let Some(runtime) = schema.scalars.get(&key) {
            if is_any_json_scalar(runtime) || structured_json_type(runtime).is_some() {
                return None;
            }
        }
    }
    let json_type = scalar
        .get("typeMappings")
        .and_then(|mappings| mappings.get("json_schema"))
        .and_then(Value::as_str);
    let mut rules = Vec::new();
    let mut names = Vec::new();
    if let Some(pattern) = text(scalar, "pattern").filter(|pattern| !pattern.is_empty()) {
        rules.push(format!("pattern {}", json::quote(pattern)));
        names.push("pattern");
    }
    for bound in ["minLength", "maxLength"] {
        if let Some(value) = number(scalar, bound).filter(|value| *value > 0.0) {
            rules.push(format!("{bound} {}", json::js_number(value)));
            names.push(bound);
        }
    }
    if let Some(json_type @ ("object" | "array")) = json_type {
        if !rules.is_empty() {
            return Some(format!(
                "scalar {name} has language primitive object and json_schema type mapping {json_type}, but its {} {} on a string, so the validators check its values as strings: drop the {}",
                crate::errors::join_and(&rules),
                if rules.len() > 1 { "are rules" } else { "is a rule" },
                crate::errors::join_and(&names)
            ));
        }
    }
    Some(format!(
        "scalar {name} has language primitive object but no json_schema type mapping of object, array or any to say which JSON it holds: add typeMappings: {{ json_schema: object }} (or array or any; JSONSchemaType in a catalog row), use the catalog's Generic.JSON for free-form JSON, or model a value with known fields as a nested object type"
    ))
}

/// Why the schema runtime cannot validate a field's type, or `None` when
/// it can.
pub(crate) fn field_type_issue(document: &Value, type_name: &str, field: &Value) -> Option<String> {
    let label = format!("field {type_name}.{}", field_name(field));
    let referenced = self::type_name(field);
    if is_map(field) {
        return Some(format!(
            "{label} is a map, which the schema runtime does not validate"
        ));
    }
    match ref_kind(document, referenced) {
        RefKind::Union => Some(format!(
            "{label} has the union type {referenced}, which the schema runtime does not validate"
        )),
        RefKind::Unknown => Some(format!(
            "{label} has type {referenced}, which is not a primitive, a scalar, or an enum or type of this schema"
        )),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_schema_name_fits_a_path_segment() {
        for name in [
            "Order",
            "order-line",
            "A_1",
            &format!("A{}", "b".repeat(127)),
        ] {
            assert!(is_schema_name(name), "{name}");
        }
        for name in [
            "",
            "1Order",
            "Order Line",
            "Ö",
            &format!("A{}", "b".repeat(128)),
        ] {
            assert!(!is_schema_name(name), "{name}");
        }
        assert_eq!(
            check_schema_name("not a name").unwrap_err().message(),
            "schema name \"not a name\" must match ^[A-Za-z][A-Za-z0-9_-]{0,127}$"
        );
    }

    // ir/testdata/object_scalar_errors.json: the cases the Go IR's
    // ScalarDef.ObjectJSONError tests read too.
    #[test]
    fn an_object_scalars_refusal_is_the_go_loaders_word_for_word() {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../../../ir/testdata/object_scalar_errors.json"
        );
        let corpus: Value = serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
        let cases = corpus["cases"].as_array().unwrap();
        assert!(!cases.is_empty());
        for case in cases {
            let scalar = &case["scalar"];
            let name = scalar["name"].as_str().unwrap();
            assert_eq!(
                object_scalar_issue(name, scalar).as_deref(),
                case["error"].as_str(),
                "{}",
                case["case"]
            );
        }
    }

    #[test]
    fn reachable_types_walk_the_fields() {
        let document: Value = serde_json::from_str(
            r#"{"types": {
                "Order": {"name": "Order", "fields": [{"name": "lines", "typeRef": {"name": "Line", "isArray": true}}]},
                "Line": {"name": "Line", "fields": [{"name": "sku", "typeRef": {"name": "string"}}, {"name": "next", "typeRef": {"name": "Line"}}]},
                "Sketch": {"name": "Sketch"}
            }}"#,
        )
        .unwrap();
        assert_eq!(reachable_types(&document, "Order"), ["Order", "Line"]);
        assert_eq!(ref_kind(&document, "Contact.Email"), RefKind::Scalar);
        assert_eq!(ref_kind(&document, "Line"), RefKind::Type);
        assert_eq!(ref_kind(&document, "Int"), RefKind::Primitive);
        assert_eq!(ref_kind(&document, "Nothing"), RefKind::Unknown);
    }
}
