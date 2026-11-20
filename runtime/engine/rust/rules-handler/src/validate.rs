//! A row's own fields held to its type's version, as a table's columns and
//! their CHECKs hold them (D83): each field's kind, a required field's
//! value, a string's length, a number's bounds, a list's length, an enum's
//! values; a member the type does not declare, a field a behavior owns and
//! the key are refused. A value is kept in its stored form: an instant as
//! `toISOString`'s text, a UUID in its base62 wire form, a whole number as
//! an integer.

use serde_json::{Map, Number, Value};
use superschematic_rules::ValidationIssue;

use crate::document::{Field, Kind, Model};
use crate::values::{base62, instant_of, iso_of, uuid_of};

/// Whether own fields are a create's, which takes each field's default and
/// holds every required field to a value, or a merge patch's.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Create,
    Patch,
}

/// The issues with own fields of a type as a write gives them, and the
/// fields in their stored form: for a create, every field with a value
/// after the defaults; for a patch, the patch itself, a null clearing a
/// field.
pub fn own_fields(
    model: &Model,
    data: &Map<String, Value>,
    mode: Mode,
) -> (Map<String, Value>, Vec<ValidationIssue>) {
    let mut issues = Vec::new();
    let mut out = Map::new();
    for name in data.keys() {
        if model.field(name).is_some() {
            continue;
        }
        if name == &model.key.name {
            issues.push(ValidationIssue::new(
                format!("/{name}"),
                "key",
                format!(
                    "{name} is {}'s key, which a write does not change",
                    model.name
                ),
            ));
        } else if let Some(owned) = model.owned_field(name) {
            let behavior = model
                .use_of(&owned.use_id)
                .map_or("", |used| used.spec.behavior.as_str());
            issues.push(ValidationIssue::new(
                format!("/{name}"),
                "owned",
                format!("{name} is {behavior}'s: only its operations change it"),
            ));
        } else {
            issues.push(ValidationIssue::new(
                format!("/{name}"),
                "unknown_field",
                format!("{} declares no field {name}", model.name),
            ));
        }
    }
    for field in &model.fields {
        let path = format!("/{}", field.name);
        match data.get(&field.name) {
            None | Some(Value::Null) => {
                let cleared = data.contains_key(&field.name);
                match mode {
                    Mode::Create => {
                        if let Some(default) = &field.default {
                            match value(field, default, &path) {
                                Ok(stored) => {
                                    out.insert(field.name.clone(), stored);
                                }
                                Err(found) => issues.extend(found),
                            }
                        } else if field.required {
                            issues.push(ValidationIssue::new(
                                &path,
                                "required",
                                format!("{} is required", field.name),
                            ));
                        }
                    }
                    Mode::Patch if cleared => {
                        if field.required {
                            issues.push(ValidationIssue::new(
                                &path,
                                "required",
                                format!("{} is required, so a patch cannot clear it", field.name),
                            ));
                        } else {
                            out.insert(field.name.clone(), Value::Null);
                        }
                    }
                    Mode::Patch => {}
                }
            }
            Some(given) => match value(field, given, &path) {
                Ok(stored) => {
                    out.insert(field.name.clone(), stored);
                }
                Err(found) => issues.extend(found),
            },
        }
    }
    (out, issues)
}

/// The issues a stored row's own fields have under a type's version, every
/// field held as a create holds it but defaults: what a publish checks of
/// each row it keeps.
pub fn stored_fields(model: &Model, data: &Map<String, Value>) -> Vec<ValidationIssue> {
    let mut issues = Vec::new();
    for field in &model.fields {
        let path = format!("/{}", field.name);
        match data.get(&field.name) {
            None | Some(Value::Null) => {
                if field.required {
                    issues.push(ValidationIssue::new(
                        &path,
                        "required",
                        format!(
                            "{} is required, and the row holds no value of it",
                            field.name
                        ),
                    ));
                }
            }
            Some(held) => {
                if let Err(found) = value(field, held, &path) {
                    issues.extend(found);
                }
            }
        }
    }
    issues
}

