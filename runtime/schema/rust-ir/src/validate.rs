//! Validation of a value against a type of the runtime schema, the port of
//! the TypeScript runtime's `validation/validate.ts`: presence, then the
//! JSON type, then the type's rules, with one error per failing value at a
//! path such as `field`, `field[i]` or `field[i][j]`, a nested object's
//! errors under its field. The codes and messages are the TypeScript
//! runtime's, and a scalar's own checks are superscalar's core's.
//!
//! A `pattern` is matched as the generated Rust validators match one
//! (`superschematic_schema_runtime::translate_pattern`): `\d`, `\w` and `\b` are ASCII, as they
//! are to JavaScript with the `u` flag, and so is `\s`, as it is to RE2.

use std::collections::HashMap;
use std::sync::RwLock;

use regex::Regex;
use serde_json::{Map, Value};

use crate::errors::FieldErrors;
use crate::jsjson::stringify;
use crate::jsnum::js_number;
use crate::scalars;
use crate::schema::{
    is_any_json_scalar, structured_json_type, FieldDef, JsonShape, ScalarDef, Schema, TypeDef,
};
use superschematic_schema_runtime::{code_points, translate_pattern, ValidationError};

/// Patterns compiled once each, for the schema a runtime holds. A pattern
/// that does not compile matches nothing, so its rule refuses every value.
#[derive(Default)]
pub(crate) struct Patterns(RwLock<HashMap<String, Option<Regex>>>);

impl Patterns {
    pub(crate) fn is_match(&self, source: &str, text: &str) -> bool {
        if let Ok(compiled) = self.0.read() {
            if let Some(regex) = compiled.get(source) {
                return regex.as_ref().is_some_and(|re| re.is_match(text));
            }
        }
        let regex = Regex::new(&translate_pattern(source)).ok();
        let matched = regex.as_ref().is_some_and(|re| re.is_match(text));
        if let Ok(mut compiled) = self.0.write() {
            compiled.insert(source.to_owned(), regex);
        }
        matched
    }
}

/// What a type reference names.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RefKind {
    Scalar,
    Enum,
    Type,
    Input,
    /// A GraphQL builtin, an IR primitive, or a name the schema does not
    /// declare.
    Builtin,
}

pub(crate) fn resolve_ref_kind(schema: &Schema, name: &str) -> RefKind {
    if schema.scalars.contains_key(name) {
        RefKind::Scalar
    } else if schema.enums.contains_key(name) {
        RefKind::Enum
    } else if schema.types.contains_key(name) {
        RefKind::Type
    } else if schema.inputs.contains_key(name) {
        RefKind::Input
    } else {
        RefKind::Builtin
    }
}

/// A finite JSON number as a float64, as JavaScript holds it.
pub(crate) fn to_number(value: &Value) -> Option<f64> {
    value.as_number()?.as_f64().filter(|n| n.is_finite())
}

/// Whether a JSON document can carry value: every number finite.
pub(crate) fn is_json_value(value: &Value) -> bool {
    match value {
        Value::Number(_) => to_number(value).is_some(),
        Value::Array(items) => items.iter().all(is_json_value),
        Value::Object(map) => map.values().all(is_json_value),
        _ => true,
    }
}

/// The JSON shape of a value: an array, an object, or neither.
pub(crate) fn json_shape_of(value: &Value) -> Option<JsonShape> {
    match value {
        Value::Array(_) => Some(JsonShape::Array),
        Value::Object(_) => Some(JsonShape::Object),
        _ => None,
    }
}

fn error(validator: &str, message: String) -> ValidationError {
    ValidationError::new(validator, message)
}

fn required_error(name: &str) -> ValidationError {
    error("required", format!("{name} is required."))
}

pub(crate) struct Validator<'a> {
    pub(crate) schema: &'a Schema,
    pub(crate) patterns: &'a Patterns,
}

