//! Parse of a value as a type of the runtime schema, the port of the
//! TypeScript runtime's `parse/` (`walk.ts`, `coerce.ts`, `defaults.ts`):
//! each field's default filled when it is absent, primitives coerced
//! (lenient mode reads a numeric or boolean string; strict mode refuses
//! anything not already the primitive), each scalar's normalize and parse
//! steps run through superscalar's core, nested types and lists walked,
//! and, in strict mode, a key the type does not declare refused as
//! `unknown_field`. What the parse cannot read is kept as it was, for
//! validation to report.

use std::collections::HashSet;

use serde_json::{Map, Value};

use crate::errors::FieldErrors;
use crate::jsjson::{js_string_to_number, js_trim, number_value, stringify, MAX_SAFE_INTEGER};
use crate::jsnum::js_number;
use crate::scalars;
use crate::schema::{
    is_any_json_scalar, structured_json_type, FieldDef, JsonShape, ScalarDef, Schema, TypeDef,
};
use crate::validate::{json_shape_of, resolve_ref_kind, to_number, RefKind};

/// How a parse reads its value.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ParseOptions {
    /// Refuse a value not already of its primitive, and a key the type does
    /// not declare.
    pub strict: bool,
}

/// What a parse made of a value: the parsed object and the errors it met.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ParseResult {
    pub data: Map<String, Value>,
    pub errors: FieldErrors,
}

struct Walker<'a> {
    schema: &'a Schema,
    strict: bool,
}