/// A field's value in its stored form, or its issues.
pub fn value(field: &Field, given: &Value, path: &str) -> Result<Value, Vec<ValidationIssue>> {
    if !field.list {
        return scalar(field, given, path).map_err(|issue| vec![issue]);
    }
    let Value::Array(items) = given else {
        return Err(vec![ValidationIssue::new(
            path,
            "type",
            format!("{} must be a list", field.name),
        )]);
    };
    let mut issues = Vec::new();
    let length = items.len() as u64;
    if field.list_min.is_some_and(|min| length < min) {
        issues.push(ValidationIssue::new(
            path,
            "minItems",
            format!(
                "{} must hold at least {} items",
                field.name,
                field.list_min.unwrap_or_default()
            ),
        ));
    }
    if field.list_max.is_some_and(|max| length > max) {
        issues.push(ValidationIssue::new(
            path,
            "maxItems",
            format!(
                "{} must hold at most {} items",
                field.name,
                field.list_max.unwrap_or_default()
            ),
        ));
    }
    let mut out = Vec::with_capacity(items.len());
    for (index, item) in items.iter().enumerate() {
        match scalar(field, item, &format!("{path}/{index}")) {
            Ok(stored) => out.push(stored),
            Err(issue) => issues.push(issue),
        }
    }
    if issues.is_empty() {
        Ok(Value::Array(out))
    } else {
        Err(issues)
    }
}

fn type_issue(field: &Field, path: &str, what: &str) -> ValidationIssue {
    ValidationIssue::new(path, "type", format!("{} must be {what}", field.name))
}

fn scalar(field: &Field, given: &Value, path: &str) -> Result<Value, ValidationIssue> {
    let stored = match &field.kind {
        Kind::Text => {
            let Value::String(text) = given else {
                return Err(type_issue(field, path, "a string"));
            };
            let length = text.chars().count() as u64;
            if let Some(min) = field.min_length.filter(|min| length < *min) {
                return Err(ValidationIssue::new(
                    path,
                    "minLength",
                    format!("{} must be at least {min} characters long", field.name),
                ));
            }
            if let Some(max) = field.max_length.filter(|max| length > *max) {
                return Err(ValidationIssue::new(
                    path,
                    "maxLength",
                    format!("{} must be at most {max} characters long", field.name),
                ));
            }
            given.clone()
        }
        Kind::Number => {
            if !given.is_number() {
                return Err(type_issue(field, path, "a number"));
            }
            given.clone()
        }
        Kind::Integer => {
            let whole = match given {
                Value::Number(number) => number.as_i64().or_else(|| {
                    number
                        .as_f64()
                        .filter(|n| n.fract() == 0.0 && n.abs() < 9.007_199_254_740_992e15)
                        .map(|n| n as i64)
                }),
                _ => None,
            };
            let Some(whole) = whole else {
                return Err(type_issue(field, path, "a whole number"));
            };
            Value::Number(Number::from(whole))
        }
        Kind::Bool => {
            if !given.is_boolean() {
                return Err(type_issue(field, path, "a boolean"));
            }
            given.clone()
        }
        Kind::Uuid => {
            let Some(uuid) = given.as_str().and_then(uuid_of) else {
                return Err(ValidationIssue::new(
                    path,
                    "format",
                    format!(
                        "{} must be a UUID, hyphenated or in its base62 form",
                        field.name
                    ),
                ));
            };
            Value::String(base62(uuid))
        }
        Kind::DateTime => {
            let Some(text) = instant_of(given).and_then(iso_of) else {
                return Err(ValidationIssue::new(
                    path,
                    "format",
                    format!("{} must be a date-time (ISO 8601)", field.name),
                ));
            };
            Value::String(text)
        }
        Kind::Date => {
            let ok = given.as_str().is_some_and(is_date);
            if !ok {
                return Err(ValidationIssue::new(
                    path,
                    "format",
                    format!("{} must be a date, YYYY-MM-DD", field.name),
                ));
            }
            given.clone()
        }
        Kind::Json => given.clone(),
        Kind::Enum(values) => {
            let ok = given
                .as_str()
                .is_some_and(|text| values.iter().any(|value| value == text));
            if !ok {
                return Err(ValidationIssue::new(
                    path,
                    "enum",
                    format!("{} must be one of {}", field.name, values.join(", ")),
                ));
            }
            given.clone()
        }
        Kind::Object(type_name) => {
            if !given.is_object() {
                return Err(type_issue(
                    field,
                    path,
                    &format!("an object of {type_name}"),
                ));
            }
            given.clone()
        }
    };
    if let (Some(n), true) = (
        stored.as_f64(),
        matches!(field.kind, Kind::Number | Kind::Integer),
    ) {
        if let Some(min) = field.min.filter(|min| n < *min) {
            return Err(ValidationIssue::new(
                path,
                "minimum",
                format!("{} must be at least {min}", field.name),
            ));
        }
        if let Some(max) = field.max.filter(|max| n > *max) {
            return Err(ValidationIssue::new(
                path,
                "maximum",
                format!("{} must be at most {max}", field.name),
            ));
        }
    }
    Ok(stored)
}