impl Validator<'_> {
    fn validate_enum_value(&self, enum_name: &str, value: &Value) -> Vec<ValidationError> {
        let Some(text) = value.as_str() else {
            return vec![error("type", format!("{enum_name} must be a string."))];
        };
        let Some(enum_def) = self.schema.enums.get(enum_name) else {
            return Vec::new();
        };
        let allowed = enum_def.values.iter().any(|member| {
            let candidate = if member.serialized_as.is_empty() {
                &member.name
            } else {
                &member.serialized_as
            };
            candidate == text
        });
        if allowed {
            Vec::new()
        } else {
            vec![error(
                "enum",
                format!("{} must be one of the allowed values.", enum_def.name),
            )]
        }
    }

    fn validate_string_constraints(
        &self,
        scalar: &ScalarDef,
        value: &Value,
        apply_required: bool,
    ) -> Vec<ValidationError> {
        // A value of another JSON type is "type", required or not, and its
        // length and format are not checked.
        let Some(text) = value.as_str() else {
            return vec![error("type", format!("{} must be a string.", scalar.name))];
        };
        if text.is_empty() && apply_required {
            return vec![required_error(&scalar.name)];
        }

        let mut errors = Vec::new();
        // Code points, not UTF-16 units: Go counts runes, Python counts len().
        let length = code_points(text) as f64;
        if scalar.min_length > 0.0 && length < scalar.min_length {
            errors.push(error(
                "minLength",
                format!(
                    "{} must be at least {} characters.",
                    scalar.name,
                    js_number(scalar.min_length)
                ),
            ));
        }
        if scalar.max_length > 0.0 && length > scalar.max_length {
            errors.push(error(
                "maxLength",
                format!(
                    "{} must be at most {} characters.",
                    scalar.name,
                    js_number(scalar.max_length)
                ),
            ));
        }
        if !scalar.pattern.is_empty() && !self.patterns.is_match(&scalar.pattern, text) {
            errors.push(error(
                "pattern",
                format!("{} has an invalid format.", scalar.name),
            ));
        }
        if !scalar.reserved_words.is_empty() {
            let reserved = if scalar.case_insensitive {
                let lowered = text.to_lowercase();
                scalar
                    .reserved_words
                    .iter()
                    .any(|word| word.to_lowercase() == lowered)
            } else {
                scalar.reserved_words.iter().any(|word| word == text)
            };
            if reserved {
                errors.push(error(
                    "reservedWord",
                    format!("{} contains a reserved word.", scalar.name),
                ));
            }
        }

        // The scalar core checks the same pattern and lengths again, so it
        // runs only when the constraints above pass: one failing value, one
        // error.
        if errors.is_empty() && (scalar.has_custom_validate || scalars::has_validator(&scalar.name))
        {
            errors.extend(scalars::validate(&scalar.name, text));
        }
        errors
    }

    fn validate_bounds(&self, scalar: &ScalarDef, number: f64) -> Vec<ValidationError> {
        let mut errors = Vec::new();
        if let Some(minimum) = scalar.minimum {
            if number < minimum {
                errors.push(error(
                    "min",
                    format!("{} must be at least {}.", scalar.name, js_number(minimum)),
                ));
            }
        }
        if let Some(maximum) = scalar.maximum {
            if number > maximum {
                errors.push(error(
                    "max",
                    format!("{} must be at most {}.", scalar.name, js_number(maximum)),
                ));
            }
        }
        errors
    }

    fn validate_int_constraints(&self, scalar: &ScalarDef, value: &Value) -> Vec<ValidationError> {
        match to_number(value) {
            Some(number) if number.fract() == 0.0 => self.validate_bounds(scalar, number),
            _ => vec![error("type", format!("{} must be a number.", scalar.name))],
        }
    }

    fn validate_float_constraints(
        &self,
        scalar: &ScalarDef,
        value: &Value,
    ) -> Vec<ValidationError> {
        match to_number(value) {
            Some(number) => self.validate_bounds(scalar, number),
            None => vec![error("type", format!("{} must be a number.", scalar.name))],
        }
    }

    pub(crate) fn validate_scalar_value(
        &self,
        scalar: &ScalarDef,
        value: &Value,
        required: bool,
    ) -> Vec<ValidationError> {
        if is_any_json_scalar(scalar) {
            return validate_any_json_scalar_value(scalar, value, required);
        }
        if let Some(shape) = structured_json_type(scalar) {
            if !value.is_string() {
                return validate_structured_json_scalar_value(scalar, shape, value, required);
            }
        }
        match scalar.primitive.as_str() {
            "Int" => self.validate_int_constraints(scalar, value),
            "Float" => self.validate_float_constraints(scalar, value),
            "Type" => validate_type_scalar_value(scalar, value, required),
            "Bytes" => validate_bytes_scalar_value(scalar, value, required),
            _ => self.validate_string_constraints(scalar, value, required),
        }
    }

    fn validate_field_level_string_constraints(
        &self,
        field: &FieldDef,
        text: &str,
    ) -> Vec<ValidationError> {
        let mut errors = Vec::new();
        // Code points, not UTF-16 units: Go counts runes, Python counts len().
        let length = code_points(text) as f64;
        if let Some(minimum) = field.validate_min_length {
            if length < minimum {
                errors.push(error(
                    "minLength",
                    format!(
                        "{} must be at least {} characters.",
                        field.name,
                        js_number(minimum)
                    ),
                ));
            }
        }
        if let Some(maximum) = field.validate_max_length {
            if length > maximum {
                errors.push(error(
                    "maxLength",
                    format!(
                        "{} must be at most {} characters.",
                        field.name,
                        js_number(maximum)
                    ),
                ));
            }
        }
        if !field.validate_pattern.is_empty()
            && !self.patterns.is_match(&field.validate_pattern, text)
        {
            errors.push(error(
                "pattern",
                format!("{} has an invalid format.", field.name),
            ));
        }
        errors
    }

    fn apply_field_level_constraints(
        &self,
        field: &FieldDef,
        value: &Value,
    ) -> Vec<ValidationError> {
        if let Some(text) = value.as_str() {
            return self.validate_field_level_string_constraints(field, text);
        }
        match to_number(value) {
            Some(number) => validate_field_level_numeric_constraints(field, number),
            None => Vec::new(),
        }
    }

    fn validate_single_field(
        &self,
        field: &FieldDef,
        key: &str,
        value: &Value,
        errors: &mut FieldErrors,
    ) {
        let name = field.type_ref.name.as_str();
        match resolve_ref_kind(self.schema, name) {
            RefKind::Scalar => {
                let Some(scalar) = self.schema.scalars.get(name) else {
                    return;
                };
                let mut field_errors = self.validate_scalar_value(scalar, value, field.required);
                if has_field_level_constraints(field) {
                    field_errors.extend(self.apply_field_level_constraints(field, value));
                }
                errors.set(key, field_errors);
            }
            RefKind::Enum => errors.set(key, self.validate_enum_value(name, value)),
            kind @ (RefKind::Type | RefKind::Input) => {
                self.validate_object_value(field, kind, key, value, errors);
            }
            RefKind::Builtin => {
                if let Some(type_error) = builtin_type_error(field, value) {
                    errors.set(key, vec![type_error]);
                    return;
                }
                if field.required
                    && (name == "String" || name == "ID")
                    && value.as_str() == Some("")
                {
                    errors.add(key, "required", format!("{name} is required."));
                    return;
                }
                if has_field_level_constraints(field) {
                    errors.set(key, self.apply_field_level_constraints(field, value));
                }
            }
        }
    }

    fn validate_array_field(
        &self,
        field: &FieldDef,
        key: &str,
        value: &Value,
        errors: &mut FieldErrors,
    ) {
        let Some(items) = value.as_array() else {
            return;
        };
        let kind = resolve_ref_kind(self.schema, &field.type_ref.name);
        if !field.type_ref.is_array_of_arrays {
            self.validate_array_elements(field, kind, key, items, errors);
            return;
        }
        // T[][]: every inner list is an array, never null, and may be empty.
        // A null inner list is "required" and any other value "type", at
        // key[i].
        for (index, inner) in items.iter().enumerate() {
            let inner_key = format!("{key}[{index}]");
            match inner {
                Value::Null => errors.add(inner_key, "required", "required field"),
                Value::Array(elements) => {
                    self.validate_array_elements(field, kind, &inner_key, elements, errors);
                }
                _ => errors.add(inner_key, "type", "expected an array"),
            }
        }
    }

    /// Validates the elements of one list of a T[] or T[][] field,
    /// reporting each at `key[index]`. For T[][] the key already names the
    /// inner list, so elements report at field[i][j]. A list element is
    /// never null, in a required list and an optional one alike, and the
    /// field's own constraints (length, pattern, bounds) apply to each
    /// element.
    fn validate_array_elements(
        &self,
        field: &FieldDef,
        kind: RefKind,
        key: &str,
        elements: &[Value],
        errors: &mut FieldErrors,
    ) {
        for (index, element) in elements.iter().enumerate() {
            let element_key = format!("{key}[{index}]");
            if element.is_null() {
                errors.add(
                    element_key,
                    "required",
                    format!("{} is required.", field.type_ref.name),
                );
                continue;
            }
            match kind {
                RefKind::Scalar => {
                    let Some(scalar) = self.schema.scalars.get(&field.type_ref.name) else {
                        continue;
                    };
                    let mut element_errors =
                        self.validate_scalar_value(scalar, element, field.required);
                    if has_field_level_constraints(field) {
                        element_errors.extend(self.apply_field_level_constraints(field, element));
                    }
                    errors.set(element_key, element_errors);
                }
                RefKind::Builtin => {
                    if let Some(type_error) = builtin_type_error(field, element) {
                        errors.set(element_key, vec![type_error]);
                    } else if has_field_level_constraints(field) {
                        errors.set(
                            element_key,
                            self.apply_field_level_constraints(field, element),
                        );
                    }
                }
                RefKind::Enum => {
                    errors.set(
                        element_key,
                        self.validate_enum_value(&field.type_ref.name, element),
                    );
                }
                RefKind::Type | RefKind::Input => {
                    self.validate_object_value(field, kind, &element_key, element, errors);
                }
            }
        }
    }

    /// Validates a value of an object-typed field, or one element of a
    /// list of them, at key: an object's field errors nest under key, and
    /// any other JSON value is "type".
    fn validate_object_value(
        &self,
        field: &FieldDef,
        kind: RefKind,
        key: &str,
        value: &Value,
        errors: &mut FieldErrors,
    ) {
        let Some(object) = value.as_object() else {
            errors.add(key, "type", "expected an object");
            return;
        };
        let defs = if kind == RefKind::Type {
            &self.schema.types
        } else {
            &self.schema.inputs
        };
        if let Some(nested) = defs.get(&field.type_ref.name) {
            errors.nest(key, self.validate_type_def(nested, object));
        }
    }

    fn validate_field(
        &self,
        field: &FieldDef,
        data: &Map<String, Value>,
        errors: &mut FieldErrors,
    ) {
        let key = field.key();
        let value = match data.get(key) {
            None | Some(Value::Null) => {
                if field.required {
                    errors.add(key, "required", format!("{} is required.", field.name));
                }
                return;
            }
            Some(value) => value,
        };

        if field.type_ref.is_array {
            // A required list means present, not non-empty: [] is a value,
            // and non-emptiness is declared with listMin. A present value
            // that is not a list is of the wrong JSON type, required or
            // optional.
            let Some(items) = value.as_array() else {
                errors.add(key, "type", "expected an array");
                return;
            };
            // List bounds apply to the outer list, including for T[][].
            let length = items.len() as f64;
            if let Some(minimum) = field.validate_list_min {
                if length < minimum {
                    errors.add(
                        key,
                        "listMin",
                        format!(
                            "{} must have at least {} items.",
                            field.name,
                            js_number(minimum)
                        ),
                    );
                }
            }
            if let Some(maximum) = field.validate_list_max {
                if length > maximum {
                    errors.add(
                        key,
                        "listMax",
                        format!(
                            "{} must have at most {} items.",
                            field.name,
                            js_number(maximum)
                        ),
                    );
                }
            }
            self.validate_array_field(field, key, value, errors);
            return;
        }

        self.validate_single_field(field, key, value, errors);
    }

    pub(crate) fn validate_type_def(
        &self,
        type_def: &TypeDef,
        data: &Map<String, Value>,
    ) -> FieldErrors {
        let mut errors = FieldErrors::new();
        for field in &type_def.fields {
            self.validate_field(field, data, &mut errors);
        }
        errors
    }
}

