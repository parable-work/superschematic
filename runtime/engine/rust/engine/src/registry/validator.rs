//! The validator of one schema version, the port of
//! `src/registry/validator.ts` (D16).
//!
//! The schema runtime (`superschematic-schema-ir`'s `Runtime`) checks each
//! field's value (presence, JSON type, scalar rules, enum membership, list
//! rules), built from the loaded document with a catalog scalar the
//! document declares read as the catalog's. The engine checks two things
//! the runtime does not, because the compatibility rule depends on what a
//! stored instance can hold:
//!
//! - the value is JSON: a number JavaScript would read as Infinity is not;
//! - an object holds no key its type does not declare, at every level, so a
//!   new optional field never meets a value an older version let through.
//!
//! [`SchemaValidator::normalize`] is what a write stores (D16, amended): the
//! runtime's strict parse of the instance type, which puts each scalar
//! value in its canonical form at every depth and fills each absent field's
//! default. What the parse cannot read is left as it was for
//! [`SchemaValidator::validate`] to report; a scalar parser's own refusal is
//! returned for the write to report when validate finds nothing.

use std::collections::BTreeSet;

use serde_json::{Map, Value};

use superschematic_schema_ir::{parse_schema_ir, FieldEntry, FieldErrors, ParseOptions, Runtime};

use super::document::{
    array_depth, fields_of, json_key, ref_kind, runtime_document, type_name, RefKind, SchemaModel,
};
use crate::errors::{Error, ValidationIssue};
use crate::json;

/// How [`SchemaValidator::normalize`] reads a write.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NormalizeMode {
    /// A create's instance, which takes defaults.
    Create,
    /// A merge patch, which takes none.
    Patch,
}

/// A write as the version stores it, and what a scalar's parser refused in
/// it.
#[derive(Clone, Debug, PartialEq)]
pub struct Normalized {
    pub value: Value,
    /// A scalar parser's refusals (rule `parse`), which the write reports
    /// when validate reports nothing.
    pub issues: Vec<ValidationIssue>,
}

// The parse's findings validate reports itself, under its own rules: a
// value of the wrong type, a key no type declares, and a default whose
// text the field's type cannot read.
const VALIDATE_REPORTS: [&str; 3] = ["type", "unknown_field", "default"];

/// The validator of one published version, built once per version.
pub struct SchemaValidator {
    model: SchemaModel,
    runtime: Runtime,
    // The runtime's object types; every other type of the document is an
    // input.
    object_types: BTreeSet<String>,
}

impl std::fmt::Debug for SchemaValidator {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SchemaValidator")
            .field("name", &self.model.name)
            .field("instance_type", &self.model.instance_type)
            .finish_non_exhaustive()
    }
}

impl SchemaValidator {
    /// The validator of a version the engine accepted.
    pub fn new(model: SchemaModel) -> Result<Self, Error> {
        let schema = parse_schema_ir(&runtime_document(&model.document)).map_err(|error| {
            Error::internal(format!(
                "schema {} does not read as IR: {}",
                model.name,
                error.message()
            ))
        })?;
        let object_types = schema.types.keys().cloned().collect();
        Ok(SchemaValidator {
            model,
            runtime: Runtime::new(schema),
            object_types,
        })
    }

    /// The version's model: its name, instance type, document and canonical
    /// form.
    pub fn model(&self) -> &SchemaModel {
        &self.model
    }

    fn errors_of(&self, type_name: &str, value: &Value) -> FieldErrors {
        if self.object_types.contains(type_name) {
            self.runtime.validate_type(type_name, value)
        } else {
            self.runtime.validate_input(type_name, value)
        }
    }

    /// Every issue with `value` as an instance; empty means valid.
    pub fn validate(&self, value: &Value) -> Vec<ValidationIssue> {
        let mut issues = Vec::new();
        let Value::Object(members) = value else {
            issues.push(ValidationIssue::new(
                "",
                "type",
                "an instance is a JSON object",
            ));
            return issues;
        };
        json_issues(value, "", &mut issues);
        if !issues.is_empty() {
            return issues;
        }
        undeclared_keys(
            &self.model.document,
            &self.model.instance_type,
            members,
            "",
            &mut issues,
        );
        flatten(
            &self.errors_of(&self.model.instance_type, value),
            "",
            &mut issues,
        );
        issues
    }

