//! JSON merge patch (RFC 7396), the rule an update applies to a row's own
//! fields, as the engine applies it (D16): a member of the patch replaces
//! the row's member, a nested object merges into the nested object, null
//! removes the member, and an array or any other value replaces what was
//! there. An object keeps its members in order: a removed member is gone
//! from its place, a new one goes last.

use serde_json::{Map, Value};

/// A row's own fields with a patch applied; neither changes.
pub fn merge_patch(target: &Map<String, Value>, patch: &Map<String, Value>) -> Map<String, Value> {
    let mut result = target.clone();
    for (key, value) in patch {
        if value.is_null() {
            result.shift_remove(key);
        } else {
            let merged = merge_value(result.get(key).unwrap_or(&Value::Null), value);
            result.insert(key.clone(), merged);
        }
    }
    result
}

fn merge_value(target: &Value, patch: &Value) -> Value {
    let Value::Object(members) = patch else {
        return patch.clone();
    };
    let empty = Map::new();
    let target = match target {
        Value::Object(target) => target,
        _ => &empty,
    };
    Value::Object(merge_patch(target, members))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn object(value: Value) -> Map<String, Value> {
        value.as_object().cloned().unwrap()
    }

    #[test]
    fn a_patch_merges_objects_removes_nulls_and_replaces_the_rest() {
        let target = object(
            json!({"title": "Desk", "address": {"street": "1 Main St", "city": "Oslo"}, "parcels": [1, 2]}),
        );
        let merged = merge_patch(
            &target,
            &object(
                json!({"address": {"city": "Bergen", "street": null}, "parcels": [3], "note": "x"}),
            ),
        );
        assert_eq!(
            serde_json::to_string(&merged).unwrap(),
            r#"{"title":"Desk","address":{"city":"Bergen"},"parcels":[3],"note":"x"}"#
        );
        // Removing a member the row does not have changes nothing.
        assert_eq!(
            merge_patch(&target, &object(json!({"status": null}))),
            target
        );
    }
}