/// Any-JSON scalars ([`is_any_json_scalar`]): an object, an array, a
/// string (JSON text or not), a number and a boolean are all values, so no
/// type, length or pattern check applies, and the scalar core is not
/// consulted. Null is a missing value. A value no JSON document can carry
/// (a number past the float64 range) is "type".
fn validate_any_json_scalar_value(
    scalar: &ScalarDef,
    value: &Value,
    required: bool,
) -> Vec<ValidationError> {
    if value.is_null() {
        return if required {
            vec![required_error(&scalar.name)]
        } else {
            Vec::new()
        };
    }
    if !is_json_value(value) {
        return vec![error(
            "type",
            format!("{} must be a JSON value.", scalar.name),
        )];
    }
    Vec::new()
}

/// JSON object and array scalars ([`structured_json_type`]), for a value
/// other than a string: the object or array is the value every generated
/// type holds, so a value of another JSON type is "type". The object or
/// array goes to the scalar core as the JSON text `JSON.stringify` writes,
/// the form the core reads, so the core checks what it holds (a string
/// map's values, a vector's numbers). A string is the value's JSON text,
/// which gets the String primitive's checks. Null is a missing value.
fn validate_structured_json_scalar_value(
    scalar: &ScalarDef,
    shape: JsonShape,
    value: &Value,
    required: bool,
) -> Vec<ValidationError> {
    if value.is_null() {
        return if required {
            vec![required_error(&scalar.name)]
        } else {
            Vec::new()
        };
    }
    if json_shape_of(value) != Some(shape) || !is_json_value(value) {
        return vec![error(
            "type",
            format!("{} must be a JSON {}.", scalar.name, shape.as_str()),
        )];
    }
    if !scalars::has_validator(&scalar.name) {
        return Vec::new();
    }
    scalars::validate(&scalar.name, &stringify(value))
}

