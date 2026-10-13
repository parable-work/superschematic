//! The reader of the superschematic Schema IR JSON format
//! (`superschematic build --emit-ir`, or a schema-file document the strict
//! loader returns), the port of the TypeScript runtime's `ir/reader.ts`.
//!
//! The IR is the format-agnostic representation the Go schema loader
//! emits. It keeps the runtime payload's fields (primitive, jsonTag,
//! restMethod, ...) so runtime consumers can be fed from it; this module
//! maps that JSON into the runtime [`Schema`] that validation and parse
//! read. A value of the wrong JSON type where a key expects a string, a
//! number, a boolean or a list reads as that key's zero value; only a
//! definition that is not an object is refused.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::LazyLock;

use serde_json::{Map, Value};

use crate::schema::{
    ArgumentDef, EnumDef, EnumValueDef, FieldDef, FileUploadConfig, ImageConstraints, Import,
    IndexDef, MiddlewareConfig, RelationDef, ScalarDef, Schema, TypeDef, TypeKind, TypeRef,
    UnionDef,
};

/// An IR document the reader refuses.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IrError {
    message: String,
}

impl IrError {
    fn new(message: String) -> Self {
        Self { message }
    }

    pub fn message(&self) -> &str {
        &self.message
    }
}

impl fmt::Display for IrError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for IrError {}

type JsonObject = Map<String, Value>;

const CATALOG: &str = include_str!("builtin_scalars.json");

struct Catalog {
    scalars: BTreeMap<String, ScalarDef>,
    value_classes: BTreeMap<String, String>,
}

/// The builtin catalog, written by internal/tools/scalarcatalog: the rows
/// the TypeScript runtime's builtin-scalars.generated.ts holds, read as an
/// IR document's scalars are.
static BUILTIN: LazyLock<Catalog> = LazyLock::new(|| {
    let catalog: Value = serde_json::from_str(CATALOG).unwrap_or(Value::Null);
    let mut scalars = BTreeMap::new();
    if let Some(rows) = catalog.get("scalars").and_then(Value::as_object) {
        for (name, row) in rows {
            if let Ok(scalar) = parse_ir_scalar(row, &format!("scalars.{name}")) {
                scalars.insert(scalar.name.clone(), scalar);
            }
        }
    }
    let mut value_classes = BTreeMap::new();
    if let Some(classes) = catalog.get("valueClasses").and_then(Value::as_object) {
        for (name, class) in classes {
            if let Some(class) = class.as_str() {
                value_classes.insert(name.clone(), class.to_owned());
            }
        }
    }
    Catalog {
        scalars,
        value_classes,
    }
});

/// The builtin scalar catalog, keyed by each scalar's legacy underscore
/// name (`Contact_Email`): one row per scalar in the superscalar commit
/// this repository pins.
pub fn builtin_scalars() -> &'static BTreeMap<String, ScalarDef> {
    &BUILTIN.scalars
}

/// The value class (D19) the graph descriptor gives a single field of each
/// builtin scalar, keyed as [`builtin_scalars`] is: the class of one value
/// of it stored in a column of its own. A scalar no class reads is absent.
pub fn builtin_scalar_value_classes() -> &'static BTreeMap<String, String> {
    &BUILTIN.value_classes
}

fn as_object<'a>(value: &'a Value, path: &str) -> Result<&'a JsonObject, IrError> {
    value.as_object().ok_or_else(|| {
        IrError::new(format!(
            "runtime schema IR parse error: expected object at {path}"
        ))
    })
}

fn as_string(value: Option<&Value>) -> String {
    value.and_then(Value::as_str).unwrap_or_default().to_owned()
}

fn as_boolean(value: Option<&Value>) -> bool {
    value.and_then(Value::as_bool).unwrap_or(false)
}

fn as_number_or_null(value: Option<&Value>) -> Option<f64> {
    value
        .and_then(Value::as_number)
        .and_then(serde_json::Number::as_f64)
        .filter(|n| n.is_finite())
}

fn as_number(value: Option<&Value>) -> f64 {
    as_number_or_null(value).unwrap_or(0.0)
}

fn as_string_or_null(value: Option<&Value>) -> Option<String> {
    value.and_then(Value::as_str).map(str::to_owned)
}

