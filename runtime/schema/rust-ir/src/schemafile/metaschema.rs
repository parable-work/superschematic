//! What the schema-file loader reads from a meta-schema: the output of
//! `superschematic json-schema`, the JSON Schema a binary's registry builds
//! for schema files. Everything a registry decides comes from it: the
//! schema kinds, the single-definition file forms and their discriminators,
//! the document's collection keys, and the invocation policy's key and
//! default. A deployment's meta-schema may add kinds, extension slots,
//! documents, behaviors and another policy, and the loader reads it the
//! same way.

use std::fmt;

use serde_json::{Map, Value};

const DOCUMENT: &str = "Document";
const MCP_RECORD: &str = "OperationMCP";
const DEFS_PREFIX: &str = "#/$defs/";

/// A meta-schema the loader cannot read its rules from.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MetaSchemaError {
    message: String,
}

impl MetaSchemaError {
    pub(crate) fn new(message: impl Into<String>) -> Self {
        Self {
            message: format!("schema-file meta-schema: {}", message.into()),
        }
    }

    pub fn message(&self) -> &str {
        &self.message
    }
}

impl fmt::Display for MetaSchemaError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for MetaSchemaError {}

/// Where a single definition goes in a document: under its name in a map
/// collection, or as the one entry of a list.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Collection {
    pub(crate) key: String,
    pub(crate) list: bool,
}

/// A file form: the $defs entry a payload validates against and where its
/// definition goes in the document. The document form has no collection.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct FileForm {
    pub(crate) def: String,
    pub(crate) collection: Option<Collection>,
}

/// The invocation policy of the registry that wrote the meta-schema.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct InvocationPolicy {
    pub(crate) def: String,
    pub(crate) key: String,
    pub(crate) default: String,
}

/// The dispatch rules and definitions one meta-schema declares.
#[derive(Debug, Clone)]
pub(crate) struct MetaSchemaRules {
    pub(crate) defs: Map<String, Value>,
    pub(crate) document: FileForm,
    /// The schema kinds, in the meta-schema's order.
    pub(crate) schema_kinds: Vec<String>,
    /// Single-definition forms with the value of their kind discriminator,
    /// in the root oneOf's order.
    pub(crate) by_kind: Vec<(String, FileForm)>,
    /// The form without a kind discriminator and the key that marks it.
    pub(crate) by_discriminator: (String, FileForm),
    /// Keys that make a payload without a discriminator a document.
    pub(crate) collection_keys: Vec<String>,
    pub(crate) policy: InvocationPolicy,
}

impl MetaSchemaRules {
    /// Every form a payload can take: the document, the form without a kind
    /// and the forms with one.
    pub(crate) fn forms(&self) -> impl Iterator<Item = &FileForm> {
        std::iter::once(&self.document)
            .chain(std::iter::once(&self.by_discriminator.1))
            .chain(self.by_kind.iter().map(|(_, form)| form))
    }

    /// The properties `$defs/<def>` declares.
    pub(crate) fn properties(&self, def: &str) -> Option<&Map<String, Value>> {
        self.defs.get(def)?.get("properties")?.as_object()
    }
}

/// The definition name a `$ref` to `$defs` names.
pub(crate) fn ref_name(node: Option<&Value>) -> Option<&str> {
    node?.get("$ref")?.as_str()?.strip_prefix(DEFS_PREFIX)
}

fn fail(message: impl Into<String>) -> MetaSchemaError {
    MetaSchemaError::new(message)
}

fn empty_object() -> Value {
    Value::Object(Map::new())
}

