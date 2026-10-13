//! The strict schema-file loader (the `schemafile` feature), the port of
//! the TypeScript runtime's `schemafile/loader.ts`, itself the twin of the
//! Go data-form reader (internal/loader/jsonreader over
//! schemafile.DecodeWith). It does what that reader does and no more. It
//! dispatches a JSON payload to its file form, validates it against that
//! form's definition in a meta-schema, decodes it as Go decodes it into
//! the IR types, puts a single definition into a document, drops empty
//! extension entries, and fills in the registry's invocation policy
//! default. The canonical form of the result is byte for byte what
//! `ir.CanonicalJSON` writes for the document Go decodes;
//! runtime/schema/testdata/schema_file_parity.json holds the vectors.
//!
//! The meta-schema is checked with the jsonschema crate (JSON Schema
//! 2020-12, formats as annotations), as ajv checks it in the TypeScript
//! loader, and the issues are written as that loader writes them: ajv's
//! message for each keyword, an `if` branch's own failure beside the errors
//! inside it, an unknown key or a value outside an enum in the loader's
//! words, and a JSON syntax error as Node 24's `JSON.parse` words it. The
//! issues come in a defined order, not the order either validator visits a
//! schema's keywords in: by JSON pointer, then by the ajv keyword that
//! failed, then by message, each compared by code point, as the TypeScript
//! loader sorts them (D58), so both give the same list.
//!
//! ```
//! use superschematic_schema_ir::schemafile::{load_schema_file, SchemaFileLoader};
//!
//! let loaded = load_schema_file(r#"{"name": "Item", "role": "EmbeddedStruct"}"#, "item.schema.json").unwrap();
//! assert_eq!(loaded.canonical, r#"{"types":{"Item":{"name":"Item","role":"EmbeddedStruct"}}}"#);
//!
//! let refused = SchemaFileLoader::new().load(r#"{"name": "Item"}"#, "item.schema.json").unwrap_err();
//! assert_eq!(refused.source(), "item.schema.json");
//! ```

mod json;
mod metaschema;

use std::collections::HashMap;
use std::fmt;
use std::sync::LazyLock;

use jsonschema::Validator;
use serde_json::{Map, Value};

pub use json::{canonical_json, JsonSyntaxError};
pub use metaschema::MetaSchemaError;

use json::{escape_pointer, format_float64, literal_value, write_canonical, Node};
use metaschema::{read_meta_schema, FileForm, MetaSchemaRules};

use crate::ajv::{ajv_errors, json_string};

/// The core registry's meta-schema, `superschematic json-schema` with no
/// extension linked; internal/tools/schemafiletypes writes it, the same
/// bytes as `@superschematic/schema-ir/schema-file.json`.
pub const CORE_META_SCHEMA: &str = include_str!("schema-file.json");

/// Keys whose values Go stores as raw JSON, not decoded into IR types.
const RAW_SLOTS: [&str; 2] = ["extensions", "documents"];

/// One reason a payload failed to load, at a JSON pointer into it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SchemaFileIssue {
    pub path: String,
    pub message: String,
}

/// A payload the loader refuses.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SchemaFileError {
    source: String,
    issues: Vec<SchemaFileIssue>,
}

impl SchemaFileError {
    fn new(source: &str, issues: Vec<SchemaFileIssue>) -> Self {
        Self {
            source: source.to_owned(),
            issues,
        }
    }

    fn one(source: &str, path: impl Into<String>, message: impl Into<String>) -> Self {
        Self::new(
            source,
            vec![SchemaFileIssue {
                path: path.into(),
                message: message.into(),
            }],
        )
    }

    /// The payload's name, as the caller gave it.
    pub fn source(&self) -> &str {
        &self.source
    }

    pub fn issues(&self) -> &[SchemaFileIssue] {
        &self.issues
    }
}

/// `<source>: <path>: <message>` per issue, joined with `; `, a path left
/// out where it is empty.
impl fmt::Display for SchemaFileError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}: ", self.source)?;
        for (i, issue) in self.issues.iter().enumerate() {
            if i > 0 {
                f.write_str("; ")?;
            }
            if issue.path.is_empty() {
                f.write_str(&issue.message)?;
            } else {
                write!(f, "{}: {}", issue.path, issue.message)?;
            }
        }
        Ok(())
    }
}

impl std::error::Error for SchemaFileError {}