fn as_string_array(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

fn as_list(value: Option<&Value>) -> &[Value] {
    value.and_then(Value::as_array).map_or(&[], Vec::as_slice)
}

fn or_else(first: String, second: Option<&Value>) -> String {
    if first.is_empty() {
        as_string(second)
    } else {
        first
    }
}

fn non_empty(value: String) -> Option<String> {
    if value.is_empty() {
        None
    } else {
        Some(value)
    }
}

fn doc_text(definition: &JsonObject) -> String {
    or_else(
        as_string(definition.get("description")),
        definition.get("comment"),
    )
}

/// The IR names scalars with dot namespaces (`Identity.UserID`); the
/// runtime schema uses the legacy underscore form (`Identity_UserID`).
/// Type and enum names never contain dots, so the replacement leaves them
/// as they are.
fn normalize_type_name(name: &str) -> String {
    name.replace('.', "_")
}

fn parse_ir_type_ref(raw: Option<&Value>, path: &str) -> Result<TypeRef, IrError> {
    let type_ref = as_object(raw.unwrap_or(&Value::Null), path)?;
    let parsed = TypeRef {
        name: normalize_type_name(&as_string(type_ref.get("name"))),
        is_array: as_boolean(type_ref.get("isArray")),
        is_array_of_arrays: as_boolean(type_ref.get("isArrayOfArrays")),
        elem_non_null: as_boolean(type_ref.get("elemNonNull")),
    };
    if parsed.is_array_of_arrays && !parsed.is_array {
        return Err(IrError::new(format!(
            "runtime schema IR parse error: isArrayOfArrays requires isArray at {path}"
        )));
    }
    Ok(parsed)
}

fn parse_ir_relation(raw: Option<&Value>) -> Option<RelationDef> {
    let relation = raw?.as_object()?;
    Some(RelationDef {
        type_name: as_string(relation.get("type")),
        field: as_string(relation.get("field")),
    })
}

fn parse_ir_middleware(raw: Option<&Value>) -> Option<MiddlewareConfig> {
    let middleware = raw?.as_object()?;
    Some(MiddlewareConfig {
        rate_limit: as_number_or_null(middleware.get("rateLimit")),
        body_limit: as_number_or_null(middleware.get("bodyLimit")),
        timeout: as_number_or_null(middleware.get("timeout")),
        encrypted: as_boolean(middleware.get("encrypted")),
    })
}

fn parse_ir_argument(raw: &Value, path: &str) -> Result<ArgumentDef, IrError> {
    let argument = as_object(raw, path)?;
    Ok(ArgumentDef {
        name: as_string(argument.get("name")),
        description: doc_text(argument),
        type_ref: parse_ir_type_ref(argument.get("typeRef"), &format!("{path}.typeRef"))?,
        required: as_boolean(argument.get("required")),
        default_value: as_string_or_null(argument.get("default")),
        is_query: as_boolean(argument.get("isQuery")),
    })
}

fn parse_ir_field(raw: &Value, path: &str) -> Result<FieldDef, IrError> {
    let field = as_object(raw, path)?;
    let name = as_string(field.get("name"));
    let arguments = as_list(field.get("arguments"))
        .iter()
        .enumerate()
        .map(|(index, argument)| parse_ir_argument(argument, &format!("{path}.arguments[{index}]")))
        .collect::<Result<Vec<_>, _>>()?;
    Ok(FieldDef {
        description: doc_text(field),
        // The wire form omits empty metadata (Go omitempty); mirror that
        // absence.
        title: non_empty(as_string(field.get("title"))),
        purpose: non_empty(as_string(field.get("purpose"))),
        icon: non_empty(as_string(field.get("icon"))),
        placeholder: non_empty(as_string(field.get("placeholder"))),
        json_key: non_empty(as_string(field.get("jsonTag"))).unwrap_or_else(|| name.clone()),
        required: as_boolean(field.get("required")),
        auto_generated: as_boolean(field.get("autoGenerated")),
        default_value: as_string_or_null(field.get("default")),
        validate_min: as_number_or_null(field.get("validateMin")),
        validate_max: as_number_or_null(field.get("validateMax")),
        validate_min_length: as_number_or_null(field.get("validateMinLength")),
        validate_max_length: as_number_or_null(field.get("validateMaxLength")),
        validate_list_min: as_number_or_null(field.get("validateListMin")),
        validate_list_max: as_number_or_null(field.get("validateListMax")),
        validate_pattern: as_string(field.get("validatePattern")),
        arguments,
        key: as_boolean(field.get("key")),
        unique: as_boolean(field.get("unique")),
        search_field: as_boolean(field.get("searchField")),
        relation: parse_ir_relation(field.get("relation")),
        has_many: as_boolean(field.get("hasMany")),
        many_to_many: as_boolean(field.get("manyToMany")),
        json_field: as_boolean(field.get("jsonField")),
        secret: as_boolean(field.get("secret")),
        ui_hidden: as_boolean(field.get("uiHidden")),
        temporal_format: as_string(field.get("temporalFormat")),
        internal_metadata: as_boolean(field.get("internalMetadata")),
        auth: as_boolean(field.get("auth")),
        encrypted: as_boolean(field.get("encrypted")),
        require_ownership: as_boolean(field.get("requireOwnership")),
        permissions: as_string_array(field.get("permissions")),
        rest_method: or_else(as_string(field.get("restMethod")), field.get("httpMethod")),
        param_type: as_string(field.get("paramType")),
        middleware: parse_ir_middleware(field.get("middleware")),
        type_ref: parse_ir_type_ref(field.get("typeRef"), &format!("{path}.typeRef"))?,
        name,
    })
}

fn parse_ir_indexes(raw: Option<&Value>) -> Vec<IndexDef> {
    as_list(raw)
        .iter()
        .filter_map(Value::as_object)
        .map(|index| IndexDef {
            keys: as_string_array(index.get("keys")),
            unique: as_boolean(index.get("unique")),
        })
        .collect()
}

fn parse_ir_type(name: &str, raw: &Value, path: &str) -> Result<TypeDef, IrError> {
    let definition = as_object(raw, path)?;
    let role = or_else(as_string(definition.get("role")), definition.get("kind"));
    let fields = as_list(definition.get("fields"))
        .iter()
        .enumerate()
        .map(|(index, field)| parse_ir_field(field, &format!("{path}.fields[{index}]")))
        .collect::<Result<Vec<_>, _>>()?;
    Ok(TypeDef {
        name: name.to_owned(),
        description: doc_text(definition),
        kind: if role == "APIInput" {
            TypeKind::Input
        } else {
            TypeKind::Object
        },
        fields,
        indexes: parse_ir_indexes(definition.get("indexes")),
        env_vars: as_boolean(definition.get("envVars")),
    })
}

fn parse_ir_enum(name: &str, raw: &Value, path: &str) -> Result<EnumDef, IrError> {
    let definition = as_object(raw, path)?;
    let values = as_list(definition.get("values"))
        .iter()
        .enumerate()
        .map(|(index, value)| {
            let value = as_object(value, &format!("{path}.values[{index}]"))?;
            Ok(EnumValueDef {
                name: as_string(value.get("name")),
                description: doc_text(value),
                serialized_as: as_string(value.get("serializedAs")),
            })
        })
        .collect::<Result<Vec<_>, IrError>>()?;
    Ok(EnumDef {
        name: name.to_owned(),
        owner: as_string(definition.get("owner")),
        description: doc_text(definition),
        values,
    })
}

fn parse_ir_file_upload(raw: Option<&Value>) -> Option<FileUploadConfig> {
    let upload = raw?.as_object()?;
    Some(FileUploadConfig {
        max_size: as_number(upload.get("maxSize")),
        allowed_types: as_string_array(upload.get("allowedTypes")),
        category: as_string(upload.get("category")),
    })
}

fn parse_ir_image_constraints(raw: Option<&Value>) -> Option<ImageConstraints> {
    let constraints = raw?.as_object()?;
    Some(ImageConstraints {
        max_width: as_number(constraints.get("maxWidth")),
        max_height: as_number(constraints.get("maxHeight")),
        min_aspect_ratio: as_number_or_null(constraints.get("minAspectRatio")),
        max_aspect_ratio: as_number_or_null(constraints.get("maxAspectRatio")),
        require_transparency: as_boolean(constraints.get("requireTransparency")),
    })
}

fn map_language_primitive(language_primitive: &str) -> &'static str {
    match language_primitive {
        "number" => "Float",
        "boolean" => "Boolean",
        "object" => "JSON",
        _ => "String",
    }
}