/// The keys of a `required` list.
fn required_of(def: &Value) -> Vec<&str> {
    def.get("required")
        .and_then(Value::as_array)
        .map(|keys| keys.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default()
}

/// Derives the loader's rules from a meta-schema. It refuses one that lacks
/// a part the rules need, such as the invocation policy's default, which
/// meta-schemas from before that default was declared lack.
pub(crate) fn read_meta_schema(meta_schema: &Value) -> Result<MetaSchemaRules, MetaSchemaError> {
    let defs = meta_schema
        .get("$defs")
        .and_then(Value::as_object)
        .ok_or_else(|| fail("no $defs"))?;
    let document_props = defs
        .get(DOCUMENT)
        .and_then(|def| def.get("properties"))
        .and_then(Value::as_object)
        .ok_or_else(|| fail(format!("no $defs/{DOCUMENT} with properties")))?;
    let variants = meta_schema
        .get("oneOf")
        .and_then(Value::as_array)
        .ok_or_else(|| fail("no root oneOf"))?;

    // A collection is a Document property holding definitions: a map keyed
    // by definition name, or a list.
    let collection_of = |def: &str| -> Result<Collection, MetaSchemaError> {
        let matches: Vec<Collection> = document_props
            .iter()
            .filter(|(_, prop)| {
                ref_name(prop.get("additionalProperties")) == Some(def)
                    || ref_name(prop.get("items")) == Some(def)
            })
            .map(|(key, prop)| Collection {
                key: key.clone(),
                list: prop.get("type").and_then(Value::as_str) == Some("array"),
            })
            .collect();
        match <[Collection; 1]>::try_from(matches) {
            Ok([only]) => Ok(only),
            Err(matches) => Err(fail(format!(
                "expected one {DOCUMENT} property holding $defs/{def}, found {}",
                matches.len()
            ))),
        }
    };

    // A single-definition form with a kind discriminator is a copy of a
    // definition with a required const "kind"; find the definition it
    // copies.
    let base_of = |file_def: &str| -> Result<String, MetaSchemaError> {
        let file = &defs[file_def];
        let mut props = file
            .get("properties")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        props.remove("kind");
        let props = Value::Object(props);
        let required: Vec<Value> = required_of(file)
            .into_iter()
            .filter(|key| *key != "kind")
            .map(|key| Value::String(key.to_owned()))
            .collect();
        let required = Value::Array(required);
        let matches: Vec<&String> = defs
            .iter()
            .filter(|(name, def)| {
                name.as_str() != file_def
                    && *def.get("properties").unwrap_or(&empty_object()) == props
                    && *def.get("required").unwrap_or(&Value::Array(Vec::new())) == required
            })
            .map(|(name, _)| name)
            .collect();
        match matches.as_slice() {
            [only] => Ok((*only).clone()),
            _ => Err(fail(format!(
                "expected one definition $defs/{file_def} copies, found {}",
                matches.len()
            ))),
        }
    };

    let mut by_kind = Vec::new();
    let mut without_kind = Vec::new();
    for variant in variants {
        let name = ref_name(Some(variant))
            .filter(|name| defs.contains_key(*name))
            .ok_or_else(|| fail("a root oneOf entry is not a $ref to $defs"))?;
        if name == DOCUMENT {
            continue;
        }
        let kind = defs[name]
            .get("properties")
            .and_then(|props| props.get("kind"))
            .and_then(|kind| kind.get("const"))
            .and_then(Value::as_str);
        match kind {
            Some(kind) => {
                let collection = collection_of(&base_of(name)?)?;
                by_kind.push((
                    kind.to_owned(),
                    FileForm {
                        def: name.to_owned(),
                        collection: Some(collection),
                    },
                ));
            }
            None => without_kind.push(name),
        }
    }
    let [type_form] = without_kind.as_slice() else {
        return Err(fail(format!(
            "expected one single-definition form without a kind discriminator, found {}",
            without_kind.len()
        )));
    };
    // That form is marked by the one key it requires that a document does
    // not have.
    let markers: Vec<&str> = required_of(&defs[*type_form])
        .into_iter()
        .filter(|key| !document_props.contains_key(*key))
        .collect();
    let [marker] = markers.as_slice() else {
        return Err(fail(format!(
            "expected one key marking $defs/{type_form}, found {}",
            markers.len()
        )));
    };

    let schema_kinds = document_props
        .get("kind")
        .and_then(|kind| kind.get("enum"))
        .and_then(Value::as_array)
        .ok_or_else(|| fail(format!("$defs/{DOCUMENT}/properties/kind has no enum")))?
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();

    // The invocation policy is the mcp record's one property whose default
    // is one of its values; the other defaults are the zero values Go omits.
    let mcp_props = defs
        .get(MCP_RECORD)
        .and_then(|def| def.get("properties"))
        .and_then(Value::as_object);
    let policies: Vec<(&String, &str)> = mcp_props
        .into_iter()
        .flatten()
        .filter_map(|(key, prop)| {
            let default = prop.get("default")?.as_str()?;
            let values = prop.get("enum")?.as_array()?;
            values
                .iter()
                .any(|value| value.as_str() == Some(default))
                .then_some((key, default))
        })
        .collect();
    let [(policy_key, policy_default)] = policies.as_slice() else {
        return Err(fail(format!(
            "expected one invocation policy with a default on $defs/{MCP_RECORD}, found {}",
            policies.len()
        )));
    };

    let collection_keys = document_props
        .iter()
        .filter(|(_, prop)| {
            matches!(
                prop.get("type").and_then(Value::as_str),
                Some("object" | "array")
            )
        })
        .map(|(key, _)| key.clone())
        .collect();

    Ok(MetaSchemaRules {
        document: FileForm {
            def: DOCUMENT.to_owned(),
            collection: None,
        },
        schema_kinds,
        by_kind,
        by_discriminator: (
            (*marker).to_owned(),
            FileForm {
                def: (*type_form).to_owned(),
                collection: Some(collection_of(type_form)?),
            },
        ),
        collection_keys,
        policy: InvocationPolicy {
            def: MCP_RECORD.to_owned(),
            key: (*policy_key).clone(),
            default: (*policy_default).to_owned(),
        },
        defs: defs.clone(),
    })
}