/// A loaded schema file.
#[derive(Debug, Clone, PartialEq)]
pub struct LoadedSchemaFile {
    /// The document, its members in the order the file wrote them and each
    /// number as serde_json reads its literal: exactly with serde_json's
    /// `arbitrary_precision` feature, an integer past 64 bits as a float64
    /// without it. `canonical` holds every number exactly.
    pub document: Value,
    /// The document as Go's ir.CanonicalJSON writes the document Go decodes.
    pub canonical: String,
}

/// A value Go's decoder refuses after the JSON Schema accepted it.
struct DecodeFailure(SchemaFileIssue);

/// Loads schema files against one meta-schema, compiled once.
pub struct SchemaFileLoader {
    rules: MetaSchemaRules,
    validators: HashMap<String, Validator>,
}

impl fmt::Debug for SchemaFileLoader {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SchemaFileLoader")
            .field("forms", &self.validators.keys().collect::<Vec<_>>())
            .finish_non_exhaustive()
    }
}

impl Default for SchemaFileLoader {
    fn default() -> Self {
        Self::new()
    }
}

impl SchemaFileLoader {
    /// A loader for the core registry's meta-schema, [`CORE_META_SCHEMA`].
    pub fn new() -> Self {
        match Self::with_meta_schema_text(CORE_META_SCHEMA) {
            Ok(loader) => loader,
            // The crate's tests load against it, so this is a build defect.
            Err(error) => panic!("the core meta-schema does not compile: {error}"),
        }
    }

    /// A loader for the meta-schema of a deployment's binary: its
    /// `superschematic json-schema` output, which adds its kinds, extension
    /// decorators, documents, invocation policy and behaviors.
    pub fn with_meta_schema(meta_schema: &Value) -> Result<Self, MetaSchemaError> {
        let rules = read_meta_schema(meta_schema)?;
        let mut validators = HashMap::new();
        for form in rules.forms() {
            if validators.contains_key(&form.def) {
                continue;
            }
            // The meta-schema with its root pointing at the form's
            // definition, so `$ref`s resolve against its own `$id`.
            let mut schema = meta_schema.clone();
            if let Some(root) = schema.as_object_mut() {
                root.remove("oneOf");
                root.insert(
                    "$ref".to_owned(),
                    Value::String(format!("#/$defs/{}", form.def)),
                );
            }
            // The Go validator treats format as an annotation and ignores
            // unknown keywords, as ajv does without its strict mode.
            let validator = crate::ajv::options().build(&schema).map_err(|error| {
                MetaSchemaError::new(format!("cannot compile $defs/{}: {error}", form.def))
            })?;
            validators.insert(form.def.clone(), validator);
        }
        Ok(Self { rules, validators })
    }

    /// A loader for a meta-schema given as JSON text.
    pub fn with_meta_schema_text(text: &str) -> Result<Self, MetaSchemaError> {
        let meta_schema: Value = serde_json::from_str(text)
            .map_err(|error| MetaSchemaError::new(format!("not JSON: {error}")))?;
        Self::with_meta_schema(&meta_schema)
    }

    /// Reads one schema file from its JSON text. source names the payload in
    /// errors, as a file path or a label. A payload the Go reader refuses is
    /// a [`SchemaFileError`].
    pub fn load(&self, text: &str, source: &str) -> Result<LoadedSchemaFile, SchemaFileError> {
        let parsed =
            json::parse(text).map_err(|error| SchemaFileError::one(source, "", error.message()))?;
        let tree = parsed.node;
        // Go reads the payload into a map of float64 values first, so a
        // number past float64 fails anywhere, extension data included.
        if let Some(path) = find_non_finite(&tree, "") {
            return Err(SchemaFileError::one(
                source,
                path,
                "number is out of the float64 range",
            ));
        }
        if tree.as_object().is_none() {
            return Err(SchemaFileError::one(
                source,
                "",
                "a schema file is a JSON object",
            ));
        }
        // Go's decode reads every copy of a repeated key into the same
        // value, so its reader refuses one.
        if let Some((key, path)) = parsed.repeated {
            return Err(SchemaFileError::one(
                source,
                path,
                format!("repeated object key {}", json_string(&key)),
            ));
        }

        let form = self.dispatch(&tree, source)?;
        if let Some(validator) = self.validators.get(&form.def) {
            let value = tree.to_value();
            let errors: Vec<_> = validator.iter_errors(&value).collect();
            if !errors.is_empty() {
                return Err(SchemaFileError::new(source, ajv_issues(&errors, &value)));
            }
        }

        let decoded = self
            .decode_def(&tree, &form.def, "")
            .map_err(|DecodeFailure(issue)| SchemaFileError::new(source, vec![issue]))?;
        let mut document = into_document(decoded, form);
        drop_empty_extension_entries(&mut document);
        self.fill_invocation_policy(&mut document);
        Ok(LoadedSchemaFile {
            document: document.to_value(),
            canonical: write_canonical(&document),
        })
    }

