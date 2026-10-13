//! The runtime schema: what the IR reader makes of an IR document, and what
//! validation and parse read. It is the TypeScript runtime's `Schema` shape
//! (`@superschematic/schema-ir`'s index.d.ts), field for field, with the
//! types and inputs split by role and scalar names in their legacy
//! underscore form (`Contact_Email`).

use std::collections::BTreeMap;

/// One schema, read from an IR document.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Schema {
    pub name: Option<String>,
    pub kind: Option<String>,
    pub description: Option<String>,
    pub root_type: Option<String>,
    pub imports: Vec<Import>,
    /// The builtin catalog's scalars, then the document's, which replace a
    /// builtin of the same name.
    pub scalars: BTreeMap<String, ScalarDef>,
    pub enums: BTreeMap<String, EnumDef>,
    /// Every type whose role is not `APIInput`.
    pub types: BTreeMap<String, TypeDef>,
    /// The `APIInput` types.
    pub inputs: BTreeMap<String, TypeDef>,
    pub unions: BTreeMap<String, UnionDef>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Import {
    pub from: String,
    pub types: Vec<String>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct TypeRef {
    pub name: String,
    pub is_array: bool,
    /// `T[][]`: each element of the outer list is a list of `name`. Only
    /// with `is_array`.
    pub is_array_of_arrays: bool,
    pub elem_non_null: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct MiddlewareConfig {
    pub rate_limit: Option<f64>,
    pub body_limit: Option<f64>,
    pub timeout: Option<f64>,
    pub encrypted: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct RelationDef {
    pub type_name: String,
    pub field: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct IndexDef {
    pub keys: Vec<String>,
    pub unique: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct ArgumentDef {
    pub name: String,
    pub description: String,
    pub type_ref: TypeRef,
    pub required: bool,
    pub default_value: Option<String>,
    pub is_query: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct FieldDef {
    pub name: String,
    pub description: String,
    /// The human-facing label; `None` derives one from the name.
    pub title: Option<String>,
    pub purpose: Option<String>,
    pub icon: Option<String>,
    pub placeholder: Option<String>,
    /// The key the field's value sits under in a JSON object.
    pub json_key: String,
    pub required: bool,
    pub auto_generated: bool,
    pub default_value: Option<String>,
    pub validate_min: Option<f64>,
    pub validate_max: Option<f64>,
    pub validate_min_length: Option<f64>,
    pub validate_max_length: Option<f64>,
    pub validate_list_min: Option<f64>,
    pub validate_list_max: Option<f64>,
    pub validate_pattern: String,
    pub arguments: Vec<ArgumentDef>,
    pub key: bool,
    pub unique: bool,
    pub search_field: bool,
    pub relation: Option<RelationDef>,
    pub has_many: bool,
    pub many_to_many: bool,
    pub json_field: bool,
    pub secret: bool,
    pub ui_hidden: bool,
    pub temporal_format: String,
    pub internal_metadata: bool,
    pub auth: bool,
    pub encrypted: bool,
    pub require_ownership: bool,
    pub permissions: Vec<String>,
    pub rest_method: String,
    pub param_type: String,
    pub middleware: Option<MiddlewareConfig>,
    pub type_ref: TypeRef,
}

impl FieldDef {
    /// The key the field's value sits under: its JSON key, else its name.
    pub fn key(&self) -> &str {
        if self.json_key.is_empty() {
            &self.name
        } else {
            &self.json_key
        }
    }
}

/// Whether a type is read as an object type or an input.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum TypeKind {
    #[default]
    Object,
    Input,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct TypeDef {
    pub name: String,
    pub description: String,
    pub kind: TypeKind,
    pub fields: Vec<FieldDef>,
    pub indexes: Vec<IndexDef>,
    pub env_vars: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct EnumValueDef {
    pub name: String,
    pub description: String,
    pub serialized_as: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct EnumDef {
    pub name: String,
    pub owner: String,
    pub description: String,
    pub values: Vec<EnumValueDef>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct FileUploadConfig {
    pub max_size: f64,
    pub allowed_types: Vec<String>,
    pub category: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct ImageConstraints {
    pub max_width: f64,
    pub max_height: f64,
    pub min_aspect_ratio: Option<f64>,
    pub max_aspect_ratio: Option<f64>,
    pub require_transparency: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct ScalarDef {
    /// The legacy underscore form of the scalar's name (`Contact_Email`).
    pub name: String,
    pub description: String,
    /// `String`, `Int`, `Float`, `Boolean`, `JSON`, `Type` or `Bytes`.
    pub primitive: String,
    /// Zero when the scalar sets no bound.
    pub min_length: f64,
    pub max_length: f64,
    pub pattern: String,
    pub format: String,
    pub reserved_words: Vec<String>,
    pub case_insensitive: bool,
    pub reserved_words_case_insensitive: bool,
    pub reserved_words_match_partial: bool,
    pub minimum: Option<f64>,
    pub maximum: Option<f64>,
    pub example: String,
    pub file_upload: Option<FileUploadConfig>,
    pub image_constraints: Option<ImageConstraints>,
    pub has_custom_normalize: bool,
    pub has_custom_validate: bool,
    pub has_custom_parse: bool,
    /// Each language's type for the scalar, keyed by language (`go`,
    /// `json_schema`, `sql`, ...).
    pub type_mappings: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct UnionDef {
    pub name: String,
    pub description: String,
    pub types: Vec<String>,
}

/// The JSON a structured scalar holds (see [`structured_json_type`]).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum JsonShape {
    Object,
    Array,
}

impl JsonShape {
    pub fn as_str(self) -> &'static str {
        match self {
            JsonShape::Object => "object",
            JsonShape::Array => "array",
        }
    }
}

/// Whether a scalar's value is any JSON value, which its json_schema type
/// mapping declares as `any` (Generic_JSON in the core catalog). The
/// catalog gives such a scalar the String primitive, but an object, an
/// array, a string, a number and a boolean are all values, and only null
/// stands for a missing one. Parse and validation key the rule off this
/// mapping, not the scalar's name or primitive.
pub fn is_any_json_scalar(scalar: &ScalarDef) -> bool {
    scalar.type_mappings.get("json_schema").map(String::as_str) == Some("any")
}

/// The JSON shape a scalar's value has when its json_schema type mapping
/// is `object` or `array` (Generic_StringMap, Geo_Location and
/// Embedding_Vector in the core catalog), and `None` otherwise. The catalog
/// gives such a scalar the String primitive, but every generated type holds
/// the object or the array. A scalar that also declares a pattern or a
/// length, which are rules on a string, contradicts itself and is not
/// structured: it keeps the String primitive's checks until its metadata
/// agrees.
pub fn structured_json_type(scalar: &ScalarDef) -> Option<JsonShape> {
    if !scalar.pattern.is_empty() || scalar.min_length > 0.0 || scalar.max_length > 0.0 {
        return None;
    }
    match scalar.type_mappings.get("json_schema").map(String::as_str) {
        Some("object") => Some(JsonShape::Object),
        Some("array") => Some(JsonShape::Array),
        _ => None,
    }
}