impl Walker<'_> {
    fn walk_type_def(&self, type_def: &TypeDef, raw: &Map<String, Value>) -> ParseResult {
        let mut data = Map::new();
        let mut errors = FieldErrors::new();
        let mut known = HashSet::new();
        for field in &type_def.fields {
            let key = field.key();
            known.insert(key);
            self.walk_field(field, key, raw, &mut data, &mut errors);
        }
        if self.strict {
            for key in raw.keys() {
                if !known.contains(key.as_str()) {
                    errors.add(key.as_str(), "unknown_field", "unknown field");
                }
            }
        }
        ParseResult { data, errors }
    }

    fn walk_field(
        &self,
        field: &FieldDef,
        key: &str,
        raw: &Map<String, Value>,
        data: &mut Map<String, Value>,
        errors: &mut FieldErrors,
    ) {
        let Some(value) = raw.get(key) else {
            if field.default_value.is_none() || field.type_ref.is_array {
                return;
            }
            let kind = resolve_ref_kind(self.schema, &field.type_ref.name);
            if matches!(kind, RefKind::Type | RefKind::Input) {
                return;
            }
            let scalar = if kind == RefKind::Scalar {
                self.schema.scalars.get(&field.type_ref.name)
            } else {
                None
            };
            match apply_default(field, scalar) {
                Some(default) => {
                    data.insert(key.to_owned(), default);
                }
                None => errors.add(
                    key,
                    "default",
                    "default value is not parseable for declared type",
                ),
            }
            return;
        };

        if value.is_null() {
            data.insert(key.to_owned(), Value::Null);
            return;
        }

        let kind = resolve_ref_kind(self.schema, &field.type_ref.name);
        let parsed = if field.type_ref.is_array {
            self.walk_array_field(field, key, kind, value, errors)
        } else {
            self.walk_value(field, key, kind, value, errors)
        };
        data.insert(key.to_owned(), parsed);
    }

    /// One value of a field, a single one or a list element, at key: its
    /// parse, or the value as it was with the error that kept it.
    fn walk_value(
        &self,
        field: &FieldDef,
        key: &str,
        kind: RefKind,
        value: &Value,
        errors: &mut FieldErrors,
    ) -> Value {
        match kind {
            RefKind::Scalar => {
                let Some(scalar) = self.schema.scalars.get(&field.type_ref.name) else {
                    return value.clone();
                };
                match self.apply_scalar(scalar, value) {
                    Some(coerced) => self.apply_scalar_parse(scalar, coerced, key, errors),
                    None => {
                        errors.add(key, "type", type_mismatch_message(scalar));
                        value.clone()
                    }
                }
            }
            RefKind::Enum => value.clone(),
            RefKind::Builtin => match apply_builtin(&field.type_ref.name, value, self.strict) {
                Some(coerced) => coerced,
                None => {
                    errors.add(
                        key,
                        "type",
                        format!("expected {} value", field.type_ref.name),
                    );
                    value.clone()
                }
            },
            RefKind::Type | RefKind::Input => {
                let defs = if kind == RefKind::Type {
                    &self.schema.types
                } else {
                    &self.schema.inputs
                };
                match (defs.get(&field.type_ref.name), value.as_object()) {
                    (Some(type_def), Some(object)) => {
                        let nested = self.walk_type_def(type_def, object);
                        errors.nest(key, nested.errors);
                        Value::Object(nested.data)
                    }
                    _ => {
                        errors.add(key, "type", "expected object value");
                        value.clone()
                    }
                }
            }
        }
    }

    fn walk_array_field(
        &self,
        field: &FieldDef,
        key: &str,
        kind: RefKind,
        value: &Value,
        errors: &mut FieldErrors,
    ) -> Value {
        let Some(items) = value.as_array() else {
            errors.add(key, "type", "expected array value");
            return value.clone();
        };
        if !field.type_ref.is_array_of_arrays {
            return Value::Array(self.walk_array_elements(field, key, kind, items, errors));
        }
        // T[][]: walk each inner list; its elements report at key[i][j]. A
        // null inner list passes through for validation to reject, as a
        // null element of T[] does.
        let mut out = Vec::with_capacity(items.len());
        for (index, inner) in items.iter().enumerate() {
            let inner_key = format!("{key}[{index}]");
            out.push(match inner {
                Value::Null => Value::Null,
                Value::Array(elements) => Value::Array(
                    self.walk_array_elements(field, &inner_key, kind, elements, errors),
                ),
                _ => {
                    errors.add(inner_key, "type", "expected an array");
                    inner.clone()
                }
            });
        }
        Value::Array(out)
    }

    fn walk_array_elements(
        &self,
        field: &FieldDef,
        key: &str,
        kind: RefKind,
        elements: &[Value],
        errors: &mut FieldErrors,
    ) -> Vec<Value> {
        elements
            .iter()
            .enumerate()
            .map(|(index, element)| {
                if element.is_null() {
                    Value::Null
                } else {
                    self.walk_value(field, &format!("{key}[{index}]"), kind, element, errors)
                }
            })
            .collect()
    }

    /// The value coerced to the scalar's primitive, and its normalize step
    /// run, or `None` when it is of the wrong JSON type.
    fn apply_scalar(&self, scalar: &ScalarDef, value: &Value) -> Option<Value> {
        // Any JSON value is one, whatever the scalar's primitive; validation
        // checks it.
        if is_any_json_scalar(scalar) {
            return Some(value.clone());
        }
        // A JSON object or array scalar takes its object or array, or the
        // value's JSON text, which the String primitive's steps below read.
        if let Some(shape) = structured_json_type(scalar) {
            if json_shape_of(value) == Some(shape) {
                return Some(value.clone());
            }
            if !value.is_string() {
                return None;
            }
        }
        match scalar.primitive.as_str() {
            "Int" => coerce_int(value, self.strict),
            "Float" => coerce_float(value, self.strict),
            "Boolean" => coerce_bool(value, self.strict),
            "String" | "" => {
                let text = value.as_str()?;
                if scalar.has_custom_normalize {
                    if let Some(normalized) = scalars::normalize(&scalar.name, text) {
                        return Some(Value::String(normalized));
                    }
                }
                Some(value.clone())
            }
            _ => Some(value.clone()),
        }
    }

    fn apply_scalar_parse(
        &self,
        scalar: &ScalarDef,
        value: Value,
        key: &str,
        errors: &mut FieldErrors,
    ) -> Value {
        if let Some(shape) = structured_json_type(scalar) {
            return parse_structured_json(scalar, shape, value, key, errors);
        }
        if !scalar.has_custom_parse {
            return value;
        }
        let input = match &value {
            Value::String(text) => text.clone(),
            other => js_string(other),
        };
        match scalars::parse(&scalar.name, &input) {
            None => value,
            Some(Err(parse_errors)) => {
                errors.set(key, parse_errors);
                value
            }
            Some(Ok(parsed)) => {
                if scalar.primitive == "Int" {
                    coerce_int(&Value::String(parsed), false).unwrap_or(value)
                } else {
                    Value::String(parsed)
                }
            }
        }
    }
}

/// Parses a value of a JSON object or array scalar
/// ([`structured_json_type`]) into that object or array, the value every
/// generated type holds. A string is the value's JSON text. When the
/// scalar has a parse step (Generic_StringMap, Geo_Location), the text, or
/// the JSON text of an object or array, goes through it and its canonical
/// text is decoded; a parse failure is the result. Text that does not
/// decode to the declared shape is kept as it is, for validation to report.
fn parse_structured_json(
    scalar: &ScalarDef,
    shape: JsonShape,
    value: Value,
    key: &str,
    errors: &mut FieldErrors,
) -> Value {
    let mut text = value.as_str().map(str::to_owned);
    if scalar.has_custom_parse {
        let input = text.clone().unwrap_or_else(|| stringify(&value));
        match scalars::parse(&scalar.name, &input) {
            None => {}
            Some(Err(parse_errors)) => {
                errors.set(key, parse_errors);
                return value;
            }
            Some(Ok(parsed)) => text = Some(parsed),
        }
    }
    let Some(text) = text else {
        return value;
    };
    match serde_json::from_str::<Value>(&text) {
        Ok(decoded) if json_shape_of(&decoded) == Some(shape) => decoded,
        _ => value,
    }
}