    /// Picks the file form, in the Go reader's order: a kind naming a
    /// single-definition form or a schema kind, then the key that marks the
    /// form without a kind, then any document collection key.
    fn dispatch(&self, payload: &Node, source: &str) -> Result<&FileForm, SchemaFileError> {
        let rules = &self.rules;
        if let Some(kind) = payload.get("kind") {
            let Node::String(kind) = kind else {
                return Err(SchemaFileError::one(
                    source,
                    "/kind",
                    "the \"kind\" key must be a string",
                ));
            };
            if let Some((_, form)) = rules.by_kind.iter().find(|(name, _)| name == kind) {
                return Ok(form);
            }
            if rules.schema_kinds.iter().any(|name| name == kind) {
                return Ok(&rules.document);
            }
            let kinds: Vec<&str> = rules
                .by_kind
                .iter()
                .map(|(name, _)| name.as_str())
                .collect();
            return Err(SchemaFileError::one(
                source,
                "/kind",
                format!(
                    "unknown kind \"{kind}\": expected a definition kind ({}) or a schema kind ({})",
                    kinds.join(", "),
                    rules.schema_kinds.join(", ")
                ),
            ));
        }
        let (marker, form) = &rules.by_discriminator;
        if payload.has(marker) {
            return Ok(form);
        }
        if rules.collection_keys.iter().any(|key| payload.has(key)) {
            return Ok(&rules.document);
        }
        Err(SchemaFileError::one(
            source,
            "",
            format!(
                "cannot determine schema file shape: expected a \"kind\" or \"{marker}\" discriminator or a document collection key ({})",
                rules.collection_keys.join(", ")
            ),
        ))
    }

    /// Decodes an object against a $defs entry as Go's encoding/json decodes
    /// it into the IR struct and the struct encodes back: a property that
    /// holds its default, the value the Go encoder omits, is dropped, except
    /// the invocation policy, which Go keeps once set.
    fn decode_def(&self, node: &Node, def: &str, path: &str) -> Result<Node, DecodeFailure> {
        let Some(members) = node.as_object() else {
            return Ok(node.clone());
        };
        let empty = Map::new();
        let props = self.rules.properties(def).unwrap_or(&empty);
        let mut out = Vec::with_capacity(members.len());
        for (key, child) in members {
            let Some(prop) = props.get(key) else {
                return Err(DecodeFailure(SchemaFileIssue {
                    path: path.to_owned(),
                    message: format!(
                        "schema-file loader: $defs/{def} validated a key it does not declare: {key}"
                    ),
                }));
            };
            let at = format!("{path}/{}", escape_pointer(key));
            let value = if RAW_SLOTS.contains(&key.as_str()) {
                child.clone()
            } else {
                self.decode(child, prop, &at)?
            };
            let policy = &self.rules.policy;
            let is_policy = def == policy.def && *key == policy.key;
            if !is_policy {
                if let Some(fallback) = prop.get("default") {
                    if equals_default(&value, fallback) {
                        continue;
                    }
                }
            }
            out.push((key.clone(), value));
        }
        Ok(Node::Object(out))
    }

