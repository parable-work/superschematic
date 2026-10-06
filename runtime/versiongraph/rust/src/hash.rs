//! `content_hash`: a digest of a tree's content that ignores bookkeeping.

use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::descriptor::Graph;
use crate::tree::Tree;

/// The canonical document the hash covers: for each kind with live rows,
/// `{"<kind>": [{"entityKey": ..., "content": {...}}, ...]}` with rows sorted
/// by entity key and only content columns kept, less each content column
/// the descriptor declares whose value is null. A declared content column
/// the row lacks reads as null (`Row::content`), so its null and its absence
/// hash the same, and a column a descriptor adds, which every row written
/// before it lacks or holds null, moves no hash, as a kind it adds moves
/// none. A content column the descriptor does not declare is hashed as the
/// row holds it, null included, as compare, merge and diff read it, so two
/// trees hash the same exactly when their diff is empty. Only the row's own
/// members are left out: a null inside a `json` value is content. Tombstone
/// rows are left out, as an absent row is. [`content_hash`] writes it with
/// every object's keys sorted and no whitespace.
pub fn canonical(graph: &Graph, tree: &Tree) -> Value {
    let mut out = Map::new();
    for (kind, rows) in graph.kinds.iter().zip(&tree.kinds) {
        let mut live: Vec<_> = rows.iter().filter(|row| !row.deleted).collect();
        if live.is_empty() {
            continue;
        }
        live.sort_by(|a, b| a.key.cmp(&b.key));
        let entries = live
            .into_iter()
            .map(|row| {
                let content: Map<String, Value> = row
                    .content(kind)
                    .into_iter()
                    .filter(|(column, value)| {
                        !(value.is_null() && kind.content_columns().contains(column))
                    })
                    .collect();
                serde_json::json!({ "entityKey": row.key, "content": content })
            })
            .collect();
        out.insert(kind.name.clone(), Value::Array(entries));
    }
    Value::Object(out)
}

/// SHA-256, as lowercase hex, of the canonical document's JSON bytes.
pub fn content_hash(graph: &Graph, tree: &Tree) -> Value {
    let mut text = String::new();
    write_sorted(&mut text, &canonical(graph, tree));
    let digest = Sha256::digest(text);
    let hex: String = digest.iter().map(|byte| format!("{byte:02x}")).collect();
    serde_json::json!({ "contentHash": hex })
}

