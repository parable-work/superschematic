//! What a generated `parse_<type>` shares: filling `@default` values,
//! refusing undeclared keys, and the error that says which step failed.

use std::fmt;

use serde_json::{Map, Value};

use crate::errors::ValidationErrors;

/// Whether a parse refuses a top-level key its type does not declare.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UnknownFields {
    Allow,
    Refuse,
}

/// Why a `parse_<type>` refused its input.
#[derive(Debug)]
pub enum ParseError {
    /// The input is not a JSON object.
    NotAnObject,
    /// The input holds top-level keys the type does not declare, in the
    /// order the input holds them.
    UnknownFields(Vec<String>),
    /// The input fails the type's validator.
    Invalid(ValidationErrors),
    /// The validated input does not decode into the Rust type.
    Decode(serde_json::Error),
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ParseError::NotAnObject => f.write_str("expected a JSON object"),
            ParseError::UnknownFields(keys) => write!(f, "unknown fields: {}", keys.join(", ")),
            ParseError::Invalid(errors) => write!(f, "validation failed: {errors}"),
            ParseError::Decode(error) => write!(f, "decode failed: {error}"),
        }
    }
}

impl std::error::Error for ParseError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            ParseError::Decode(error) => Some(error),
            ParseError::Invalid(errors) => Some(errors),
            _ => None,
        }
    }
}

/// Sets key to the `@default` value written as JSON text when the object
/// does not hold it, or holds null and `replace_null` is set (a type that is
/// not `@strictJSON`, whose null field is a missing one).
pub fn fill_default(
    object: &mut Map<String, Value>,
    key: &str,
    default_json: &str,
    replace_null: bool,
) {
    let missing = match object.get(key) {
        None => true,
        Some(Value::Null) => replace_null,
        Some(_) => false,
    };
    if missing {
        if let Ok(value) = serde_json::from_str(default_json) {
            object.insert(key.to_owned(), value);
        }
    }
}

/// Refuses the object's keys that known does not list, when policy refuses
/// them.
pub fn check_unknown_fields(
    object: &Map<String, Value>,
    known: &[&str],
    policy: UnknownFields,
) -> Result<(), ParseError> {
    if policy == UnknownFields::Allow {
        return Ok(());
    }
    let unknown: Vec<String> = object
        .keys()
        .filter(|key| !known.contains(&key.as_str()))
        .cloned()
        .collect();
    if unknown.is_empty() {
        Ok(())
    } else {
        Err(ParseError::UnknownFields(unknown))
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn defaults_fill_missing_and_null_keys() {
        let mut object = json!({"a": null, "b": 2}).as_object().unwrap().clone();
        fill_default(&mut object, "a", "1", false);
        fill_default(&mut object, "b", "1", true);
        fill_default(&mut object, "c", r#""x""#, false);
        assert_eq!(
            Value::Object(object.clone()),
            json!({"a": null, "b": 2, "c": "x"})
        );
        fill_default(&mut object, "a", "[]", true);
        assert_eq!(object["a"], json!([]));
    }

    #[test]
    fn unknown_fields_are_refused_by_policy() {
        let object = json!({"a": 1, "z": 2}).as_object().unwrap().clone();
        assert!(check_unknown_fields(&object, &["a"], UnknownFields::Allow).is_ok());
        match check_unknown_fields(&object, &["a"], UnknownFields::Refuse) {
            Err(ParseError::UnknownFields(keys)) => assert_eq!(keys, ["z"]),
            other => panic!("unexpected {other:?}"),
        }
        assert!(check_unknown_fields(&object, &["a", "z"], UnknownFields::Refuse).is_ok());
    }

    #[test]
    fn errors_say_which_step_failed() {
        assert_eq!(
            ParseError::NotAnObject.to_string(),
            "expected a JSON object"
        );
        assert_eq!(
            ParseError::UnknownFields(vec!["z".into(), "y".into()]).to_string(),
            "unknown fields: z, y"
        );
    }
}