// is_date is whether a text is a calendar date, YYYY-MM-DD, that exists.
fn is_date(text: &str) -> bool {
    let bytes = text.as_bytes();
    if bytes.len() != 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return false;
    }
    let number = |range: std::ops::Range<usize>| text.get(range)?.parse::<u32>().ok();
    let (Some(year), Some(month), Some(day)) = (number(0..4), number(5..7), number(8..10)) else {
        return false;
    };
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let days = match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return false,
    };
    (1..=days).contains(&day)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::declarations::Declarations;
    use crate::document::read;
    use serde_json::json;

    fn model() -> Model {
        read(
            &json!({
                "name": "Item",
                "behaviors": [{"name": "Workflow", "field": "status", "config": {"states": ["a", "b"]}}],
                "fields": [
                    {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 5},
                    {"name": "count", "typeRef": {"name": "Generic.Int64"}, "validateMin": 0},
                    {"name": "due", "typeRef": {"name": "Temporal.DateTime"}},
                    {"name": "owner", "typeRef": {"name": "Identity.UUID"}},
                    {"name": "day", "typeRef": {"name": "Temporal.Date"}},
                    {"name": "tags", "typeRef": {"name": "string", "isArray": true}, "validateListMax": 2},
                    {"name": "size", "typeRef": {"name": "string"}, "default": "m"}
                ]
            }),
            &Declarations::ported(),
        )
        .expect("the type reads")
    }

    #[test]
    fn a_create_is_held_to_each_field_and_stored_in_its_form() {
        let (stored, issues) = own_fields(
            &model(),
            json!({"title": "Desk", "count": 2.0, "due": "2026-02-01T01:00:00+01:00", "owner": "0190f0e2-7b1c-7d4e-8f00-000000000001", "day": "2024-02-29", "tags": ["x"]})
                .as_object()
                .expect("an object"),
            Mode::Create,
        );
        assert!(issues.is_empty(), "{issues:?}");
        assert_eq!(stored["count"], json!(2));
        assert_eq!(stored["due"], json!("2026-02-01T00:00:00.000Z"));
        assert_eq!(stored["size"], json!("m"), "a default is filled");
        assert!(stored["owner"]
            .as_str()
            .is_some_and(|owner| owner.len() <= 22));
    }

    #[test]
    fn every_issue_is_reported_with_its_rule() {
        let (_, issues) = own_fields(
            &model(),
            json!({"count": -1, "day": "2023-02-29", "tags": ["a", "b", "c"], "status": "b", "colour": "red", "id": "x"})
                .as_object()
                .expect("an object"),
            Mode::Create,
        );
        let rules: Vec<(&str, &str)> = issues
            .iter()
            .map(|issue| (issue.path.as_str(), issue.rule.as_str()))
            .collect();
        assert_eq!(
            rules,
            [
                ("/status", "owned"),
                ("/colour", "unknown_field"),
                ("/id", "key"),
                ("/title", "required"),
                ("/count", "minimum"),
                ("/day", "format"),
                ("/tags", "maxItems"),
            ]
        );
    }

    #[test]
    fn a_patch_clears_an_optional_field_and_not_a_required_one() {
        let (stored, issues) = own_fields(
            &model(),
            json!({"title": null, "count": null})
                .as_object()
                .expect("an object"),
            Mode::Patch,
        );
        assert_eq!(stored.get("count"), Some(&Value::Null));
        assert_eq!(issues.len(), 1);
        assert_eq!(issues[0].rule, "required");
    }
}