    fn decode(&self, node: &Node, schema: &Value, path: &str) -> Result<Node, DecodeFailure> {
        if let Some(reference) = schema.get("$ref").and_then(Value::as_str) {
            let def = reference.strip_prefix("#/$defs/").unwrap_or(reference);
            return self.decode_def(node, def, path);
        }
        match schema.get("type").and_then(Value::as_str) {
            Some("object") => {
                if schema.get("properties").is_some() {
                    return Err(DecodeFailure(SchemaFileIssue {
                        path: path.to_owned(),
                        message: format!(
                            "schema-file meta-schema: an inline object with properties at {path}"
                        ),
                    }));
                }
                let Some(members) = node.as_object() else {
                    return Ok(node.clone());
                };
                let values = schema.get("additionalProperties").filter(|v| v.is_object());
                let mut out = Vec::with_capacity(members.len());
                for (key, child) in members {
                    let at = format!("{path}/{}", escape_pointer(key));
                    let value = match values {
                        Some(values) => self.decode(child, values, &at)?,
                        None => decode_any(child),
                    };
                    out.push((key.clone(), value));
                }
                Ok(Node::Object(out))
            }
            Some("array") => {
                let Some(items) = node.as_array() else {
                    return Ok(node.clone());
                };
                let item_schema = schema.get("items").unwrap_or(&Value::Null);
                items
                    .iter()
                    .enumerate()
                    .map(|(i, item)| self.decode(item, item_schema, &format!("{path}/{i}")))
                    .collect::<Result<Vec<_>, _>>()
                    .map(Node::Array)
            }
            Some("integer") => decode_integer(node, path),
            Some("number") => Ok(match node {
                Node::Number(literal) => Node::Number(format_float64(literal_value(literal))),
                other => other.clone(),
            }),
            _ => Ok(node.clone()),
        }
    }

    /// Gives every visible tool of an operation set that omits the policy
    /// the registry's default, as the Go reader does. An mcp record
    /// elsewhere, or a hidden one, is left as it is.
    fn fill_invocation_policy(&self, document: &mut Node) {
        let policy = &self.rules.policy;
        let Some(sets) = document
            .get_mut("operationSets")
            .and_then(Node::as_array_mut)
        else {
            return;
        };
        for set in sets {
            let Some(operations) = set.get_mut("operations").and_then(Node::as_array_mut) else {
                continue;
            };
            for operation in operations {
                let Some(mcp) = operation.get_mut("mcp") else {
                    continue;
                };
                if mcp.get("hidden") == Some(&Node::Bool(false)) && !mcp.has(&policy.key) {
                    mcp.set(&policy.key, Node::String(policy.default.clone()));
                }
            }
        }
    }
}

/// Decodes a value Go holds as `any`: a number becomes a float64, written
/// back in Go's float form.
fn decode_any(node: &Node) -> Node {
    match node {
        Node::Number(literal) => Node::Number(format_float64(literal_value(literal))),
        Node::Array(items) => Node::Array(items.iter().map(decode_any).collect()),
        Node::Object(members) => Node::Object(
            members
                .iter()
                .map(|(key, value)| (key.clone(), decode_any(value)))
                .collect(),
        ),
        other => other.clone(),
    }
}

/// Decodes into a Go int or int64: an integer literal with no fraction or
/// exponent, within int64. -0 reads as 0.
fn decode_integer(node: &Node, path: &str) -> Result<Node, DecodeFailure> {
    let literal = match node {
        Node::Number(literal) => literal.as_str(),
        _ => "",
    };
    let digits = literal.strip_prefix('-').unwrap_or(literal);
    let integral = !digits.is_empty()
        && digits.bytes().all(|b| b.is_ascii_digit())
        && (digits == "0" || !digits.starts_with('0'));
    if integral {
        if let Ok(value) = literal.parse::<i64>() {
            return Ok(Node::Number(value.to_string()));
        }
    }
    Err(DecodeFailure(SchemaFileIssue {
        path: path.to_owned(),
        message: format!("cannot decode number {literal} into an integer field"),
    }))
}

fn equals_default(value: &Node, fallback: &Value) -> bool {
    match value {
        Node::Number(literal) => fallback
            .as_f64()
            .is_some_and(|number| literal_value(literal) == number),
        Node::Array(items) => fallback.as_array().is_some_and(Vec::is_empty) && items.is_empty(),
        Node::Object(members) => {
            fallback.as_object().is_some_and(Map::is_empty) && members.is_empty()
        }
        Node::Null => fallback.is_null(),
        Node::Bool(flag) => fallback.as_bool() == Some(*flag),
        Node::String(text) => fallback.as_str() == Some(text.as_str()),
    }
}

/// Puts a single definition into a document, under its name in a map
/// collection or as the one entry of a list, without the kind
/// discriminator, as the Go reader does.
fn into_document(mut decoded: Node, form: &FileForm) -> Node {
    let Some(collection) = &form.collection else {
        return decoded;
    };
    decoded.remove("kind");
    if collection.list {
        return Node::Object(vec![(collection.key.clone(), Node::Array(vec![decoded]))]);
    }
    let name = match decoded.get("name") {
        Some(Node::String(name)) => name.clone(),
        _ => String::new(),
    };
    Node::Object(vec![(
        collection.key.clone(),
        Node::Object(vec![(name, decoded)]),
    )])
}

