//! The scalar functions validation and parse call, over superscalar's core
//! (D3: scalar behavior lives in superscalar, and the runtime adds
//! nothing). Each is the TypeScript runtime's default registry entry for
//! the same scalar, which calls the same core through superscalar's
//! TypeScript binding:
//!
//! - validate: every builtin scalar, the core's lenient coercion of the
//!   value, its failure reported under the core's own name (`parse`,
//!   `range`, `custom`, ...);
//! - parse: the builtin scalars the catalog marks with a custom parse step,
//!   the core's canonical text (its strict parse for `Temporal.DateTime`
//!   and the JSON object scalars), a failure being `parse`;
//! - normalize: the builtin scalars the catalog marks with a custom
//!   normalize step, the core's canonical text, or the input when the core
//!   cannot read it.
//!
//! A registry key is a scalar's canonical name (`Contact.Email`); the
//! runtime's underscore names (`Contact_Email`) are read as the dotted
//! ones.

use serde_json::Value;
use superscalar::Registry;

use crate::jsnum::js_number;
use crate::reader::builtin_scalars;
use superschematic_schema_runtime::ValidationError;

/// The canonical name a registry key names: `Contact_Email` and
/// `Contact.Email` both name `Contact.Email`.
fn canonical_key(name: &str) -> String {
    let trimmed = name.trim();
    if trimmed.contains('.') || !trimmed.contains('_') {
        trimmed.to_owned()
    } else {
        trimmed.replace('_', ".")
    }
}

fn registry() -> &'static Registry {
    Registry::builtin()
}

/// Whether the core validates the named scalar.
pub(crate) fn has_validator(name: &str) -> bool {
    registry().def(&canonical_key(name)).is_some()
}

/// The core's verdict on a scalar's string value (or a structured
/// scalar's JSON text). An empty string is no value and passes.
pub(crate) fn validate(name: &str, value: &str) -> Vec<ValidationError> {
    if value.is_empty() {
        return Vec::new();
    }
    let canonical = canonical_key(name);
    let Some(def) = registry().def(&canonical) else {
        return Vec::new();
    };
    match registry()
        .coerce_lenient(&Value::String(value.to_owned()), def.canonical)
        .error
    {
        Some(error) => vec![ValidationError::new(error.kind.as_str(), error.message)],
        None => Vec::new(),
    }
}

/// The builtin catalog row of the named scalar, by its canonical name.
fn catalog_row(canonical: &str) -> Option<&'static crate::ScalarDef> {
    builtin_scalars().get(&canonical.replace('.', "_"))
}

/// The label a parse failure names: the canonical name after its
/// namespace (`DateTime` for `Temporal.DateTime`).
fn label(canonical: &str) -> &str {
    canonical
        .split_once('.')
        .map_or(canonical, |(_, rest)| rest)
}

/// The text TypeScript's `String(value)` gives the core's coerced value.
fn value_text(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Number(number) => number
            .as_f64()
            .map_or_else(|| number.to_string(), js_number),
        Value::Bool(flag) => flag.to_string(),
        Value::Null => "null".to_owned(),
        other => other.to_string(),
    }
}

/// The parse step's result: the canonical text, or the errors to report
/// with the input kept.
pub(crate) type Parsed = Result<String, Vec<ValidationError>>;

/// The named scalar's parse step, when the core has one the runtime calls:
/// a builtin scalar the catalog marks with a custom parse step.
pub(crate) fn parse(name: &str, input: &str) -> Option<Parsed> {
    let canonical = canonical_key(name);
    let row = catalog_row(&canonical)?;
    if !row.has_custom_parse {
        return None;
    }
    let def = registry().def(&canonical)?;
    let failed = || {
        vec![ValidationError::new(
            "parse",
            format!("invalid {}", label(def.canonical)),
        )]
    };
    // Temporal.DateTime keeps the core's exact RFC 3339 text, and a JSON
    // object scalar its canonical JSON text, through the core's strict
    // parse.
    let strict = def.canonical == "Temporal.DateTime"
        || row.type_mappings.get("json_schema").map(String::as_str) == Some("object");
    if strict {
        let scalar = registry().scalar(def.canonical)?;
        return Some(scalar.parse(registry(), input).map_err(|_| failed()));
    }
    let coerced = registry().coerce_lenient(&Value::String(input.to_owned()), def.canonical);
    Some(match (coerced.value, coerced.error) {
        (Some(value), None) => Ok(value_text(&value)),
        _ => Err(failed()),
    })
}

/// The named scalar's normalize step, when the core has one the runtime
/// calls: the canonical text, or the input when the core cannot read it.
pub(crate) fn normalize(name: &str, input: &str) -> Option<String> {
    let canonical = canonical_key(name);
    let row = catalog_row(&canonical)?;
    if !row.has_custom_normalize {
        return None;
    }
    let def = registry().def(&canonical)?;
    let coerced = registry().coerce_lenient(&Value::String(input.to_owned()), def.canonical);
    Some(match (coerced.value, coerced.error) {
        (Some(value), None) => value_text(&value),
        _ => input.to_owned(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn registry_keys_read_underscores_as_dots() {
        assert_eq!(canonical_key("Contact_Email"), "Contact.Email");
        assert_eq!(canonical_key(" Contact.Email "), "Contact.Email");
        assert_eq!(canonical_key("Plain"), "Plain");
        assert!(has_validator("Contact_Email"));
        assert!(!has_validator("Acme_Sku"));
    }

    #[test]
    fn validate_names_the_cores_failure() {
        assert!(validate("Contact_Email", "").is_empty());
        assert!(validate("Contact_Email", "a@b.co").is_empty());
        let errors = validate("Geo_Location", r#"{"lat":91,"lon":0}"#);
        assert_eq!(errors.len(), 1);
        assert_eq!(errors[0].validator, "range");
    }

    #[test]
    fn parse_and_normalize_follow_the_catalog() {
        assert_eq!(
            parse("Finance_Money", " 12345 "),
            Some(Ok("12345".to_owned()))
        );
        assert_eq!(
            parse("Temporal_DateTime", "2026-01-15T12:00:00Z"),
            Some(Ok("2026-01-15T12:00:00Z".to_owned()))
        );
        let refused = parse("Temporal_DateTime", "soon").unwrap().unwrap_err();
        assert_eq!(refused[0].validator, "parse");
        assert_eq!(refused[0].message, "invalid DateTime");
        assert_eq!(parse("Contact_Email", "A@B.CO"), None);
        assert_eq!(
            normalize("Contact_Email", " A@B.CO "),
            Some("a@b.co".to_owned())
        );
        assert_eq!(
            normalize("Contact_Email", "not an email"),
            Some("not an email".to_owned())
        );
        assert_eq!(normalize("Finance_Money", "1"), None);
    }
}