fn parse_ir_scalar(raw: &Value, path: &str) -> Result<ScalarDef, IrError> {
    let definition = as_object(raw, path)?;
    let mut type_mappings = BTreeMap::new();
    if let Some(mappings) = definition.get("typeMappings").and_then(Value::as_object) {
        for (language, mapped) in mappings {
            if let Some(mapped) = mapped.as_str() {
                type_mappings.insert(language.clone(), mapped.to_owned());
            }
        }
    }
    let primitive = non_empty(as_string(definition.get("primitive"))).unwrap_or_else(|| {
        map_language_primitive(&as_string(definition.get("languagePrimitive"))).to_owned()
    });
    Ok(ScalarDef {
        name: normalize_type_name(&as_string(definition.get("name"))),
        description: doc_text(definition),
        primitive,
        min_length: as_number(definition.get("minLength")),
        max_length: as_number(definition.get("maxLength")),
        pattern: as_string(definition.get("pattern")),
        format: as_string(definition.get("format")),
        reserved_words: as_string_array(definition.get("reservedWords")),
        case_insensitive: as_boolean(definition.get("caseInsensitive")),
        reserved_words_case_insensitive: as_boolean(definition.get("reservedWordsCaseInsensitive")),
        reserved_words_match_partial: as_boolean(definition.get("reservedWordsMatchPartial")),
        minimum: as_number_or_null(definition.get("minimum")),
        maximum: as_number_or_null(definition.get("maximum")),
        example: as_string(definition.get("example")),
        file_upload: parse_ir_file_upload(definition.get("fileUpload")),
        image_constraints: parse_ir_image_constraints(definition.get("imageConstraints")),
        has_custom_normalize: as_boolean(definition.get("hasCustomNormalize")),
        has_custom_validate: as_boolean(definition.get("hasCustomValidate")),
        has_custom_parse: as_boolean(definition.get("hasCustomParse")),
        type_mappings,
    })
}