/// Object-valued scalars (primitive `Type`), which no scalar of the core
/// catalog has. Only the shape (an object) is checked; the scalar core,
/// which reads a string, is not consulted.
fn validate_type_scalar_value(
    scalar: &ScalarDef,
    value: &Value,
    required: bool,
) -> Vec<ValidationError> {
    match value {
        Value::Null if required => vec![required_error(&scalar.name)],
        Value::Null | Value::Object(_) => Vec::new(),
        _ => vec![error("type", format!("{} must be an object.", scalar.name))],
    }
}

/// Bytes-valued scalars (primitive `Bytes`): a base64 string in JSON.
fn validate_bytes_scalar_value(
    scalar: &ScalarDef,
    value: &Value,
    required: bool,
) -> Vec<ValidationError> {
    match value {
        Value::Null if required => vec![required_error(&scalar.name)],
        Value::Null | Value::String(_) => Vec::new(),
        _ => vec![error("type", format!("{} must be bytes.", scalar.name))],
    }
}

fn validate_field_level_numeric_constraints(field: &FieldDef, number: f64) -> Vec<ValidationError> {
    let mut errors = Vec::new();
    if let Some(minimum) = field.validate_min {
        if number < minimum {
            errors.push(error(
                "min",
                format!("{} must be at least {}.", field.name, js_number(minimum)),
            ));
        }
    }
    if let Some(maximum) = field.validate_max {
        if number > maximum {
            errors.push(error(
                "max",
                format!("{} must be at most {}.", field.name, js_number(maximum)),
            ));
        }
    }
    errors
}

