//! The compatibility rule (D16), the port of `src/registry/compat.ts`: a new
//! version of a schema may change it only in ways every instance the live
//! version accepts still satisfies. The diff walks what an instance can
//! hold under the live version, the instance type and the types, enums and
//! scalars its fields reach, and compares each with the new version:
//!
//! - a field removed, renamed or given another JSON key, a changed type or
//!   list depth, an optional field made required, a new required field, a
//!   narrower bound (a higher minLength, min or listMin, a lower maxLength,
//!   max or listMax, a bound where there was none), a new or changed
//!   pattern, a removed enum value, a removed type or enum, and a scalar
//!   that accepts less are refused;
//! - a new optional field, a new enum value, a wider or removed bound, a
//!   required field made optional, and any change that does not affect
//!   which values validate (descriptions, defaults, UI metadata) pass.
//!
//! The walk also starts at each type a behavior holds values to
//! (`checked`), as a type a field reaches. Scalars are compared as the
//! schema runtime reads them: a scalar the builtin catalog holds is the
//! catalog's, whatever the document declares for it.

use std::collections::{BTreeSet, VecDeque};

use serde_json::Value;

use superschematic_schema_ir::{parse_schema_ir, Schema};

use super::document::{
    array_depth, field_name, fields_of, is_map, json_key, number, ref_kind, runtime_document,
    scalar_key, text, type_name, type_of, RefKind, SchemaModel,
};
use crate::errors::SchemaChange;
use crate::json::{self, compare_utf16};

/// Every change from `before` to `after` the rule refuses; empty means
/// compatible. `checked` names the types besides the instance type that
/// hold values the live version checked, which the walk starts from too.
pub(crate) fn incompatible_changes(
    before: &SchemaModel,
    after: &SchemaModel,
    checked: &[String],
) -> Vec<SchemaChange> {
    let mut changes = Vec::new();
    if before.instance_type != after.instance_type {
        changes.push(SchemaChange::new(
            before.instance_type.clone(),
            format!(
                "the instance type changes from {} to {}",
                before.instance_type, after.instance_type
            ),
        ));
        return changes;
    }

    let mut enums: BTreeSet<String> = BTreeSet::new();
    let mut scalars: BTreeSet<String> = BTreeSet::new();
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut queue: VecDeque<String> = std::iter::once(before.instance_type.clone())
        .chain(checked.iter().cloned())
        .collect();
    while let Some(type_name) = queue.pop_front() {
        if !seen.insert(type_name.clone()) {
            continue;
        }
        if type_of(&before.document, &type_name).is_none() {
            continue;
        }
        if type_of(&after.document, &type_name).is_none() {
            changes.push(SchemaChange::new(
                type_name.clone(),
                format!("type {type_name} is removed"),
            ));
            continue;
        }
        // A field that is removed or changes type is reported as such;
        // what its old type reaches matters only through the fields that
        // keep it.
        let kept = compare_fields(
            &type_name,
            fields_of(&before.document, &type_name),
            fields_of(&after.document, &type_name),
            &mut changes,
        );
        for field in kept {
            let referenced = self::type_name(field).to_owned();
            match ref_kind(&before.document, &referenced) {
                RefKind::Type => queue.push_back(referenced),
                RefKind::Enum => {
                    enums.insert(referenced);
                }
                RefKind::Scalar => {
                    scalars.insert(referenced);
                }
                _ => {}
            }
        }
    }

    for enum_name in sorted(enums) {
        let old_enum = before
            .document
            .get("enums")
            .and_then(|enums| enums.get(&enum_name));
        let new_enum = after
            .document
            .get("enums")
            .and_then(|enums| enums.get(&enum_name));
        if let Some(old_enum) = old_enum {
            compare_enum(&enum_name, old_enum, new_enum, &mut changes);
        }
    }

    if !scalars.is_empty() {
        let old_schema = parse_schema_ir(&runtime_document(&before.document)).ok();
        let new_schema = parse_schema_ir(&runtime_document(&after.document)).ok();
        if let (Some(old_schema), Some(new_schema)) = (old_schema, new_schema) {
            for scalar_name in sorted(scalars) {
                compare_scalar(&scalar_name, &old_schema, &new_schema, &mut changes);
            }
        }
    }
    changes
}

// JavaScript's sort of a set's names: by UTF-16 code unit.
fn sorted(names: BTreeSet<String>) -> Vec<String> {
    let mut names: Vec<String> = names.into_iter().collect();
    names.sort_by(|a, b| compare_utf16(a, b));
    names
}

