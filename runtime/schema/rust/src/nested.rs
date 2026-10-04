//! Checks a generated validator applies to the structure of a field's value:
//! a nested object, the rows of a `T[][]` field, and list elements that must
//! not be null.

use serde_json::Value;

use crate::errors::{ScalarResult, ValidationError, ValidationErrors};
use crate::path::index_path;

/// Validates a nested object at path as its own type: null passes (presence
/// is the field's check), a value that is not a JSON object is one `type`
/// error, and an object's errors nest under path.
pub fn check_nested(
    errors: &mut ValidationErrors,
    path: String,
    value: &Value,
    validate: fn(&Value) -> ValidationErrors,
) {
    match value {
        Value::Null => {}
        Value::Object(_) => errors.nest(path, validate(value)),
        _ => errors.add(path, "type", "expected an object"),
    }
}

/// Validates a nested object of a `@strictJSON` type's field: a missing or
/// null object is `required`, and an object that fails its type is one
/// `object` error whose message is the nested errors as JSON. This is the
/// `validate_<type>_required` a generated validator exposes.
pub fn require_object(
    value: Option<&Value>,
    validate: fn(&Value) -> ValidationErrors,
) -> ScalarResult {
    let value = match value {
        None | Some(Value::Null) => return Err(vec![ValidationError::required()]),
        Some(value) => value,
    };
    let nested = validate(value);
    if nested.is_empty() {
        return Ok(());
    }
    let message = serde_json::to_string(&nested).unwrap_or_default();
    Err(vec![ValidationError::new("object", message)])
}

/// The rows of a `T[][]` field: a null row is `required` and a row that is
/// not a list is `type`, at `path[i]`. A list row passes.
pub fn check_rows(errors: &mut ValidationErrors, path: &str, rows: &[Value]) {
    for (index, row) in rows.iter().enumerate() {
        match row {
            Value::Null => errors.push(index_path(path, index), ValidationError::required()),
            Value::Array(_) => {}
            _ => errors.add(index_path(path, index), "type", "expected an array"),
        }
    }
}

/// A list element is never null: a null element's one error is `required`,
/// in place of whatever the element checks made of it (D12, amended).
pub fn require_elements(errors: &mut ValidationErrors, path: &str, items: &[Value]) {
    for (index, item) in items.iter().enumerate() {
        if item.is_null() {
            errors.set(index_path(path, index), vec![ValidationError::required()]);
        }
    }
}

/// [`require_elements`] for each list row of a `T[][]` field.
pub fn require_grid_elements(errors: &mut ValidationErrors, path: &str, rows: &[Value]) {
    for (index, row) in rows.iter().enumerate() {
        if let Value::Array(items) = row {
            require_elements(errors, &index_path(path, index), items);
        }
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;
    use crate::errors::Entry;

    fn validate_point(value: &Value) -> ValidationErrors {
        let mut errors = ValidationErrors::new();
        if value.get("x").is_none() {
            errors.push("x", ValidationError::required());
        }
        errors
    }

    #[test]
    fn nested_objects_nest_their_errors() {
        let mut errors = ValidationErrors::new();
        check_nested(&mut errors, "a".into(), &json!({}), validate_point);
        check_nested(&mut errors, "b".into(), &json!({"x": 1}), validate_point);
        check_nested(&mut errors, "c".into(), &json!(null), validate_point);
        check_nested(&mut errors, "d".into(), &json!([1]), validate_point);
        assert!(matches!(errors.get("a"), Some(Entry::Nested(_))));
        assert!(errors.get("b").is_none() && errors.get("c").is_none());
        assert_eq!(errors.errors_at("d").unwrap()[0].validator, "type");
    }

    #[test]
    fn a_required_object_reports_its_errors_as_one() {
        assert_eq!(
            require_object(None, validate_point),
            Err(vec![ValidationError::required()])
        );
        assert_eq!(
            require_object(Some(&json!({"x": 1})), validate_point),
            Ok(())
        );
        let failed = require_object(Some(&json!({})), validate_point).unwrap_err();
        assert_eq!(failed[0].validator, "object");
        assert_eq!(
            failed[0].message,
            r#"{"x":[{"validator":"required","message":"required field"}]}"#
        );
    }

    #[test]
    fn rows_and_elements() {
        let mut errors = ValidationErrors::new();
        let grid = json!([null, "x", [], [1, null]]);
        let rows = grid.as_array().unwrap();
        check_rows(&mut errors, "g", rows);
        require_grid_elements(&mut errors, "g", rows);
        require_elements(&mut errors, "l", json!([1, null]).as_array().unwrap());
        let flat = errors.flatten();
        let keys: Vec<&str> = flat.keys().map(String::as_str).collect();
        assert_eq!(keys, ["g[0]", "g[1]", "g[3][1]", "l[1]"]);
        assert_eq!(flat["g[1]"][0].validator, "type");
        assert_eq!(flat["l[1]"][0].validator, "required");
    }
}