    /// `value` as the version stores it, and the issues a scalar's parser
    /// found that validate does not report: [`NormalizeMode::Create`] fills
    /// each absent field's default, at every depth; [`NormalizeMode::Patch`]
    /// reads a merge patch and fills none. A value that is not a JSON
    /// object, or holds a number that is not JSON, comes back as it is.
    pub fn normalize(&self, value: &Value, mode: NormalizeMode) -> Normalized {
        let unchanged = || Normalized {
            value: value.clone(),
            issues: Vec::new(),
        };
        if !value.is_object() {
            return unchanged();
        }
        let mut not_json = Vec::new();
        json_issues(value, "", &mut not_json);
        if !not_json.is_empty() {
            return unchanged();
        }
        let type_name = &self.model.instance_type;
        let options = ParseOptions { strict: true };
        let parsed = if self.object_types.contains(type_name) {
            self.runtime.parse_type(type_name, value, options)
        } else {
            self.runtime.parse_input(type_name, value, options)
        };
        let mut issues = Vec::new();
        flatten(&parsed.errors, "", &mut issues);
        issues.retain(|issue| !VALIDATE_REPORTS.contains(&issue.rule.as_str()));
        Normalized {
            value: overlay(
                value,
                Some(&Value::Object(parsed.data)),
                mode == NormalizeMode::Create,
            ),
            issues,
        }
    }

    /// One value of a top-level own field as a write would store it, for a
    /// lookup's key and a list's where; any other key's value comes back as
    /// it is.
    pub fn normalize_field(&self, key: &str, value: &Value) -> Value {
        let mut patch = Map::new();
        patch.insert(key.to_owned(), value.clone());
        let normalized = self.normalize(&Value::Object(patch), NormalizeMode::Patch);
        normalized
            .value
            .get(key)
            .cloned()
            .unwrap_or_else(|| value.clone())
    }

    /// Every issue with `value` as a value of one of the document's types
    /// besides the instance type, by the rules a field of that type is held
    /// to, each issue's path under `path`. A name that is no such type is
    /// refused.
    pub fn validate_type(
        &self,
        type_name: &str,
        value: &Value,
        path: &str,
    ) -> Result<Vec<ValidationIssue>, Error> {
        if type_name == self.model.instance_type
            || ref_kind(&self.model.document, type_name) != RefKind::Type
        {
            return Err(Error::usage(format!(
                "{type_name} is not a type of schema {} besides its instance type",
                self.model.name
            )));
        }
        let mut issues = Vec::new();
        let Value::Object(members) = value else {
            issues.push(ValidationIssue::new(
                path,
                "type",
                format!("a {type_name} is a JSON object"),
            ));
            return Ok(issues);
        };
        json_issues(value, path, &mut issues);
        if !issues.is_empty() {
            return Ok(issues);
        }
        undeclared_keys(&self.model.document, type_name, members, path, &mut issues);
        flatten(&self.errors_of(type_name, value), path, &mut issues);
        Ok(issues)
    }
}

// overlay is given with each value the parse read in its place: the
// parse's value for each member it read, at every depth, and the given one
// for a member it did not (an undeclared key), so validate still sees it;
// with defaults, also the members the parse filled.
fn overlay(given: &Value, parsed: Option<&Value>, defaults: bool) -> Value {
    match (given, parsed) {
        (Value::Object(given), Some(Value::Object(parsed))) => {
            let mut out = Map::new();
            for (key, member) in given {
                let value = match parsed.get(key) {
                    Some(read) => overlay(member, Some(read), defaults),
                    None => member.clone(),
                };
                out.insert(key.clone(), value);
            }
            if defaults {
                for (key, member) in parsed {
                    if !given.contains_key(key) {
                        out.insert(key.clone(), member.clone());
                    }
                }
            }
            Value::Object(out)
        }
        (Value::Array(given), Some(Value::Array(parsed))) if given.len() == parsed.len() => {
            Value::Array(
                given
                    .iter()
                    .zip(parsed)
                    .map(|(element, read)| overlay(element, Some(read), defaults))
                    .collect(),
            )
        }
        (given, None) => given.clone(),
        (_, Some(parsed)) => parsed.clone(),
    }
}