fn parse_ir_union(name: &str, raw: &Value, path: &str) -> Result<UnionDef, IrError> {
    let definition = as_object(raw, path)?;
    Ok(UnionDef {
        name: name.to_owned(),
        description: doc_text(definition),
        types: as_string_array(definition.get("types")),
    })
}

fn parse_ir_imports(raw: Option<&Value>) -> Vec<Import> {
    as_list(raw)
        .iter()
        .filter_map(Value::as_object)
        .map(|import| Import {
            from: or_else(as_string(import.get("from")), import.get("package")),
            types: as_string_array(import.get("types")),
        })
        .collect()
}

fn entries<'a>(root: &'a JsonObject, key: &str) -> impl Iterator<Item = (&'a String, &'a Value)> {
    root.get(key)
        .and_then(Value::as_object)
        .into_iter()
        .flatten()
}

/// Reads a superschematic Schema IR JSON document (the `superschematic
/// build --emit-ir` output, several of them merged map-wise, or a
/// schema-file document) into the runtime [`Schema`].
///
/// Types are split into `types` and `inputs` by their IR role: `APIInput`
/// becomes an input; every other role (`APIView`, `EmbeddedStruct`,
/// `DBTable`, ...) becomes an object type. Namespaced scalar names take
/// their legacy underscore form, so they line up with the builtin catalog,
/// whose scalars the result holds beside the document's.
pub fn parse_schema_ir(schema: &Value) -> Result<Schema, IrError> {
    let root = as_object(schema, "root")?;
    let mut parsed = Schema {
        name: non_empty(as_string(root.get("name"))),
        kind: non_empty(as_string(root.get("kind"))),
        description: non_empty(as_string(root.get("description"))),
        root_type: non_empty(as_string(root.get("rootType"))),
        imports: parse_ir_imports(root.get("imports")),
        scalars: builtin_scalars().clone(),
        ..Schema::default()
    };

    for (name, definition) in entries(root, "scalars") {
        let scalar = parse_ir_scalar(definition, &format!("scalars.{name}"))?;
        parsed.scalars.insert(scalar.name.clone(), scalar);
    }

    for (name, definition) in entries(root, "types") {
        let type_def = parse_ir_type(name, definition, &format!("types.{name}"))?;
        match type_def.kind {
            TypeKind::Input => parsed.inputs.insert(name.clone(), type_def),
            TypeKind::Object => parsed.types.insert(name.clone(), type_def),
        };
    }

    // Legacy payloads may carry a pre-split inputs map; honor it when
    // present.
    for (name, definition) in entries(root, "inputs") {
        let mut input = parse_ir_type(name, definition, &format!("inputs.{name}"))?;
        input.kind = TypeKind::Input;
        parsed.inputs.insert(name.clone(), input);
    }

    for (name, definition) in entries(root, "enums") {
        let enum_def = parse_ir_enum(name, definition, &format!("enums.{name}"))?;
        parsed.enums.insert(name.clone(), enum_def);
    }

    for (name, definition) in entries(root, "unions") {
        let union_def = parse_ir_union(name, definition, &format!("unions.{name}"))?;
        parsed.unions.insert(name.clone(), union_def);
    }

    Ok(parsed)
}

