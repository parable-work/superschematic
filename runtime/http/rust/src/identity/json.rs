//! Reading the identity config and the routes' inputs as every identity
//! runtime reads them: a member matches a field by its name exactly, a
//! member that matches none is refused, and a null member is the same as an
//! absent one.

use serde_json::{Map, Value};

/// The value of each of `names` in `object`, in the order of `names`, with
/// null read as absent. A member no name matches is an error naming it.
pub(crate) fn members<'a>(
    object: &'a Map<String, Value>,
    names: &[&str],
) -> Result<Vec<Option<&'a Value>>, String> {
    let mut values = vec![None; names.len()];
    for (key, value) in object {
        let index = names
            .iter()
            .position(|name| name == key)
            .ok_or_else(|| format!("unknown member {key:?}"))?;
        values[index] = (!value.is_null()).then_some(value);
    }
    Ok(values)
}

/// A JSON number that is an integer literal, as a Go `int64` takes one.
pub(crate) fn int64(value: &Value, member: &str) -> Result<i64, String> {
    value
        .as_i64()
        .filter(|_| is_integer_literal(value))
        .ok_or_else(|| format!("{member} must be an integer"))
}

/// A JSON number that is an integer literal from 0 to 2^32 - 1, as a Go
/// `uint32` takes one.
pub(crate) fn uint32(value: &Value, member: &str) -> Result<u32, String> {
    value
        .as_u64()
        .filter(|_| is_integer_literal(value))
        .and_then(|n| u32::try_from(n).ok())
        .ok_or_else(|| format!("{member} must be an integer from 0 to 4294967295"))
}

/// Whether a number was written without a fraction or an exponent, which
/// serde_json keeps as an integer (and as its text under
/// `arbitrary_precision`).
fn is_integer_literal(value: &Value) -> bool {
    value.as_number().is_some_and(|number| {
        let text = number.to_string();
        !text.contains(['.', 'e', 'E'])
    })
}

pub(crate) fn boolean(value: &Value, member: &str) -> Result<bool, String> {
    value
        .as_bool()
        .ok_or_else(|| format!("{member} must be a boolean"))
}

pub(crate) fn string(value: &Value, member: &str) -> Result<String, String> {
    value
        .as_str()
        .map(str::to_owned)
        .ok_or_else(|| format!("{member} must be a string"))
}

/// A list of strings, a null element read as the empty string, as Go reads
/// one into a `[]string`.
pub(crate) fn strings(value: &Value, member: &str) -> Result<Vec<String>, String> {
    let items = value
        .as_array()
        .ok_or_else(|| format!("{member} must be a list of strings"))?;
    items
        .iter()
        .map(|item| match item {
            Value::Null => Ok(String::new()),
            Value::String(s) => Ok(s.clone()),
            _ => Err(format!("{member} must be a list of strings")),
        })
        .collect()
}

pub(crate) fn object<'a>(value: &'a Value, member: &str) -> Result<&'a Map<String, Value>, String> {
    value
        .as_object()
        .ok_or_else(|| format!("{member} must be an object"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_member_matches_exactly() {
        let value = json!({"login": "a", "password": "b", "session": null});
        let found = members(
            value.as_object().unwrap(),
            &["login", "password", "session"],
        )
        .unwrap();
        assert_eq!(found[0], Some(&json!("a")));
        assert_eq!(found[1], Some(&json!("b")));
        assert_eq!(found[2], None);
        let err = members(value.as_object().unwrap(), &["login"]).unwrap_err();
        assert!(err.contains("password"), "{err}");
        let other_case = json!({"Login": "a"});
        assert!(members(other_case.as_object().unwrap(), &["login"]).is_err());
    }

    #[test]
    fn an_integer_is_written_as_one() {
        assert_eq!(int64(&json!(3600), "n"), Ok(3600));
        assert_eq!(int64(&json!(-1), "n"), Ok(-1));
        assert!(int64(&json!(1.5), "n").is_err());
        assert!(int64(&serde_json::from_str::<Value>("1.0").unwrap(), "n").is_err());
        assert!(int64(&serde_json::from_str::<Value>("1e3").unwrap(), "n").is_err());
        assert!(int64(&json!("3600"), "n").is_err());
        assert_eq!(uint32(&json!(19456), "n"), Ok(19456));
        assert!(uint32(&json!(-1), "n").is_err());
        assert!(uint32(&json!(4_294_967_296_u64), "n").is_err());
    }
}