// json_issues reports each number JavaScript reads as Infinity, which is
// not a JSON number.
fn json_issues(value: &Value, path: &str, issues: &mut Vec<ValidationIssue>) {
    match value {
        Value::Number(number) => {
            let read = json::number_value(number);
            if !read.is_finite() {
                issues.push(ValidationIssue::new(
                    path,
                    "type",
                    format!("{} is not a JSON number", json::js_number(read)),
                ));
            }
        }
        Value::Array(items) => {
            for (index, item) in items.iter().enumerate() {
                json_issues(item, &format!("{path}[{index}]"), issues);
            }
        }
        Value::Object(members) => {
            for key in json::js_keys(members) {
                json_issues(&members[key], &join(path, key), issues);
            }
        }
        _ => {}
    }
}

// undeclared_keys reports each key of value that the type does not
// declare, then walks into the values of its object-typed fields.
fn undeclared_keys(
    document: &Value,
    type_name: &str,
    value: &Map<String, Value>,
    path: &str,
    issues: &mut Vec<ValidationIssue>,
) {
    let fields = fields_of(document, type_name);
    for key in json::js_keys(value) {
        if !fields.iter().any(|field| json_key(field) == key) {
            issues.push(ValidationIssue::new(
                join(path, key),
                "unknown",
                format!("{type_name} has no field {key}"),
            ));
        }
    }
    // A JSON key two fields share is walked once, where the first of them
    // stands, with the last of them, as the TypeScript engine's map by key
    // keeps it.
    let mut walked: Vec<&str> = Vec::new();
    for field in fields {
        let key = json_key(field);
        if walked.contains(&key) {
            continue;
        }
        walked.push(key);
        let last = fields
            .iter()
            .rev()
            .find(|candidate| json_key(candidate) == key)
            .unwrap_or(field);
        let referenced = self::type_name(last);
        if ref_kind(document, referenced) == RefKind::Type {
            nested_keys(
                document,
                referenced,
                value.get(key),
                array_depth(last),
                &join(path, key),
                issues,
            );
        }
    }
}

// nested_keys walks a value of an object type down its list depth to each
// object it holds. It skips a value of the wrong shape, which the runtime
// reports.
fn nested_keys(
    document: &Value,
    type_name: &str,
    value: Option<&Value>,
    depth: usize,
    path: &str,
    issues: &mut Vec<ValidationIssue>,
) {
    if depth == 0 {
        if let Some(Value::Object(members)) = value {
            undeclared_keys(document, type_name, members, path, issues);
        }
        return;
    }
    if let Some(Value::Array(items)) = value {
        for (index, item) in items.iter().enumerate() {
            nested_keys(
                document,
                type_name,
                Some(item),
                depth - 1,
                &format!("{path}[{index}]"),
                issues,
            );
        }
    }
}

// The runtime nests a type's errors under the field's key; a list
// element's key already carries its index (`lines[2]`).
fn flatten(errors: &FieldErrors, path: &str, issues: &mut Vec<ValidationIssue>) {
    for (key, entry) in errors.iter() {
        let at = if key.is_empty() {
            path.to_owned()
        } else {
            join(path, key)
        };
        match entry {
            FieldEntry::Errors(errors) => {
                for error in errors {
                    issues.push(ValidationIssue::new(
                        at.clone(),
                        error.validator.clone(),
                        error.message.clone(),
                    ));
                }
            }
            FieldEntry::Nested(nested) => flatten(nested, &at, issues),
        }
    }
}

fn join(path: &str, key: &str) -> String {
    if path.is_empty() {
        key.to_owned()
    } else {
        format!("{path}.{key}")
    }
}
