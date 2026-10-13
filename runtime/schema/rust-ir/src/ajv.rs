//! JSON Schema (2020-12) checked with the jsonschema crate and its failures
//! written as ajv writes them (the `ajv` feature): what the TypeScript
//! runtime's schema-file loader and the behavior engine's registry read
//! from ajv, so a second implementation gives clients and agents the same
//! words (D58).
//!
//! [`AjvSchema`] compiles a schema as the TypeScript engine's ajv does it
//! (`new Ajv2020({ strict: false, validateFormats: false, allErrors: true
//! })`): every failure, formats as annotations, unknown keywords ignored.
//! [`ajv_errors`] reads jsonschema's errors as ajv's: one per keyword that
//! failed, at its instance path, with ajv's keyword and message, its
//! `additionalProperty` and `allowedValues` parameters, and for an error
//! inside an `if`'s `then` (or `else`) branch, the branch's own failure
//! (`must match "then" schema`) at the value the `if` read. Each reader
//! words a parameter as it does and sorts the list itself: the loader by
//! its own words, the engine by ajv's (pointer, keyword, message, each by
//! code point), since ajv's own order follows its compiled code.
//!
//! What a compile refuses is jsonschema's wording, not ajv's: only a
//! malformed declaration meets it.

use jsonschema::error::{TypeKind, ValidationErrorKind};
use jsonschema::{Draft, ValidationError, Validator};
use serde_json::Value;

use crate::jsnum::js_number;

/// One failure, as ajv's `ErrorObject` holds it.
#[derive(Clone, Debug, PartialEq)]
pub struct AjvError {
    /// The JSON pointer to the value that failed (`instancePath`).
    pub instance_path: String,
    /// The keyword that failed: `type`, `required`, `if`, `false schema`.
    pub keyword: String,
    /// ajv's message, without a parameter a reader adds.
    pub message: String,
    /// For `additionalProperties`, the key the object holds that it may not
    /// (`params.additionalProperty`).
    pub additional_property: Option<String>,
    /// For `enum`, the values it allows (`params.allowedValues`).
    pub allowed_values: Option<Vec<Value>>,
}

impl AjvError {
    fn new(instance_path: &str, keyword: &str, message: String) -> Self {
        AjvError {
            instance_path: instance_path.to_owned(),
            keyword: keyword.to_owned(),
            message,
            additional_property: None,
            allowed_values: None,
        }
    }
}

/// A compiled JSON Schema: an object schema, or `true` or `false`.
pub struct AjvSchema {
    validator: Validator,
    // The schema as given, whose `type` lists name a failure's types in
    // their order, as ajv's message does.
    schema: Value,
}

impl std::fmt::Debug for AjvSchema {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AjvSchema").finish_non_exhaustive()
    }
}

impl AjvSchema {
    /// Compiles a schema with JSON Schema 2020-12, formats as annotations
    /// and unknown keywords ignored. Nothing is fetched: a `$ref` outside
    /// the schema does not compile.
    pub fn compile(schema: &Value) -> Result<Self, String> {
        options()
            .build(schema)
            .map(|validator| AjvSchema {
                validator,
                schema: schema.clone(),
            })
            .map_err(|error| error.to_string())
    }

    /// Whether a value holds.
    pub fn is_valid(&self, instance: &Value) -> bool {
        self.validator.is_valid(instance)
    }

    /// Every failure of a value, as ajv lists them with `allErrors`, in
    /// jsonschema's order; empty when it holds.
    pub fn errors(&self, instance: &Value) -> Vec<AjvError> {
        let errors: Vec<ValidationError<'_>> = self.validator.iter_errors(instance).collect();
        errors_in(&errors, instance, Some(&self.schema))
    }
}

/// The jsonschema options every schema compiles with here.
pub(crate) fn options() -> jsonschema::ValidationOptions<'static> {
    jsonschema::options()
        .with_draft(Draft::Draft202012)
        .should_validate_formats(false)
}