/// Drops each extension entry that is an empty object, and then an empty
/// extensions object, on the holders the Go reader canonicalizes: the
/// document, its types and their fields, its operation sets and their
/// operations.
fn drop_empty_extension_entries(document: &mut Node) {
    fn clean(holder: &mut Node) {
        let Some(extensions) = holder.get_mut("extensions") else {
            return;
        };
        let Some(entries) = extensions.as_object_mut() else {
            return;
        };
        entries.retain(|(_, value)| !matches!(value, Node::Object(members) if members.is_empty()));
        if entries.is_empty() {
            holder.remove("extensions");
        }
    }
    fn clean_list(holder: &mut Node, key: &str) {
        if let Some(items) = holder.get_mut(key).and_then(Node::as_array_mut) {
            items.iter_mut().for_each(clean);
        }
    }

    clean(document);
    if let Some(types) = document.get_mut("types").and_then(Node::as_object_mut) {
        for (_, type_def) in types.iter_mut() {
            clean(type_def);
            clean_list(type_def, "fields");
        }
    }
    if let Some(sets) = document
        .get_mut("operationSets")
        .and_then(Node::as_array_mut)
    {
        for set in sets {
            clean(set);
            clean_list(set, "operations");
        }
    }
}

/// The JSON pointer of the first number past the float64 range, in
/// document order.
fn find_non_finite(node: &Node, path: &str) -> Option<String> {
    match node {
        Node::Number(literal) => (!literal_value(literal).is_finite()).then(|| path.to_owned()),
        Node::Array(items) => items
            .iter()
            .enumerate()
            .find_map(|(i, item)| find_non_finite(item, &format!("{path}/{i}"))),
        Node::Object(members) => members.iter().find_map(|(key, value)| {
            find_non_finite(value, &format!("{path}/{}", escape_pointer(key)))
        }),
        _ => None,
    }
}