/// The text JavaScript's `String(value)` gives a primitive.
fn js_string(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Number(_) => to_number(value).map_or_else(|| value.to_string(), js_number),
        Value::Bool(flag) => flag.to_string(),
        Value::Null => "null".to_owned(),
        other => stringify(other),
    }
}

/// The GraphQL builtins (Int, Float, Boolean, String, ID) and the IR's
/// number and boolean. Lenient mode coerces a numeric or boolean string for
/// the IR names as it does for Float and Boolean; strict mode passes their
/// values through, and validation checks them.
fn apply_builtin(name: &str, value: &Value, strict: bool) -> Option<Value> {
    match name {
        "Int" => coerce_int(value, strict),
        "Float" => coerce_float(value, strict),
        "Boolean" => coerce_bool(value, strict),
        "number" if strict => Some(value.clone()),
        "number" => coerce_float(value, false),
        "boolean" if strict => Some(value.clone()),
        "boolean" => coerce_bool(value, false),
        "String" | "ID" => value.is_string().then(|| value.clone()),
        _ => Some(value.clone()),
    }
}

fn type_mismatch_message(scalar: &ScalarDef) -> String {
    if let Some(shape) = structured_json_type(scalar) {
        return format!("expected a JSON {} or its JSON text", shape.as_str());
    }
    match scalar.primitive.as_str() {
        "Int" => "expected integer value",
        "Float" => "expected numeric value",
        "Boolean" => "expected boolean value",
        "String" | "" => "expected string value",
        _ => "type mismatch",
    }
    .to_owned()
}

fn is_safe_integer(number: f64) -> bool {
    number.is_finite() && number.fract() == 0.0 && number.abs() <= MAX_SAFE_INTEGER
}

/// A safe integer: a JSON number that is one, or in lenient mode a string
/// holding one (`BigInt` syntax: an optionally signed decimal, or a `0x`,
/// `0o` or `0b` literal).
fn coerce_int(value: &Value, strict: bool) -> Option<Value> {
    match value {
        Value::Number(_) => to_number(value)
            .filter(|n| is_safe_integer(*n))
            .map(number_value),
        Value::String(text) if !strict => {
            let trimmed = js_trim(text);
            if trimmed.is_empty() {
                return None;
            }
            bigint_text(trimmed)
                .filter(|n| is_safe_integer(*n))
                .map(number_value)
        }
        _ => None,
    }
}

/// The integer `BigInt(text)` reads, as a float64, or `None` for text it
/// refuses.
fn bigint_text(text: &str) -> Option<f64> {
    let lower = text.get(..2).map(str::to_ascii_lowercase);
    let radix = match lower.as_deref() {
        Some("0x") => Some(16),
        Some("0o") => Some(8),
        Some("0b") => Some(2),
        _ => None,
    };
    if let Some(radix) = radix {
        let digits = &text[2..];
        if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
            return None;
        }
        return i128::from_str_radix(digits, radix)
            .ok()
            .map(|n| n as f64)
            .or(Some(f64::INFINITY));
    }
    let unsigned = text
        .strip_prefix('+')
        .or_else(|| text.strip_prefix('-'))
        .unwrap_or(text);
    if unsigned.is_empty() || !unsigned.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let magnitude = unsigned
        .parse::<i128>()
        .map(|n| n as f64)
        .unwrap_or(f64::INFINITY);
    Some(if text.starts_with('-') {
        -magnitude
    } else {
        magnitude
    })
}

/// A finite number: a JSON number, or in lenient mode a string `Number`
/// reads as one.
fn coerce_float(value: &Value, strict: bool) -> Option<Value> {
    match value {
        Value::Number(_) => to_number(value).map(|_| value.clone()),
        Value::String(text) if !strict => {
            if js_trim(text).is_empty() {
                return None;
            }
            let number = js_string_to_number(text);
            number.is_finite().then(|| number_value(number))
        }
        _ => None,
    }
}

/// A boolean: a JSON one, or in lenient mode `true` or `false` in any case.
fn coerce_bool(value: &Value, strict: bool) -> Option<Value> {
    match value {
        Value::Bool(_) => Some(value.clone()),
        Value::String(text) if !strict => match js_trim(text).to_lowercase().as_str() {
            "true" => Some(Value::Bool(true)),
            "false" => Some(Value::Bool(false)),
            _ => None,
        },
        _ => None,
    }
}