/// Reads IR JSON text, as [`parse_schema_ir`] reads the parsed document.
pub fn parse_schema_ir_json(text: &str) -> Result<Schema, IrError> {
    let parsed: Value = serde_json::from_str(text).map_err(|error| {
        IrError::new(format!(
            "runtime schema IR parse error: invalid JSON: {error}"
        ))
    })?;
    parse_schema_ir(&parsed)
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn the_catalog_holds_every_builtin_scalar() {
        let scalars = builtin_scalars();
        let email = &scalars["Contact_Email"];
        assert_eq!(email.name, "Contact_Email");
        assert_eq!(email.primitive, "String");
        assert_eq!(email.max_length, 255.0);
        assert!(email.has_custom_normalize);
        assert_eq!(
            email.type_mappings.get("sql").map(String::as_str),
            Some("CITEXT")
        );
        assert_eq!(scalars["Generic_Int64"].maximum, Some(9007199254740991.0));
        assert_eq!(
            builtin_scalar_value_classes()
                .get("Temporal_DateTime")
                .map(String::as_str),
            Some("dateTime")
        );
        assert!(!builtin_scalar_value_classes().contains_key("Geo_Location"));
    }

    #[test]
    fn splits_types_and_inputs_and_keeps_document_scalars() {
        let schema = parse_schema_ir(&json!({
            "name": "shop",
            "kind": "General",
            "scalars": {"Acme.Sku": {"name": "Acme.Sku", "languagePrimitive": "string", "pattern": "^[A-Z]+$"}},
            "types": {
                "Order": {"name": "Order", "role": "DBTable", "fields": [
                    {"name": "sku", "jsonTag": "skuCode", "typeRef": {"name": "Acme.Sku", "isArray": true}, "required": true, "validateListMin": 1}
                ]},
                "OrderInput": {"name": "OrderInput", "role": "APIInput", "fields": []}
            },
            "enums": {"Shade": {"name": "Shade", "values": [{"name": "LIGHT", "serializedAs": "light"}]}}
        }))
        .unwrap();
        assert_eq!(schema.name.as_deref(), Some("shop"));
        let order = &schema.types["Order"];
        assert_eq!(order.kind, TypeKind::Object);
        let sku = &order.fields[0];
        assert_eq!(sku.key(), "skuCode");
        assert_eq!(sku.type_ref.name, "Acme_Sku");
        assert!(sku.type_ref.is_array);
        assert_eq!(sku.validate_list_min, Some(1.0));
        assert_eq!(schema.inputs["OrderInput"].kind, TypeKind::Input);
        assert_eq!(schema.scalars["Acme_Sku"].pattern, "^[A-Z]+$");
        assert!(schema.scalars.contains_key("Contact_Email"));
        assert_eq!(schema.enums["Shade"].values[0].serialized_as, "light");
    }

    #[test]
    fn refuses_a_definition_that_is_not_an_object() {
        let error = parse_schema_ir(&json!({"types": {"Order": []}})).unwrap_err();
        assert_eq!(
            error.to_string(),
            "runtime schema IR parse error: expected object at types.Order"
        );
        let error = parse_schema_ir(&json!({"types": {"Order": {"fields": [
            {"name": "grid", "typeRef": {"name": "String", "isArrayOfArrays": true}}
        ]}}}))
        .unwrap_err();
        assert_eq!(
            error.to_string(),
            "runtime schema IR parse error: isArrayOfArrays requires isArray at types.Order.fields[0].typeRef"
        );
        assert!(parse_schema_ir(&json!([])).is_err());
        assert!(parse_schema_ir_json("{").is_err());
    }
}