/// The issues jsonschema's errors stand for, as ajv lists them (the `ajv`
/// module), in the TypeScript loader's words for an unknown key and a value
/// outside an enum; then sorted by pointer, ajv keyword and message, each
/// by code point (the byte order of their UTF-8), as the TypeScript loader
/// sorts ajv's errors.
fn ajv_issues(errors: &[jsonschema::ValidationError<'_>], payload: &Value) -> Vec<SchemaFileIssue> {
    let mut out: Vec<KeywordIssue> = ajv_errors(errors, payload)
        .into_iter()
        .map(|error| {
            let message = match (&error.additional_property, &error.allowed_values) {
                (Some(key), _) => format!("unknown key {}", json_string(key)),
                (_, Some(values)) => format!(
                    "must be one of {}",
                    values
                        .iter()
                        .map(Value::to_string)
                        .collect::<Vec<_>>()
                        .join(", ")
                ),
                _ => error.message,
            };
            KeywordIssue {
                keyword: error.keyword,
                issue: SchemaFileIssue {
                    path: error.instance_path,
                    message,
                },
            }
        })
        .collect();
    out.sort_by(|a, b| {
        a.issue
            .path
            .cmp(&b.issue.path)
            .then_with(|| a.keyword.cmp(&b.keyword))
            .then_with(|| a.issue.message.cmp(&b.issue.message))
    });
    out.into_iter().map(|keyed| keyed.issue).collect()
}

/// An issue and the ajv keyword that failed, which orders it.
struct KeywordIssue {
    keyword: String,
    issue: SchemaFileIssue,
}

static CORE: LazyLock<SchemaFileLoader> = LazyLock::new(SchemaFileLoader::new);

/// Loads one schema file against the core registry's meta-schema, with a
/// loader built once. Construct a [`SchemaFileLoader`] to keep one for a
/// deployment's meta-schema.
pub fn load_schema_file(text: &str, source: &str) -> Result<LoadedSchemaFile, SchemaFileError> {
    CORE.load(text, source)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_core_meta_schema_reads_and_compiles() {
        let loader = SchemaFileLoader::new();
        let rules = &loader.rules;
        assert_eq!(rules.by_discriminator.0, "role");
        let kinds: Vec<&str> = rules
            .by_kind
            .iter()
            .map(|(kind, _)| kind.as_str())
            .collect();
        assert_eq!(kinds, ["Enum", "Union", "Scalar", "OperationSet"]);
        assert_eq!(rules.policy.key, "invocationPolicy");
        assert_eq!(rules.policy.default, "auto");
        assert!(rules.collection_keys.iter().any(|key| key == "types"));
    }

    #[test]
    fn issues_carry_ajv_messages() {
        let loader = SchemaFileLoader::new();
        let issues = |text: &str| loader.load(text, "x").unwrap_err().issues().to_vec();
        assert_eq!(
            issues(r#"{"name": "Item", "role": "EmbeddedStruct", "colour": "red"}"#),
            [SchemaFileIssue {
                path: String::new(),
                message: "unknown key \"colour\"".to_owned()
            }]
        );
        assert_eq!(
            issues(r#"{"name": "Item", "role": "EmbeddedStruct", "strictJSON": "yes"}"#)[0].message,
            "must be boolean"
        );
        assert_eq!(
            issues(r#"{"role": "EmbeddedStruct"}"#)[0].message,
            "must have required property 'name'"
        );
        let unknown_role = issues(r#"{"name": "Item", "role": "Table"}"#);
        assert_eq!(unknown_role[0].path, "/role");
        assert!(unknown_role[0]
            .message
            .starts_with("must be one of \"DBTable\""));
        assert_eq!(
            issues(r#"{"kind": 3}"#)[0].message,
            "the \"kind\" key must be a string"
        );
        assert_eq!(issues("[]")[0].message, "a schema file is a JSON object");
        assert_eq!(
            issues(r#"{"types": {}, "x": 1e400}"#)[0],
            SchemaFileIssue {
                path: "/x".to_owned(),
                message: "number is out of the float64 range".to_owned()
            }
        );
    }

    #[test]
    fn a_meta_schema_without_its_parts_is_refused() {
        let refused = |value: Value| SchemaFileLoader::with_meta_schema(&value).unwrap_err();
        assert_eq!(
            refused(serde_json::json!({})).to_string(),
            "schema-file meta-schema: no $defs"
        );
        let mut meta: Value = serde_json::from_str(CORE_META_SCHEMA).unwrap();
        meta["$defs"]["OperationMCP"]["properties"]["invocationPolicy"]
            .as_object_mut()
            .unwrap()
            .remove("default");
        assert!(refused(meta).to_string().contains("invocation policy"));
        assert!(SchemaFileLoader::with_meta_schema_text("{").is_err());
    }

    #[test]
    fn a_loader_is_shared_across_threads() {
        fn shared<T: Send + Sync>() {}
        shared::<SchemaFileLoader>();
    }

    #[test]
    fn issues_come_sorted_by_pointer_then_keyword_then_message() {
        let loader = SchemaFileLoader::new();
        let error = loader
            .load(
                r#"{"name": "Document", "role": "DBTable", "behaviors": [{"name": "Comments", "config": {"threads": true}}]}"#,
                "x",
            )
            .unwrap_err();
        let issues: Vec<(&str, &str)> = error
            .issues()
            .iter()
            .map(|issue| (issue.path.as_str(), issue.message.as_str()))
            .collect();
        assert_eq!(
            issues,
            [
                ("/behaviors/0", "must match \"then\" schema"),
                ("/behaviors/0/config", "unknown key \"threads\""),
            ]
        );
    }

    #[test]
    fn issues_at_one_pointer_come_by_keyword_before_message() {
        let loader = SchemaFileLoader::new();
        let error = loader
            .load(
                r#"{"name": "Document", "role": "DBTable", "behaviors": [{"name": "Comments", "config": {"threads": true}}, {"config": {}}], "strictJSON": "yes"}"#,
                "x",
            )
            .unwrap_err();
        let issues: Vec<(&str, &str)> = error
            .issues()
            .iter()
            .map(|issue| (issue.path.as_str(), issue.message.as_str()))
            .collect();
        // By pointer first.
        assert!(
            issues.windows(2).all(|pair| pair[0].0 <= pair[1].0),
            "{issues:#?}"
        );
        // At one pointer, an `if` branch's failure (keyword `if`) comes
        // before a missing property (keyword `required`), though its
        // message sorts after.
        let entry: Vec<&str> = issues
            .iter()
            .filter(|(path, _)| *path == "/behaviors/1")
            .map(|(_, message)| *message)
            .collect();
        assert_eq!(entry.last(), Some(&"must have required property 'name'"));
        assert!(entry[..entry.len() - 1]
            .iter()
            .all(|message| *message == "must match \"then\" schema"));
        assert_eq!(issues.last(), Some(&("/strictJSON", "must be boolean")));
    }
}