// compare_fields reports the changes to one type's fields and returns the
// old fields that keep their type.
fn compare_fields<'d>(
    type_name: &str,
    old_fields: &'d [Value],
    new_fields: &[Value],
    changes: &mut Vec<SchemaChange>,
) -> Vec<&'d Value> {
    let mut kept = Vec::new();
    for old_field in old_fields {
        let path = format!("{type_name}.{}", field_name(old_field));
        // The last field of a name stands, as the TypeScript map by name
        // keeps it.
        let Some(new_field) = new_fields
            .iter()
            .rev()
            .find(|candidate| field_name(candidate) == field_name(old_field))
        else {
            changes.push(SchemaChange::new(
                path.clone(),
                format!("field {path} is removed"),
            ));
            continue;
        };
        if json_key(old_field) != json_key(new_field) {
            changes.push(SchemaChange::new(
                path.clone(),
                format!(
                    "field {path} changes its JSON key from {} to {}",
                    json_key(old_field),
                    json_key(new_field)
                ),
            ));
        }
        if self::type_name(old_field) != self::type_name(new_field)
            || array_depth(old_field) != array_depth(new_field)
            || is_map(old_field) != is_map(new_field)
        {
            changes.push(SchemaChange::new(
                path.clone(),
                format!(
                    "field {path} changes type from {} to {}",
                    type_label(old_field),
                    type_label(new_field)
                ),
            ));
            // Its bounds applied to another type; the type change says
            // enough.
            continue;
        }
        kept.push(old_field);
        let required = |field: &Value| field.get("required").and_then(Value::as_bool) == Some(true);
        if !required(old_field) && required(new_field) {
            changes.push(SchemaChange::new(
                path.clone(),
                format!("field {path} becomes required"),
            ));
        }
        let label = format!("field {path}");
        let bound = |key: &str| (number(old_field, key), number(new_field, key));
        let (before, after) = bound("validateMinLength");
        lower_bound(changes, &path, &label, "minLength", before, after);
        let (before, after) = bound("validateMaxLength");
        upper_bound(changes, &path, &label, "maxLength", before, after);
        let (before, after) = bound("validateMin");
        lower_bound(changes, &path, &label, "min", before, after);
        let (before, after) = bound("validateMax");
        upper_bound(changes, &path, &label, "max", before, after);
        let (before, after) = bound("validateListMin");
        lower_bound(changes, &path, &label, "listMin", before, after);
        let (before, after) = bound("validateListMax");
        upper_bound(changes, &path, &label, "listMax", before, after);
        pattern(
            changes,
            &label,
            &path,
            text(old_field, "validatePattern").unwrap_or_default(),
            text(new_field, "validatePattern").unwrap_or_default(),
        );
    }
    for new_field in new_fields {
        let added = !old_fields
            .iter()
            .any(|old_field| field_name(old_field) == field_name(new_field));
        if added && new_field.get("required").and_then(Value::as_bool) == Some(true) {
            let path = format!("{type_name}.{}", field_name(new_field));
            changes.push(SchemaChange::new(
                path.clone(),
                format!("field {path} is added as required"),
            ));
        }
    }
    kept
}

fn enum_values(enum_def: &Value) -> Vec<&str> {
    enum_def
        .get("values")
        .and_then(Value::as_array)
        .map(|values| {
            values
                .iter()
                .map(|value| match text(value, "serializedAs") {
                    Some(serialized) if !serialized.is_empty() => serialized,
                    _ => text(value, "name").unwrap_or_default(),
                })
                .collect()
        })
        .unwrap_or_default()
}

fn compare_enum(
    enum_name: &str,
    old_enum: &Value,
    new_enum: Option<&Value>,
    changes: &mut Vec<SchemaChange>,
) {
    let Some(new_enum) = new_enum else {
        changes.push(SchemaChange::new(
            enum_name,
            format!("enum {enum_name} is removed"),
        ));
        return;
    };
    let kept = enum_values(new_enum);
    for serialized in enum_values(old_enum) {
        if !kept.contains(&serialized) {
            changes.push(SchemaChange::new(
                enum_name,
                format!("enum {enum_name} drops value {serialized}"),
            ));
        }
    }
}

