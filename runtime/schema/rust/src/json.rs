//! The JSON type checks a generated validator runs on a present value before
//! its rules (D14: presence, then type, then rules). Each `expect_*` reports
//! one `type` error at path for a present value of another JSON type and
//! returns the value when it has the expected type. A missing or null value
//! returns `None` with no error: presence is the caller's check.

use serde_json::{Map, Number, Value};

use crate::errors::ValidationErrors;

/// True for a missing value or JSON null, which the presence checks treat
/// alike.
pub fn is_absent(value: Option<&Value>) -> bool {
    matches!(value, None | Some(Value::Null))
}

pub fn expect_string<'a>(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&'a Value>,
) -> Option<&'a str> {
    match value? {
        Value::Null => None,
        Value::String(text) => Some(text),
        _ => type_error(errors, path, "expected a string"),
    }
}

/// A finite number, as f64. A literal too large for f64, which serde_json
/// keeps when its `arbitrary_precision` feature is on, is not one.
pub fn expect_number(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&Value>,
) -> Option<f64> {
    match value? {
        Value::Null => None,
        Value::Number(number) => match finite_number(number) {
            Some(number) => Some(number),
            None => type_error(errors, path, "expected a number"),
        },
        _ => type_error(errors, path, "expected a number"),
    }
}

/// A finite number with no fractional part, as f64 for the range rules.
/// `1.0` is one, as it is to the TypeScript and Python validators.
pub fn expect_integer(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&Value>,
) -> Option<f64> {
    match value? {
        Value::Null => None,
        Value::Number(number) => match integer_value(number) {
            Some(number) => Some(number),
            None => type_error(errors, path, "expected an integer"),
        },
        _ => type_error(errors, path, "expected an integer"),
    }
}

pub fn expect_boolean(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&Value>,
) -> Option<bool> {
    match value? {
        Value::Null => None,
        Value::Bool(flag) => Some(*flag),
        _ => type_error(errors, path, "expected a boolean"),
    }
}

/// A list field (`T[]` or `T[][]`) holds a JSON array.
pub fn expect_list<'a>(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&'a Value>,
) -> Option<&'a Vec<Value>> {
    match value? {
        Value::Null => None,
        Value::Array(items) => Some(items),
        _ => type_error(errors, path, "expected an array"),
    }
}

/// An object-typed field, list element or map value holds a JSON object.
pub fn expect_object<'a>(
    errors: &mut ValidationErrors,
    path: &str,
    value: Option<&'a Value>,
) -> Option<&'a Map<String, Value>> {
    match value? {
        Value::Null => None,
        Value::Object(fields) => Some(fields),
        _ => type_error(errors, path, "expected an object"),
    }
}

/// The items of a JSON array, or `None` for any other value or none.
pub fn as_array(value: Option<&Value>) -> Option<&Vec<Value>> {
    value.and_then(Value::as_array)
}

/// The entries of a JSON object, or `None` for any other value or none.
pub fn as_object(value: Option<&Value>) -> Option<&Map<String, Value>> {
    value.and_then(Value::as_object)
}

/// The number as f64 when it is finite.
pub fn finite_number(number: &Number) -> Option<f64> {
    number.as_f64().filter(|value| value.is_finite())
}

/// The number as f64 when it is finite and has no fractional part. An i64
/// or u64 literal is one; so is a float literal such as `1.0`, which
/// `as_i64` refuses when serde_json keeps the literal's text.
pub fn integer_value(number: &Number) -> Option<f64> {
    if let Some(value) = number.as_i64() {
        #[allow(clippy::cast_precision_loss)]
        return Some(value as f64);
    }
    if let Some(value) = number.as_u64() {
        #[allow(clippy::cast_precision_loss)]
        return Some(value as f64);
    }
    finite_number(number).filter(|value| value.fract() == 0.0)
}

fn type_error<T>(errors: &mut ValidationErrors, path: &str, message: &str) -> Option<T> {
    errors.add(path, "type", message);
    None
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    type Check = fn(&mut ValidationErrors, &Value) -> bool;

    fn type_message(errors: &ValidationErrors, path: &str) -> Option<String> {
        errors.errors_at(path).map(|errs| {
            assert_eq!(errs.len(), 1);
            assert_eq!(errs[0].validator, "type");
            errs[0].message.clone()
        })
    }

    #[test]
    fn absent_and_null_values_pass_without_an_error() {
        let mut errors = ValidationErrors::new();
        assert_eq!(expect_string(&mut errors, "a", None), None);
        assert_eq!(expect_number(&mut errors, "a", Some(&Value::Null)), None);
        assert_eq!(expect_list(&mut errors, "a", Some(&Value::Null)), None);
        assert!(errors.is_empty());
        assert!(is_absent(None));
        assert!(is_absent(Some(&Value::Null)));
        assert!(!is_absent(Some(&json!(""))));
    }

    #[test]
    fn a_value_of_the_expected_type_is_returned() {
        let mut errors = ValidationErrors::new();
        assert_eq!(
            expect_string(&mut errors, "a", Some(&json!("x"))),
            Some("x")
        );
        assert_eq!(
            expect_number(&mut errors, "a", Some(&json!(1.5))),
            Some(1.5)
        );
        assert_eq!(
            expect_integer(&mut errors, "a", Some(&json!(-3))),
            Some(-3.0)
        );
        assert_eq!(
            expect_integer(&mut errors, "a", Some(&json!(2.0))),
            Some(2.0)
        );
        assert_eq!(
            expect_boolean(&mut errors, "a", Some(&json!(false))),
            Some(false)
        );
        assert_eq!(
            expect_list(&mut errors, "a", Some(&json!([1]))).map(Vec::len),
            Some(1)
        );
        assert_eq!(
            expect_object(&mut errors, "a", Some(&json!({"k": 1}))).map(Map::len),
            Some(1)
        );
        assert!(errors.is_empty());
    }

    #[test]
    fn a_value_of_another_type_is_one_type_error() {
        let cases: [(&str, Check, Value); 6] = [
            (
                "expected a string",
                |e, v| expect_string(e, "f", Some(v)).is_some(),
                json!(1),
            ),
            (
                "expected a number",
                |e, v| expect_number(e, "f", Some(v)).is_some(),
                json!("1"),
            ),
            (
                "expected an integer",
                |e, v| expect_integer(e, "f", Some(v)).is_some(),
                json!(1.5),
            ),
            (
                "expected a boolean",
                |e, v| expect_boolean(e, "f", Some(v)).is_some(),
                json!("true"),
            ),
            (
                "expected an array",
                |e, v| expect_list(e, "f", Some(v)).is_some(),
                json!({}),
            ),
            (
                "expected an object",
                |e, v| expect_object(e, "f", Some(v)).is_some(),
                json!([]),
            ),
        ];
        for (message, check, value) in cases {
            let mut errors = ValidationErrors::new();
            assert!(!check(&mut errors, &value), "{message}");
            assert_eq!(type_message(&errors, "f").as_deref(), Some(message));
        }
    }

    #[test]
    fn integer_value_reads_integral_literals_only() {
        let number = |text: &str| serde_json::from_str::<Number>(text).unwrap();
        assert_eq!(
            integer_value(&number("9007199254740993")),
            Some(9_007_199_254_740_992.0)
        );
        assert_eq!(
            integer_value(&number("18446744073709551615")),
            Some(1.844_674_407_370_955_2e19)
        );
        assert_eq!(integer_value(&number("1.0")), Some(1.0));
        assert_eq!(integer_value(&number("1e2")), Some(100.0));
        assert_eq!(integer_value(&number("1.25")), None);
    }
}
