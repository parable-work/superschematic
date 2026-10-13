//! JSON merge patch (RFC 7386), the engine's update rule, the port of
//! `src/instances/patch.ts` (D16): a member of the patch replaces the
//! instance's member, a nested object merges into the nested object, null
//! removes the member, and an array or any other value replaces what was
//! there.
//!
//! An object keeps its members in the order a JavaScript object would hold
//! them after the same steps (a removed member is gone from its place, a
//! new one goes last), and the engine writes it with
//! [`crate::json::stringify`], which orders array-index keys as
//! JavaScript does, so what is stored is the TypeScript engine's text.

use serde_json::{Map, Value};

use crate::json::js_equal;

/// `target` with `patch` applied; neither changes.
pub fn merge_patch(target: &Value, patch: &Value) -> Value {
    let Value::Object(members) = patch else {
        return patch.clone();
    };
    let mut result = match target {
        Value::Object(target) => target.clone(),
        _ => Map::new(),
    };
    for (key, value) in members {
        if value.is_null() {
            result.shift_remove(key);
        } else {
            let merged = merge_patch(result.get(key).unwrap_or(&Value::Null), value);
            result.insert(key.clone(), merged);
        }
    }
    Value::Object(result)
}

/// The merge patch that turns `before` into `after`: each member that is
/// new or changed, a nested object changed member by member, and null for
/// a member `after` no longer has. Applying it to `before` with
/// [`merge_patch`] gives `after`, for values that hold no null.
pub fn diff_patch(before: &Map<String, Value>, after: &Map<String, Value>) -> Map<String, Value> {
    let mut patch = Map::new();
    for (key, value) in after {
        let previous = before.get(key);
        if previous.is_some_and(|previous| js_equal(previous, value)) {
            continue;
        }
        let changed = match (previous, value) {
            (Some(Value::Object(previous)), Value::Object(value)) => {
                Value::Object(diff_patch(previous, value))
            }
            _ => value.clone(),
        };
        patch.insert(key.clone(), changed);
    }
    for key in before.keys() {
        if !after.contains_key(key) {
            patch.insert(key.clone(), Value::Null);
        }
    }
    patch
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::json::stringify;
    use serde_json::json;

    #[test]
    fn a_patch_merges_objects_removes_nulls_and_replaces_the_rest() {
        let target = json!({"title": "Desk", "address": {"street": "1 Main St", "city": "Oslo"}, "parcels": [1, 2]});
        let merged = merge_patch(
            &target,
            &json!({"address": {"city": "Bergen", "street": null}, "parcels": [3], "note": "x"}),
        );
        assert_eq!(
            stringify(&merged),
            r#"{"title":"Desk","address":{"city":"Bergen"},"parcels":[3],"note":"x"}"#
        );
        assert_eq!(merge_patch(&target, &json!("text")), json!("text"));
        assert_eq!(
            merge_patch(&json!(3), &json!({"a": {"b": 1}})),
            json!({"a": {"b": 1}})
        );
        // A removed member is gone from its place: one set again goes last.
        assert_eq!(
            stringify(&merge_patch(&json!({"a": 1, "b": 2}), &json!({"a": null}))),
            r#"{"b":2}"#
        );
    }

    #[test]
    fn a_diff_is_the_patch_between_two_values() {
        let before =
            json!({"title": "Desk", "quantity": 1, "address": {"city": "Oslo", "zip": "1"}});
        let after = json!({"title": "Desk", "status": "open", "address": {"city": "Bergen", "zip": "1"}, "quantity": 1.0});
        let patch = diff_patch(before.as_object().unwrap(), after.as_object().unwrap());
        assert_eq!(
            Value::Object(patch.clone()),
            json!({"status": "open", "address": {"city": "Bergen"}})
        );
        assert_eq!(
            merge_patch(&before, &Value::Object(patch)),
            merge_patch(
                &before,
                &json!({"status": "open", "address": {"city": "Bergen"}})
            )
        );
        assert_eq!(
            Value::Object(diff_patch(before.as_object().unwrap(), &Map::new())),
            json!({"title": null, "quantity": null, "address": null})
        );
    }
}