// The schema runtime's scalar rules: 0 is no length bound, None no range
// bound, '' no pattern.
fn compare_scalar(
    scalar_name: &str,
    old_schema: &Schema,
    new_schema: &Schema,
    changes: &mut Vec<SchemaChange>,
) {
    let key = scalar_key(scalar_name);
    let Some(old_scalar) = old_schema.scalars.get(&key) else {
        return;
    };
    let label = format!("scalar {scalar_name}");
    let Some(new_scalar) = new_schema.scalars.get(&key) else {
        changes.push(SchemaChange::new(
            scalar_name,
            format!("{label} is removed"),
        ));
        return;
    };
    if old_scalar.primitive != new_scalar.primitive {
        changes.push(SchemaChange::new(
            scalar_name,
            format!(
                "{label} changes primitive from {} to {}",
                old_scalar.primitive, new_scalar.primitive
            ),
        ));
    }
    let json_type = |scalar: &superschematic_schema_ir::ScalarDef| {
        scalar
            .type_mappings
            .get("json_schema")
            .cloned()
            .unwrap_or_default()
    };
    let (old_json_type, new_json_type) = (json_type(old_scalar), json_type(new_scalar));
    if old_json_type != new_json_type {
        let shown = |mapped: &str| {
            if mapped.is_empty() {
                "none".to_owned()
            } else {
                mapped.to_owned()
            }
        };
        changes.push(SchemaChange::new(
            scalar_name,
            format!(
                "{label} changes its JSON type from {} to {}",
                shown(&old_json_type),
                shown(&new_json_type)
            ),
        ));
    }
    let positive = |value: f64| (value > 0.0).then_some(value);
    lower_bound(
        changes,
        scalar_name,
        &label,
        "minLength",
        positive(old_scalar.min_length),
        positive(new_scalar.min_length),
    );
    upper_bound(
        changes,
        scalar_name,
        &label,
        "maxLength",
        positive(old_scalar.max_length),
        positive(new_scalar.max_length),
    );
    lower_bound(
        changes,
        scalar_name,
        &label,
        "minimum",
        old_scalar.minimum,
        new_scalar.minimum,
    );
    upper_bound(
        changes,
        scalar_name,
        &label,
        "maximum",
        old_scalar.maximum,
        new_scalar.maximum,
    );
    pattern(
        changes,
        &label,
        scalar_name,
        &old_scalar.pattern,
        &new_scalar.pattern,
    );
    let added: Vec<&str> = new_scalar
        .reserved_words
        .iter()
        .filter(|word| !old_scalar.reserved_words.contains(word))
        .map(String::as_str)
        .collect();
    if !added.is_empty() {
        changes.push(SchemaChange::new(
            scalar_name,
            format!("{label} reserves {}", added.join(", ")),
        ));
    }
    if (!old_scalar.case_insensitive && new_scalar.case_insensitive)
        || (!old_scalar.reserved_words_case_insensitive
            && new_scalar.reserved_words_case_insensitive)
        || (!old_scalar.reserved_words_match_partial && new_scalar.reserved_words_match_partial)
    {
        changes.push(SchemaChange::new(
            scalar_name,
            format!("{label} matches its reserved words more widely"),
        ));
    }
    if !old_scalar.has_custom_validate && new_scalar.has_custom_validate {
        changes.push(SchemaChange::new(
            scalar_name,
            format!("{label} gains a custom validator"),
        ));
    }
}

// A lower bound (minLength, min, listMin) may fall or go; it may not rise
// or appear.
fn lower_bound(
    changes: &mut Vec<SchemaChange>,
    path: &str,
    label: &str,
    rule: &str,
    before: Option<f64>,
    after: Option<f64>,
) {
    let Some(after) = after else { return };
    if before.is_some_and(|before| after <= before) {
        return;
    }
    changes.push(SchemaChange::new(
        path,
        match before {
            None => format!("{label} gains {rule} {}", json::js_number(after)),
            Some(before) => format!(
                "{label} raises {rule} from {} to {}",
                json::js_number(before),
                json::js_number(after)
            ),
        },
    ));
}

// An upper bound (maxLength, max, listMax) may rise or go; it may not fall
// or appear.
fn upper_bound(
    changes: &mut Vec<SchemaChange>,
    path: &str,
    label: &str,
    rule: &str,
    before: Option<f64>,
    after: Option<f64>,
) {
    let Some(after) = after else { return };
    if before.is_some_and(|before| after >= before) {
        return;
    }
    changes.push(SchemaChange::new(
        path,
        match before {
            None => format!("{label} gains {rule} {}", json::js_number(after)),
            Some(before) => format!(
                "{label} lowers {rule} from {} to {}",
                json::js_number(before),
                json::js_number(after)
            ),
        },
    ));
}

// Whether one pattern accepts every string another does is not decidable
// here, so a pattern may only be dropped.
fn pattern(changes: &mut Vec<SchemaChange>, label: &str, path: &str, before: &str, after: &str) {
    if after.is_empty() || after == before {
        return;
    }
    changes.push(SchemaChange::new(
        path,
        if before.is_empty() {
            format!("{label} gains a pattern")
        } else {
            format!("{label} changes its pattern")
        },
    ));
}

fn type_label(field: &Value) -> String {
    let name = type_name(field);
    if is_map(field) {
        format!("Record<string, {name}>")
    } else {
        format!("{name}{}", "[]".repeat(array_depth(field)))
    }
}