/// The value an absent field's default text stands for, by its scalar's
/// primitive or its builtin type's, or `None` when the text does not read
/// as one. Object-typed and list fields take no default.
fn apply_default(field: &FieldDef, scalar: Option<&ScalarDef>) -> Option<Value> {
    let raw = field.default_value.as_deref()?;
    let primitive = match scalar {
        Some(scalar) => scalar.primitive.as_str(),
        None => primitive_for_builtin(&field.type_ref.name),
    };
    match primitive {
        "Int" => {
            let number = js_string_to_number(raw);
            number.is_finite().then(|| number_value(number.trunc()))
        }
        "Float" => {
            let number = js_string_to_number(raw);
            number.is_finite().then(|| number_value(number))
        }
        "Boolean" => match raw {
            "true" | "1" => Some(Value::Bool(true)),
            "false" | "0" => Some(Value::Bool(false)),
            _ => None,
        },
        _ => Some(Value::String(raw.to_owned())),
    }
}

/// The primitive a builtin name stands for: a GraphQL scalar's, or the
/// IR's number, boolean and string, whose default text reads as a number,
/// a boolean and the text itself (D14, amended); empty for any other name.
fn primitive_for_builtin(name: &str) -> &'static str {
    match name {
        "Int" => "Int",
        "Float" | "number" => "Float",
        "Boolean" | "boolean" => "Boolean",
        "String" | "ID" | "string" => "String",
        _ => "",
    }
}

fn unknown(name: &str, kind: &str) -> ParseResult {
    let mut errors = FieldErrors::new();
    errors.add("", "type", format!("unknown {kind} \"{name}\""));
    ParseResult {
        data: Map::new(),
        errors,
    }
}

fn parse_in(
    defs: &std::collections::BTreeMap<String, TypeDef>,
    kind: &str,
    schema: &Schema,
    name: &str,
    data: &Value,
    options: ParseOptions,
) -> ParseResult {
    let Some(type_def) = defs.get(name) else {
        return unknown(name, kind);
    };
    let empty = Map::new();
    let object = data.as_object().unwrap_or(&empty);
    Walker {
        schema,
        strict: options.strict,
    }
    .walk_type_def(type_def, object)
}

/// Parses data as a value of the named object type. A name the schema has
/// no object type for is a `type` error at the empty path; a value that is
/// not an object is read as an empty one.
pub fn parse_type(
    schema: &Schema,
    type_name: &str,
    data: &Value,
    options: ParseOptions,
) -> ParseResult {
    parse_in(&schema.types, "type", schema, type_name, data, options)
}

/// Parses data as a value of the named input type, as [`parse_type`]
/// parses an object type.
pub fn parse_input(
    schema: &Schema,
    input_name: &str,
    data: &Value,
    options: ParseOptions,
) -> ParseResult {
    parse_in(&schema.inputs, "input", schema, input_name, data, options)
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn coerce_reads_primitives_as_the_typescript_runtime_does() {
        assert_eq!(coerce_int(&json!(5), true), Some(json!(5)));
        assert_eq!(coerce_int(&json!(5.0), true), Some(json!(5)));
        assert_eq!(coerce_int(&json!(5.5), false), None);
        assert_eq!(coerce_int(&json!(9007199254740992_u64), false), None);
        assert_eq!(coerce_int(&json!(" -12 "), false), Some(json!(-12)));
        assert_eq!(coerce_int(&json!("0x1f"), false), Some(json!(31)));
        assert_eq!(coerce_int(&json!("1.5"), false), None);
        assert_eq!(coerce_int(&json!("12"), true), None);
        assert_eq!(coerce_float(&json!(" 1.5 "), false), Some(json!(1.5)));
        assert_eq!(coerce_float(&json!("Infinity"), false), None);
        assert_eq!(coerce_float(&json!(""), false), None);
        assert_eq!(coerce_bool(&json!(" TRUE "), false), Some(json!(true)));
        assert_eq!(coerce_bool(&json!("yes"), false), None);
        assert_eq!(coerce_bool(&json!("true"), true), None);
    }

    #[test]
    fn defaults_read_by_primitive() {
        let field = |name: &str, default: &str| FieldDef {
            name: "f".to_owned(),
            default_value: Some(default.to_owned()),
            type_ref: crate::schema::TypeRef {
                name: name.to_owned(),
                ..Default::default()
            },
            ..Default::default()
        };
        assert_eq!(apply_default(&field("Int", "3.9"), None), Some(json!(3)));
        assert_eq!(
            apply_default(&field("number", "1.5"), None),
            Some(json!(1.5))
        );
        assert_eq!(
            apply_default(&field("boolean", "1"), None),
            Some(json!(true))
        );
        assert_eq!(apply_default(&field("boolean", "yes"), None), None);
        assert_eq!(apply_default(&field("Int", "x"), None), None);
        assert_eq!(apply_default(&field("string", "x"), None), Some(json!("x")));
        assert_eq!(
            apply_default(&field("Shade", "LIGHT"), None),
            Some(json!("LIGHT"))
        );
    }
}
