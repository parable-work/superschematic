//! What a behavior's `parse_config` is told about the type a config is
//! given on (`ConfigTarget` in `src/behaviors/behavior.ts`): its schema,
//! its own fields and their JSON Schemas, the document's other types
//! through a reader that records each type it reads, every behavior the
//! type lists with the fields each declares (D56's `behaviorFields`) and
//! the config of each as the schema holds it, and, when a schema is defined
//! or published, the namespace's other schemas.
//!
//! Each type `parse_config` reads through [`ConfigTarget::types`] counts as
//! reachable from the instance type for the version: the document's field
//! checks cover it and the types it reaches, and the compatibility rule
//! holds a new version to its fields. In Rust the target is borrowed for
//! the one call, so no read can come after it returns.

use std::cell::{OnceCell, RefCell};
use std::collections::BTreeMap;

use serde_json::{Map, Value};

use crate::errors::Result;
use crate::registry::document::{
    array_depth, fields_of, flag, is_map, json_key, ref_kind, type_name, RefKind, SchemaModel,
};
use crate::registry::fields::{field_schemas, scalar_type};

/// A field a behavior of the type declares, as `parse_config` reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigBehaviorField {
    pub name: String,
    /// Whether it can move without an event of its own instance (D56).
    pub volatile: bool,
}

/// What a type's field's type name is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConfigFieldKind {
    Primitive,
    Scalar,
    Enum,
    Type,
}

impl ConfigFieldKind {
    pub fn as_str(self) -> &'static str {
        match self {
            ConfigFieldKind::Primitive => "primitive",
            ConfigFieldKind::Scalar => "scalar",
            ConfigFieldKind::Enum => "enum",
            ConfigFieldKind::Type => "type",
        }
    }
}

/// A field of a type as `parse_config` reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigTypeField {
    /// The key of its value in an object of the type: its jsonTag, else its
    /// name.
    pub key: String,
    /// The name of its type, as the document writes it.
    pub type_name: String,
    pub kind: ConfigFieldKind,
    /// For a scalar, the JSON type of one of its values, as the describe
    /// document writes a field of it (`any` for every JSON type).
    pub json_type: Option<String>,
    /// 0 for a single value, 1 for a list, 2 for a list of lists.
    pub depth: u8,
    /// Whether an object of the type may leave it out.
    pub optional: bool,
}

/// A type of the schema as `parse_config` reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigType {
    pub name: String,
    /// Its fields in the document's order, without a field the document
    /// checks refuse (a map, a union, a type the document lacks).
    pub fields: Vec<ConfigTypeField>,
}

/// Another schema as `parse_config` sees it.
#[derive(Clone, Debug, PartialEq)]
pub struct ConfigSchema {
    pub schema: String,
    pub type_name: String,
    /// The JSON type of each of the type's own fields' values, by JSON key.
    pub fields: Map<String, Value>,
    /// Every behavior the type lists, in order.
    pub behaviors: Vec<String>,
    /// The config of each, as the schema holds it (`{}` when it gives none).
    pub configs: Map<String, Value>,
}

/// The schemas `parse_config` reaches when a schema is defined or
/// published.
pub trait ConfigSchemas {
    /// A schema's live version, looked up in the namespace, then in the
    /// shared one; for the schema's own name, the version being defined or
    /// published. It asks the access policy for read on the schema as the
    /// caller, unless the name is the schema's own: a refusal is forbidden.
    fn get(&self, name: &str) -> Result<Option<ConfigSchema>>;
}

/// The document's types besides the instance type, as `parse_config` reads
/// them; it records each type read.
pub struct ConfigTypes<'a> {
    model: &'a SchemaModel,
    names: Vec<String>,
    read: RefCell<BTreeMap<String, ConfigType>>,
}

impl<'a> ConfigTypes<'a> {
    pub(crate) fn new(model: &'a SchemaModel) -> Self {
        ConfigTypes {
            model,
            names: other_types(model),
            read: RefCell::new(BTreeMap::new()),
        }
    }

    /// Their names, sorted. Listing them reads none.
    pub fn names(&self) -> &[String] {
        &self.names
    }

    /// A type by name, with its fields; `None` for a name that is not a
    /// type of the document besides the instance type. Reading it counts
    /// it, and the types its fields reach, as reachable from the instance
    /// type for the version.
    pub fn get(&self, name: &str) -> Option<ConfigType> {
        if name == self.model.instance_type || ref_kind(&self.model.document, name) != RefKind::Type
        {
            return None;
        }
        let mut read = self.read.borrow_mut();
        Some(
            read.entry(name.to_owned())
                .or_insert_with(|| config_type_of(&self.model.document, name))
                .clone(),
        )
    }