/// Writes a JSON value with no whitespace and every object's keys sorted, at
/// every depth, so the bytes hashed do not depend on the order `serde_json`'s
/// map keeps: with its `preserve_order` feature on, which another crate in a
/// build can turn on, a map keeps insertion order instead of sorting. Keys
/// sort by code point (their UTF-8 bytes), as a `BTreeMap<String, _>` sorts
/// them.
fn write_sorted(out: &mut String, value: &Value) {
    match value {
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            out.push('{');
            for (i, key) in keys.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&Value::String(key.clone()).to_string());
                out.push(':');
                write_sorted(out, &members[key.as_str()]);
            }
            out.push('}');
        }
        Value::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_sorted(out, item);
            }
            out.push(']');
        }
        scalar => out.push_str(&scalar.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use serde_json::{json, Value};

    use crate::{run, Op};

    /// A descriptor of one kind, step, with the content columns given beside
    /// its role columns.
    fn descriptor(content: &[(&str, &str)]) -> Value {
        let mut columns = json!({
            "id": "uuid", "entity_key": "uuid", "ref": "uuid",
            "deleted_on_ref": "boolean", "_version": "integer",
        });
        for (column, class) in content {
            columns[*column] = json!(class);
        }
        json!({
            "version": 3,
            "root": {"table": "recipe", "key": "id"},
            "refTable": "recipe_ref", "commitTable": "recipe_commit",
            "patchTable": "recipe_patch", "releaseTable": "recipe_release",
            "snapshotTable": "recipe_snapshot_entry",
            "kinds": [{
                "kind": "step", "table": "step", "historyTable": "step_history",
                "key": "entity_key", "id": "id", "ref": "ref",
                "tombstone": "deleted_on_ref", "version": "_version",
                "history": {"exclude": []},
                "columns": columns,
            }],
        })
    }

    /// One live step row: its role columns and the members given.
    fn step(members: Value) -> Value {
        let mut row = json!({
            "id": "r1", "entity_key": "k1", "ref": "a",
            "deleted_on_ref": false, "_version": 1,
        });
        for (column, value) in members.as_object().expect("members").clone() {
            row[column] = value;
        }
        json!({ "step": [row] })
    }

    fn hash(descriptor: &Value, tree: &Value) -> String {
        let input = json!({ "descriptor": descriptor, "tree": tree }).to_string();
        let output = run(Op::ContentHash, input.as_bytes()).expect("content_hash");
        output["contentHash"]
            .as_str()
            .expect("contentHash")
            .to_owned()
    }

    fn canonical(descriptor: &Value, tree: &Value) -> Value {
        let graph = crate::descriptor::Graph::from_value(descriptor.clone()).expect("descriptor");
        let tree = crate::tree::parse(&graph, "tree", tree.clone()).expect("tree");
        super::canonical(&graph, &tree)
    }

    const COLUMNS: [(&str, &str); 3] =
        [("title", "string"), ("note", "string"), ("extras", "json")];

    #[test]
    fn the_canonical_document_leaves_out_null_content_members() {
        let tree = step(json!({"title": "Boil", "note": null, "extras": {"salt": null}}));
        assert_eq!(
            canonical(&descriptor(&COLUMNS), &tree),
            json!({"step": [{"entityKey": "k1", "content": {"title": "Boil", "extras": {"salt": null}}}]})
        );
        // A live row whose every content column is null is still a row.
        let empty = step(json!({"title": null}));
        assert_eq!(
            canonical(&descriptor(&COLUMNS), &empty),
            json!({"step": [{"entityKey": "k1", "content": {}}]})
        );
        assert_ne!(
            hash(&descriptor(&COLUMNS), &empty),
            hash(&descriptor(&COLUMNS), &json!({}))
        );
    }

    #[test]
    fn a_null_content_column_hashes_as_an_absent_one() {
        let descriptor = descriptor(&COLUMNS);
        let absent = hash(&descriptor, &step(json!({"title": "Boil"})));
        assert_eq!(
            hash(
                &descriptor,
                &step(json!({"title": "Boil", "note": null, "extras": null}))
            ),
            absent
        );
        // A content column the descriptor does not declare is hashed as the
        // row holds it, so its null is content, as diff reads it.
        assert_ne!(
            hash(&descriptor, &step(json!({"title": "Boil", "aside": null}))),
            absent
        );
    }

    /// Whether `diff` from `from` to `to` finds no change.
    fn no_change(descriptor: &Value, from: &Value, to: &Value) -> bool {
        let input = json!({ "descriptor": descriptor, "from": from, "to": to }).to_string();
        let output = run(Op::Diff, input.as_bytes()).expect("diff");
        output["changes"].as_array().expect("changes").is_empty()
    }

    #[test]
    fn two_trees_hash_the_same_exactly_when_their_diff_is_empty() {
        let descriptor = descriptor(&COLUMNS);
        let boil = step(json!({"title": "Boil"}));
        let pairs = [
            // A declared content column: null and absent are one value.
            (boil.clone(), step(json!({"title": "Boil", "note": null}))),
            (
                boil.clone(),
                step(json!({"title": "Boil", "note": null, "extras": null})),
            ),
            (boil.clone(), step(json!({"title": "Boil", "note": ""}))),
            // A column the descriptor does not declare: null is a value.
            (boil.clone(), step(json!({"title": "Boil", "aside": null}))),
            (boil.clone(), step(json!({"title": "Boil", "aside": 1}))),
            (
                step(json!({"title": "Boil", "aside": null})),
                step(json!({"title": "Boil", "aside": null})),
            ),
            // A null inside a json value is content.
            (
                step(json!({"title": "Boil", "extras": {}})),
                step(json!({"title": "Boil", "extras": {"salt": null}})),
            ),
            (
                step(json!({"title": "Boil", "extras": []})),
                step(json!({"title": "Boil", "extras": [null]})),
            ),
            // A row whose every content column is null is still a row.
            (json!({}), step(json!({"title": null}))),
        ];
        for (from, to) in &pairs {
            assert_eq!(
                hash(&descriptor, from) == hash(&descriptor, to),
                no_change(&descriptor, from, to),
                "{from} -> {to}"
            );
        }
    }

    #[test]
    fn a_column_a_descriptor_adds_moves_no_hash() {
        let tree = step(json!({"title": "Boil"}));
        let before = hash(&descriptor(&[("title", "string")]), &tree);
        assert_eq!(hash(&descriptor(&COLUMNS), &tree), before);
        let filled = step(json!({"title": "Boil", "note": null, "extras": null}));
        assert_eq!(hash(&descriptor(&COLUMNS), &filled), before);
    }

    #[test]
    fn a_value_and_a_null_inside_a_json_value_move_the_hash() {
        let descriptor = descriptor(&COLUMNS);
        let absent = hash(&descriptor, &step(json!({"title": "Boil"})));
        let set = hash(&descriptor, &step(json!({"title": "Boil", "note": ""})));
        assert_ne!(set, absent);
        let empty = hash(&descriptor, &step(json!({"title": "Boil", "extras": {}})));
        let nested = hash(
            &descriptor,
            &step(json!({"title": "Boil", "extras": {"salt": null}})),
        );
        assert_ne!(empty, absent);
        assert_ne!(nested, empty);
        let listed = hash(
            &descriptor,
            &step(json!({"title": "Boil", "extras": [null]})),
        );
        assert_ne!(
            listed,
            hash(&descriptor, &step(json!({"title": "Boil", "extras": []})))
        );
    }
}