/// The failures jsonschema's errors stand for, as ajv lists them: each
/// error's own, and for an error inside an `if`'s branch the branch's own
/// failure, after the last error inside it.
pub fn ajv_errors(errors: &[ValidationError<'_>], instance: &Value) -> Vec<AjvError> {
    errors_in(errors, instance, None)
}

/// [`ajv_errors`] with the schema the errors came from, whose `type` lists,
/// `items` and `dependentRequired` ajv's messages name.
#[cfg(feature = "schemafile")]
pub(crate) fn ajv_errors_in(
    errors: &[ValidationError<'_>],
    instance: &Value,
    schema: Option<&Value>,
) -> Vec<AjvError> {
    errors_in(errors, instance, schema)
}

// errors_in is ajv_errors, naming a `type` failure's types in the order
// the schema lists them when it has the schema: jsonschema keeps them as a
// set, in an order of its own.
fn errors_in(
    errors: &[ValidationError<'_>],
    instance: &Value,
    schema: Option<&Value>,
) -> Vec<AjvError> {
    let marks: Vec<Vec<BranchMark>> = errors.iter().map(branch_marks).collect();
    let mut out: Vec<AjvError> = Vec::new();
    let mut closed: Option<(String, String)> = None;
    for (i, error) in errors.iter().enumerate() {
        match closed_object(error) {
            // `additionalProperties: false` beside no `properties` fails
            // once for each key the object holds, where ajv names each key.
            Some(at) => {
                if closed.as_ref() != Some(&at) {
                    let keys: Vec<&String> = instance
                        .pointer(&at.0)
                        .and_then(Value::as_object)
                        .map(|members| members.keys().collect())
                        .unwrap_or_default();
                    out.extend(keys.into_iter().map(|key| AjvError {
                        additional_property: Some(key.clone()),
                        ..AjvError::new(
                            &at.0,
                            "additionalProperties",
                            "must NOT have additional properties".to_owned(),
                        )
                    }));
                    closed = Some(at);
                }
            }
            None => {
                closed = None;
                out.extend(errors_of(error, schema));
            }
        }
        let next = marks.get(i + 1);
        for mark in marks[i].iter().rev() {
            if next.is_some_and(|next| next.contains(mark)) {
                continue;
            }
            out.push(AjvError::new(
                &mark.instance_path,
                "if",
                format!("must match \"{}\" schema", mark.branch),
            ));
        }
    }
    out
}

/// A string as `JSON.stringify` writes one, for a message.
#[cfg(feature = "schemafile")]
pub(crate) fn json_string(text: &str) -> String {
    serde_json::to_string(text).unwrap_or_else(|_| format!("\"{text}\""))
}

/// A schema value as a message names it: a number as JavaScript writes it.
fn limit_text(value: &Value) -> String {
    match value.as_f64() {
        Some(number) => js_number(number),
        None => value.to_string(),
    }
}

/// The object an `additionalProperties: false` beside no `properties`
/// refuses, by its instance path and the keyword's schema path: jsonschema
/// reports each member it holds as a false schema at the object.
fn closed_object(error: &ValidationError<'_>) -> Option<(String, String)> {
    let schema_path = error.schema_path().to_string();
    (matches!(error.kind(), ValidationErrorKind::FalseSchema)
        && schema_path.ends_with("/additionalProperties"))
    .then(|| (error.instance_path().to_string(), schema_path))
}

/// An `if`'s branch an error sits inside: where the branch is in the
/// schema, and the value the `if` read.
#[derive(Debug, Clone, PartialEq, Eq)]
struct BranchMark {
    evaluation_path: String,
    instance_path: String,
    branch: &'static str,
}

/// The `then` and `else` branches on an error's evaluation path, outermost
/// first. Each keyword that applies a subschema to a member or an item
/// moves one step into the instance, which places the value each branch's
/// `if` read on the error's instance path.
fn branch_marks(error: &ValidationError<'_>) -> Vec<BranchMark> {
    let evaluation = error.evaluation_path().to_string();
    let instance = error.instance_path().to_string();
    let segments: Vec<&str> = evaluation.split('/').skip(1).collect();
    let steps: Vec<&str> = instance.split('/').skip(1).collect();
    let mut marks = Vec::new();
    let mut depth = 0;
    let mut i = 0;
    while i < segments.len() {
        match segments[i] {
            // A keyword whose next segment is a member's name or an index.
            "properties" | "patternProperties" | "prefixItems" => {
                depth += 1;
                i += 1;
            }
            "$defs" | "definitions" | "dependentSchemas" | "allOf" | "anyOf" | "oneOf" => i += 1,
            "additionalProperties"
            | "items"
            | "contains"
            | "unevaluatedProperties"
            | "unevaluatedItems"
            | "additionalItems" => depth += 1,
            branch @ ("then" | "else") => {
                let prefix: Vec<&str> = steps.iter().take(depth).copied().collect();
                marks.push(BranchMark {
                    evaluation_path: segments[..=i].join("/"),
                    instance_path: if prefix.is_empty() {
                        String::new()
                    } else {
                        format!("/{}", prefix.join("/"))
                    },
                    branch: if branch == "then" { "then" } else { "else" },
                });
            }
            _ => {}
        }
        i += 1;
    }
    marks
}

/// The failures one jsonschema error stands for, each with ajv's keyword
/// and message: an `anyOf` or a `oneOf` with each branch's failures before
/// its own, a `propertyNames` with its name's.
fn errors_of(error: &ValidationError<'_>, schema: Option<&Value>) -> Vec<AjvError> {
    let path = error.instance_path().to_string();
    match error.kind() {
        ValidationErrorKind::AdditionalProperties { unexpected } => unexpected
            .iter()
            .map(|key| AjvError {
                additional_property: Some(key.clone()),
                ..AjvError::new(
                    &path,
                    "additionalProperties",
                    "must NOT have additional properties".to_owned(),
                )
            })
            .collect(),
        ValidationErrorKind::Enum { options } => vec![AjvError {
            allowed_values: Some(options.as_array().cloned().unwrap_or_default()),
            ..AjvError::new(
                &path,
                "enum",
                "must be equal to one of the allowed values".to_owned(),
            )
        }],
        ValidationErrorKind::AnyOf { context } | ValidationErrorKind::OneOfNotValid { context } => {
            let mut out: Vec<AjvError> = context
                .iter()
                .flatten()
                .flat_map(|error| errors_of(error, schema))
                .collect();
            if matches!(error.kind(), ValidationErrorKind::AnyOf { .. }) {
                out.push(AjvError::new(
                    &path,
                    "anyOf",
                    "must match a schema in anyOf".to_owned(),
                ));
            } else {
                out.push(AjvError::new(
                    &path,
                    "oneOf",
                    "must match exactly one schema in oneOf".to_owned(),
                ));
            }
            out
        }
        ValidationErrorKind::PropertyNames { error: inner } => {
            let mut out = errors_of(inner, schema);
            out.push(AjvError::new(
                &path,
                "propertyNames",
                "property name must be valid".to_owned(),
            ));
            out
        }
        kind => vec![AjvError::new(
            &path,
            &ajv_keyword(kind, error),
            ajv_message(kind, error, schema),
        )],
    }
}

/// The keyword ajv names a failure by: the schema keyword, or `false
/// schema` for a `false` subschema.
fn ajv_keyword(kind: &ValidationErrorKind, error: &ValidationError<'_>) -> String {
    match kind {
        ValidationErrorKind::Type { .. } => "type",
        ValidationErrorKind::Required { .. } if is_dependent_required(error) => "dependentRequired",
        ValidationErrorKind::Required { .. } => "required",
        ValidationErrorKind::Constant { .. } => "const",
        ValidationErrorKind::Minimum { .. } => "minimum",
        ValidationErrorKind::Maximum { .. } => "maximum",
        ValidationErrorKind::ExclusiveMinimum { .. } => "exclusiveMinimum",
        ValidationErrorKind::ExclusiveMaximum { .. } => "exclusiveMaximum",
        ValidationErrorKind::MinLength { .. } => "minLength",
        ValidationErrorKind::MaxLength { .. } => "maxLength",
        ValidationErrorKind::MinItems { .. } => "minItems",
        ValidationErrorKind::MaxItems { .. } => "maxItems",
        ValidationErrorKind::MinProperties { .. } => "minProperties",
        ValidationErrorKind::MaxProperties { .. } => "maxProperties",
        ValidationErrorKind::Pattern { .. } => "pattern",
        ValidationErrorKind::UniqueItems => "uniqueItems",
        ValidationErrorKind::MultipleOf { .. } => "multipleOf",
        ValidationErrorKind::Not { .. } => "not",
        ValidationErrorKind::FalseSchema => "false schema",
        ValidationErrorKind::Format { .. } => "format",
        ValidationErrorKind::Contains => "contains",
        ValidationErrorKind::OneOfMultipleValid { .. } => "oneOf",
        ValidationErrorKind::UnevaluatedProperties { .. } => "unevaluatedProperties",
        ValidationErrorKind::UnevaluatedItems { .. } => "unevaluatedItems",
        ValidationErrorKind::AdditionalItems { .. } => "additionalItems",
        // Any other keyword ends its schema path.
        _ => {
            return error
                .schema_path()
                .to_string()
                .rsplit('/')
                .next()
                .unwrap_or_default()
                .to_owned()
        }
    }
    .to_owned()
}

/// ajv's message for one keyword's failure.
fn ajv_message(
    kind: &ValidationErrorKind,
    error: &ValidationError<'_>,
    schema: Option<&Value>,
) -> String {
    match kind {
        ValidationErrorKind::Type { kind } => {
            let mut names: Vec<&str> = match kind {
                TypeKind::Single(single) => vec![single.as_str()],
                TypeKind::Multiple(set) => set.iter().map(|one| one.as_str()).collect(),
            };
            // ajv names the types as the schema lists them.
            let listed = schema
                .and_then(|schema| schema.pointer(&error.schema_path().to_string()))
                .and_then(Value::as_array);
            if let Some(listed) = listed {
                let order = |name: &&str| {
                    listed
                        .iter()
                        .position(|one| one.as_str() == Some(*name))
                        .unwrap_or(usize::MAX)
                };
                names.sort_by_key(order);
            }
            format!("must be {}", names.join(","))
        }
        ValidationErrorKind::Required { property } => {
            let missing = property
                .as_str()
                .map_or_else(|| property.to_string(), str::to_owned);
            match dependent_of(error, &missing, schema) {
                Some((present, deps)) => format!(
                    "must have {} {} when property {present} is present",
                    if deps.len() == 1 {
                        "property"
                    } else {
                        "properties"
                    },
                    deps.join(", ")
                ),
                None => format!("must have required property '{missing}'"),
            }
        }
        ValidationErrorKind::Constant { .. } => "must be equal to constant".to_owned(),
        ValidationErrorKind::Minimum { limit } => format!("must be >= {}", limit_text(limit)),
        ValidationErrorKind::Maximum { limit } => format!("must be <= {}", limit_text(limit)),
        ValidationErrorKind::ExclusiveMinimum { limit } => {
            format!("must be > {}", limit_text(limit))
        }
        ValidationErrorKind::ExclusiveMaximum { limit } => {
            format!("must be < {}", limit_text(limit))
        }
        ValidationErrorKind::MinLength { limit } => {
            format!("must NOT have fewer than {limit} characters")
        }
        ValidationErrorKind::MaxLength { limit } => {
            format!("must NOT have more than {limit} characters")
        }
        ValidationErrorKind::MinItems { limit } => {
            format!("must NOT have fewer than {limit} items")
        }
        ValidationErrorKind::MaxItems { limit } => format!("must NOT have more than {limit} items"),
        ValidationErrorKind::MinProperties { limit } => {
            format!("must NOT have fewer than {limit} properties")
        }
        ValidationErrorKind::MaxProperties { limit } => {
            format!("must NOT have more than {limit} properties")
        }
        ValidationErrorKind::Pattern { pattern } => format!("must match pattern \"{pattern}\""),
        ValidationErrorKind::UniqueItems => {
            let types = item_types(schema, &error.schema_path().to_string());
            let (j, i) = duplicate_items(error.instance(), &types).unwrap_or((1, 0));
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)")
        }
        ValidationErrorKind::MultipleOf { multiple_of } => {
            format!("must be multiple of {}", js_number(*multiple_of))
        }
        ValidationErrorKind::Not { .. } => "must NOT be valid".to_owned(),
        ValidationErrorKind::FalseSchema => "boolean schema is false".to_owned(),
        ValidationErrorKind::Format { format } => format!("must match format \"{format}\""),
        ValidationErrorKind::Contains => "must contain at least 1 valid item(s)".to_owned(),
        ValidationErrorKind::OneOfMultipleValid { .. } => {
            "must match exactly one schema in oneOf".to_owned()
        }
        ValidationErrorKind::UnevaluatedProperties { .. } => {
            "must NOT have unevaluated properties".to_owned()
        }
        ValidationErrorKind::UnevaluatedItems { .. }
        | ValidationErrorKind::AdditionalItems { .. } => "must NOT have more items".to_owned(),
        _ => error.to_string(),
    }
}

/// Whether a `required` failure is a `dependentRequired`'s: jsonschema
/// reports a missing dependency as a missing property at the keyword.
fn is_dependent_required(error: &ValidationError<'_>) -> bool {
    error
        .schema_path()
        .to_string()
        .ends_with("/dependentRequired")
}

/// The property whose presence a `dependentRequired` failure is about and
/// every property it requires, in the schema's order, which ajv's message
/// names (`params.property` and `params.deps`): the first property the
/// object holds that requires the missing one. `None` for any other
/// failure, or when the schema is not at hand.
fn dependent_of(
    error: &ValidationError<'_>,
    missing: &str,
    schema: Option<&Value>,
) -> Option<(String, Vec<String>)> {
    if !is_dependent_required(error) {
        return None;
    }
    let dependencies = schema?
        .pointer(&error.schema_path().to_string())?
        .as_object()?;
    let held = error.instance().as_object()?;
    dependencies.iter().find_map(|(present, deps)| {
        let deps: Vec<String> = deps
            .as_array()?
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        (held.contains_key(present) && deps.iter().any(|dep| dep == missing))
            .then(|| (present.clone(), deps))
    })
}

/// The types a `uniqueItems`' sibling `items` names, which choose ajv's
/// search: empty when the schema is not at hand, or names none.
fn item_types(schema: Option<&Value>, unique_items: &str) -> Vec<String> {
    let Some(parent) = unique_items
        .strip_suffix("/uniqueItems")
        .and_then(|parent| schema?.pointer(parent))
    else {
        return Vec::new();
    };
    match parent.get("items").and_then(|items| items.get("type")) {
        Some(Value::String(one)) => vec![one.clone()],
        Some(Value::Array(many)) => many
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect(),
        _ => Vec::new(),
    }
}

/// The two indexes ajv names for the items that repeat, `j` and `i`, as
/// its two searches find them. With item types it may key by (scalar
/// ones), it walks from the last item, keeping each one's index, and stops
/// at the first item seen before: `j` is the later index, `i` the earlier,
/// and an item of another type is passed over. Otherwise it compares each
/// item, from the last, with every one before it: `i` is the later index,
/// `j` the nearest earlier one that equals it.
fn duplicate_items(instance: &Value, types: &[String]) -> Option<(usize, usize)> {
    let items = instance.as_array()?;
    let keyed = !types.is_empty() && !types.iter().any(|t| t == "object" || t == "array");
    if keyed {
        let mut seen: std::collections::HashMap<String, usize> = std::collections::HashMap::new();
        for i in (0..items.len()).rev() {
            let item = &items[i];
            if !types.iter().any(|t| of_type(item, t)) {
                continue;
            }
            // JavaScript keys an object by text: a string is marked when
            // the items may be numbers too.
            let mut key = match item {
                Value::String(text) => text.clone(),
                Value::Number(number) => number
                    .as_f64()
                    .map_or_else(|| number.to_string(), js_number),
                other => other.to_string(),
            };
            if types.len() > 1 && item.is_string() {
                key.push('_');
            }
            if let Some(j) = seen.get(&key) {
                return Some((*j, i));
            }
            seen.insert(key, i);
        }
        return None;
    }
    (0..items.len()).rev().find_map(|i| {
        (0..i)
            .rev()
            .find(|j| js_equal(&items[*j], &items[i]))
            .map(|j| (j, i))
    })
}

// of_type is whether a value is of a JSON Schema type, as ajv checks one
// with strictNumbers.
fn of_type(value: &Value, name: &str) -> bool {
    match (name, value) {
        ("string", Value::String(_))
        | ("boolean", Value::Bool(_))
        | ("null", Value::Null)
        | ("object", Value::Object(_))
        | ("array", Value::Array(_)) => true,
        ("number", Value::Number(number)) => number.as_f64().is_some_and(f64::is_finite),
        ("integer", Value::Number(number)) => number
            .as_f64()
            .is_some_and(|x| x.is_finite() && x.fract() == 0.0),
        _ => false,
    }
}

// js_equal is ajv's deep equality: numbers by value, objects by their
// members whatever their order.
fn js_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => x.as_f64() == y.as_f64(),
        (Value::Array(x), Value::Array(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(x, y)| js_equal(x, y))
        }
        (Value::Object(x), Value::Object(y)) => {
            x.len() == y.len()
                && x.iter()
                    .all(|(key, value)| y.get(key).is_some_and(|other| js_equal(value, other)))
        }
        _ => a == b,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn listed(schema: Value, instance: Value) -> Vec<(String, String, String)> {
        let mut errors: Vec<(String, String, String)> = AjvSchema::compile(&schema)
            .unwrap()
            .errors(&instance)
            .into_iter()
            .map(|error| {
                let message = match (&error.additional_property, &error.allowed_values) {
                    (Some(key), _) => format!("{}: {key}", error.message),
                    (_, Some(values)) => format!(
                        "{}: {}",
                        error.message,
                        values
                            .iter()
                            .map(Value::to_string)
                            .collect::<Vec<_>>()
                            .join(", ")
                    ),
                    _ => error.message,
                };
                (error.instance_path, error.keyword, message)
            })
            .collect();
        errors.sort();
        errors
    }

    #[test]
    fn a_repeated_item_is_named_as_ajvs_search_finds_it() {
        let message = |schema: Value, instance: Value| {
            listed(schema, instance)
                .into_iter()
                .find(|(_, keyword, _)| keyword == "uniqueItems")
                .map(|(_, _, message)| message)
                .expect("a uniqueItems failure")
        };
        // Scalar item types: ajv keys the items, from the last.
        let strings = json!({"type": "array", "items": {"type": "string"}, "uniqueItems": true});
        assert_eq!(
            message(strings.clone(), json!(["a", "a"])),
            "must NOT have duplicate items (items ## 1 and 0 are identical)"
        );
        assert_eq!(
            message(strings, json!(["a", "b", "b", "a"])),
            "must NOT have duplicate items (items ## 2 and 1 are identical)"
        );
        // No item type, or objects: ajv compares each item with the ones
        // before it.
        let any = json!({"type": "array", "items": {"enum": ["x", "y"]}, "uniqueItems": true});
        assert_eq!(
            message(any.clone(), json!(["x", "x"])),
            "must NOT have duplicate items (items ## 0 and 1 are identical)"
        );
        assert_eq!(
            message(any, json!(["x", "y", "y", "x"])),
            "must NOT have duplicate items (items ## 0 and 3 are identical)"
        );
        // A string is keyed apart from a number when the items may be
        // either.
        let mixed =
            json!({"type": "array", "items": {"type": ["string", "number"]}, "uniqueItems": true});
        assert_eq!(
            message(mixed, json!(["1", 1, "1"])),
            "must NOT have duplicate items (items ## 2 and 0 are identical)"
        );
        let objects = json!({"type": "array", "items": {"type": "object"}, "uniqueItems": true});
        assert_eq!(
            message(objects, json!([{"a": 1}, {"a": 1.0}])),
            "must NOT have duplicate items (items ## 0 and 1 are identical)"
        );
    }

    #[test]
    fn a_missing_dependency_is_named_with_the_property_that_requires_it() {
        let schema = json!({
            "type": "object",
            "dependentRequired": {"vector": ["model", "text"], "model": ["vector"]}
        });
        assert_eq!(
            listed(schema.clone(), json!({"vector": [1], "text": "x"})),
            [(
                String::new(),
                "dependentRequired".to_owned(),
                "must have properties model, text when property vector is present".to_owned()
            )]
        );
        assert_eq!(
            listed(schema, json!({"model": "m"})),
            [(
                String::new(),
                "dependentRequired".to_owned(),
                "must have property vector when property model is present".to_owned()
            )]
        );
    }

    #[test]
    fn a_type_failure_names_the_types_in_the_schemas_order() {
        let schema = json!({"type": "object", "additionalProperties": {"type": ["string", "object", "array"]}});
        assert_eq!(
            listed(schema, json!({"owner": 7})),
            [(
                "/owner".to_owned(),
                "type".to_owned(),
                "must be string,object,array".to_owned()
            )]
        );
        let items = json!({"type": "array", "items": {"type": ["string", "object"]}});
        assert_eq!(listed(items, json!([7]))[0].2, "must be string,object");
    }

    #[test]
    fn failures_read_as_ajv_writes_them() {
        let schema = json!({
            "type": "object",
            "additionalProperties": false,
            "required": ["by"],
            "properties": {
                "by": {"type": "integer", "minimum": 1},
                "tag": {"enum": ["a", "b"]},
                "list": {"type": "array", "uniqueItems": true, "maxItems": 2}
            }
        });
        assert_eq!(
            listed(
                schema,
                json!({"by": 0, "tag": "c", "list": [1, 1, 2], "extra": true})
            ),
            [
                (
                    String::new(),
                    "additionalProperties".to_owned(),
                    "must NOT have additional properties: extra".to_owned()
                ),
                (
                    "/by".to_owned(),
                    "minimum".to_owned(),
                    "must be >= 1".to_owned()
                ),
                (
                    "/list".to_owned(),
                    "maxItems".to_owned(),
                    "must NOT have more than 2 items".to_owned()
                ),
                (
                    "/list".to_owned(),
                    "uniqueItems".to_owned(),
                    "must NOT have duplicate items (items ## 0 and 1 are identical)".to_owned()
                ),
                (
                    "/tag".to_owned(),
                    "enum".to_owned(),
                    "must be equal to one of the allowed values: \"a\", \"b\"".to_owned()
                ),
            ]
        );
    }

    #[test]
    fn a_closed_object_names_each_key_it_refuses() {
        assert_eq!(
            listed(
                json!({"type": "object", "additionalProperties": false}),
                json!({"a": 1, "b": 2})
            ),
            [
                (
                    String::new(),
                    "additionalProperties".to_owned(),
                    "must NOT have additional properties: a".to_owned()
                ),
                (
                    String::new(),
                    "additionalProperties".to_owned(),
                    "must NOT have additional properties: b".to_owned()
                ),
            ]
        );
        assert!(AjvSchema::compile(&json!(true))
            .unwrap()
            .is_valid(&json!(null)));
        assert!(!AjvSchema::compile(&json!(false))
            .unwrap()
            .is_valid(&json!(null)));
        assert!(AjvSchema::compile(&json!({"type": "nope"})).is_err());
    }
}