    /// The names read, sorted.
    pub(crate) fn read(&self) -> Vec<String> {
        self.read.borrow().keys().cloned().collect()
    }
}

/// The type a config is given on, for `parse_config`.
pub struct ConfigTarget<'a> {
    pub(crate) model: &'a SchemaModel,
    pub(crate) fields: Vec<String>,
    pub(crate) field_schemas: OnceCell<Map<String, Value>>,
    pub(crate) types: ConfigTypes<'a>,
    pub(crate) behaviors: Vec<String>,
    pub(crate) behavior_fields: Vec<(String, Vec<ConfigBehaviorField>)>,
    pub(crate) configs: Map<String, Value>,
    pub(crate) schemas: Option<&'a dyn ConfigSchemas>,
}

impl<'a> ConfigTarget<'a> {
    /// The schema's name.
    pub fn schema(&self) -> &str {
        &self.model.name
    }

    /// The instance type.
    pub fn type_name(&self) -> &str {
        &self.model.instance_type
    }

    /// The JSON keys of the type's own fields.
    pub fn fields(&self) -> &[String] {
        &self.fields
    }

    /// The JSON Schema of each of the type's own fields, by JSON key, as
    /// the describe document writes an instance's properties (without the
    /// scalar key): a field that is not required also takes null.
    pub fn field_schemas(&self) -> &Map<String, Value> {
        self.field_schemas
            .get_or_init(|| field_schemas(&self.model.document, &self.model.instance_type))
    }

    /// The document's types besides the instance type.
    pub fn types(&self) -> &ConfigTypes<'a> {
        &self.types
    }

    /// Every behavior the type lists, in order.
    pub fn behaviors(&self) -> &[String] {
        &self.behaviors
    }

    /// The fields a behavior the type lists declares, in its declaration's
    /// order (empty for one that declares none); `None` for a behavior with
    /// no implementation registered with this engine.
    pub fn behavior_fields(&self, behavior: &str) -> Option<&[ConfigBehaviorField]> {
        self.behavior_fields
            .iter()
            .find(|(name, _)| name == behavior)
            .map(|(_, fields)| fields.as_slice())
    }

    /// The config of each behavior the type lists, as the schema holds it
    /// (`{}` when it gives none), by name.
    pub fn configs(&self) -> &Map<String, Value> {
        &self.configs
    }

    /// The namespace's other schemas, when the schema is defined or
    /// published; `None` when a published version is composed again.
    pub fn schemas(&self) -> Option<&'a dyn ConfigSchemas> {
        self.schemas
    }
}

/// The document's types besides the instance type, sorted as JavaScript's
/// `sort` orders strings.
pub(crate) fn other_types(model: &SchemaModel) -> Vec<String> {
    let mut names: Vec<String> = model
        .document
        .get("types")
        .and_then(Value::as_object)
        .map(|types| {
            types
                .keys()
                .filter(|name| **name != model.instance_type)
                .cloned()
                .collect()
        })
        .unwrap_or_default();
    names.sort_by(|a, b| crate::json::compare_utf16(a, b));
    names
}

// config_type_of is a type as parse_config reads it, leaving out a field
// the document checks refuse.
fn config_type_of(document: &Value, name: &str) -> ConfigType {
    let mut fields = Vec::new();
    for field in fields_of(document, name) {
        let kind = match ref_kind(document, type_name(field)) {
            RefKind::Primitive => ConfigFieldKind::Primitive,
            RefKind::Scalar => ConfigFieldKind::Scalar,
            RefKind::Enum => ConfigFieldKind::Enum,
            RefKind::Type => ConfigFieldKind::Type,
            RefKind::Union | RefKind::Unknown => continue,
        };
        if is_map(field) {
            continue;
        }
        fields.push(ConfigTypeField {
            key: json_key(field).to_owned(),
            type_name: type_name(field).to_owned(),
            kind,
            json_type: (kind == ConfigFieldKind::Scalar)
                .then(|| scalar_type(document, type_name(field)))
                .flatten(),
            depth: u8::try_from(array_depth(field)).unwrap_or(2),
            optional: !flag(field, "required"),
        });
    }
    ConfigType {
        name: name.to_owned(),
        fields,
    }
}