/// The "type" error for a builtin field value of the wrong JSON type. The
/// IR's string, number and boolean are checked, and so are the GraphQL
/// names; any other name is not. The field's own constraints are checked
/// only on a value that passes.
fn builtin_type_error(field: &FieldDef, value: &Value) -> Option<ValidationError> {
    let (valid, message) = match field.type_ref.name.as_str() {
        "string" | "String" | "ID" => (value.is_string(), "expected a string"),
        "number" | "Float" => (to_number(value).is_some(), "expected a number"),
        "Int" => (
            to_number(value).is_some_and(|n| n.fract() == 0.0),
            "expected an integer",
        ),
        "boolean" | "Boolean" => (value.is_boolean(), "expected a boolean"),
        _ => return None,
    };
    if valid {
        None
    } else {
        Some(ValidationError::new("type", message))
    }
}

fn has_field_level_constraints(field: &FieldDef) -> bool {
    field.validate_min.is_some()
        || field.validate_max.is_some()
        || field.validate_min_length.is_some()
        || field.validate_max_length.is_some()
        || !field.validate_pattern.is_empty()
}

/// The errors of data as a value of the named object type; empty when it
/// is valid or the schema has no such type. A value that is not an object
/// is read as an empty one.
pub(crate) fn validate_schema_type(
    schema: &Schema,
    patterns: &Patterns,
    type_name: &str,
    data: &Value,
) -> FieldErrors {
    validate_in(&schema.types, schema, patterns, type_name, data)
}

/// The errors of data as a value of the named input type, as
/// [`validate_schema_type`] reads an object type.
pub(crate) fn validate_schema_input(
    schema: &Schema,
    patterns: &Patterns,
    input_name: &str,
    data: &Value,
) -> FieldErrors {
    validate_in(&schema.inputs, schema, patterns, input_name, data)
}

fn validate_in(
    defs: &std::collections::BTreeMap<String, TypeDef>,
    schema: &Schema,
    patterns: &Patterns,
    name: &str,
    data: &Value,
) -> FieldErrors {
    let Some(type_def) = defs.get(name) else {
        return FieldErrors::new();
    };
    let empty = Map::new();
    let object = data.as_object().unwrap_or(&empty);
    Validator { schema, patterns }.validate_type_def(type_def, object)
}
