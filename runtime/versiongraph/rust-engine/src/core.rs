//! The core's operations over trees of canonical rows, called natively.
//!
//! The core (crate `superschematic-versiongraph`) takes one JSON document and
//! returns one; this module builds each request from typed values and reads
//! each result back into them.

use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{json, Value};
use superschematic_versiongraph::Op;

use crate::Error;

/// A tree of canonical rows by kind. A kind with no rows is absent, so the
/// kinds are in name order.
pub type Tree = BTreeMap<String, Vec<Value>>;

/// A problem in a tree that compose reports and validate lists: `code` is
/// `absent_parent`, `duplicate_entity_key`, `singleton`, `parent_cycle` or
/// `order_out_of_range`, and `entity_key` is `None` for a finding about a
/// whole kind.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Finding {
    pub code: String,
    pub kind: String,
    #[serde(rename = "entityKey", default, skip_serializing_if = "Option::is_none")]
    pub entity_key: Option<String>,
    pub message: String,
}

/// Which input a [`Resolution`] takes a unit's value from.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Take {
    Base,
    Ours,
    Theirs,
}

/// Settles the conflict at `path` of one entity, by taking one input's
/// value (`take`) or by giving the value (`value`, where `Some(Value::Null)`
/// is a value). A whole-entity conflict (path `""`) takes a side.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Resolution {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub take: Option<Take>,
    #[serde(
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub value: Option<Value>,
}

/// One unit both sides of a merge changed differently, or an edit against a
/// delete (path `""`). `base`, `ours` and `theirs` are the unit's canonical
/// values, `None` where the unit is absent on that side; the authors are
/// each side's author column.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Conflict {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub path: String,
    #[serde(
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub base: Option<Value>,
    #[serde(
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub ours: Option<Value>,
    #[serde(
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub theirs: Option<Value>,
    #[serde(
        rename = "oursAuthor",
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub ours_author: Option<Value>,
    #[serde(
        rename = "theirsAuthor",
        default,
        deserialize_with = "present",
        skip_serializing_if = "Option::is_none"
    )]
    pub theirs_author: Option<Value>,
}

/// Which input an entity's merged result came from: `side` is `ours`
/// (nothing to write onto ours), `theirs`, `merged` or `conflict`, and
/// `deleted` is true when the result is a delete.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Outcome {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub side: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub deleted: bool,
}

/// One entity two trees differ on: `operation` is `ADD`, `UPDATE` or
/// `DELETE`, and `row` is the later tree's row (its tombstone for a
/// `DELETE`, when it has one).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Change {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub operation: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub row: Option<Value>,
}

/// A merge's result: the merged tree, the conflicts left, and every entity's
/// outcome.
#[derive(Debug, Clone)]
pub(crate) struct MergeOutput {
    pub merged: Tree,
    pub conflicts: Vec<Conflict>,
    pub entities: Vec<Outcome>,
}

/// `"value": null` is a value (JSON null); only a missing member is `None`.
fn present<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<Value>, D::Error> {
    Value::deserialize(deserializer).map(Some)
}

/// Runs one core operation on a request document.
fn run(op: Op, request: &Value) -> Result<Value, Error> {
    let input = serde_json::to_vec(request)
        .map_err(|e| Error::Invalid(format!("encode the {} request: {e}", op.name())))?;
    Ok(superschematic_versiongraph::run(op, &input)?)
}

fn decode<T: for<'de> Deserialize<'de>>(op: Op, value: Value) -> Result<T, Error> {
    serde_json::from_value(value)
        .map_err(|e| Error::Invalid(format!("decode the {} result: {e}", op.name())))
}

/// A tree as the core reads it.
pub(crate) fn tree_json(tree: &Tree) -> Value {
    Value::Object(
        tree.iter()
            .map(|(kind, rows)| (kind.clone(), Value::Array(rows.clone())))
            .collect(),
    )
}

/// A tree the core returned, without the kinds that have no rows.
pub(crate) fn decode_tree(value: Value) -> Result<Tree, Error> {
    let Value::Object(members) = value else {
        return Err(Error::Invalid("decode tree: not an object".to_owned()));
    };
    let mut tree = Tree::new();
    for (kind, rows) in members {
        let Value::Array(rows) = rows else {
            return Err(Error::Invalid(format!("decode tree: {kind} is not a list")));
        };
        if !rows.is_empty() {
            tree.insert(kind, rows);
        }
    }
    Ok(tree)
}

/// The core over one descriptor.
#[derive(Debug, Clone)]
pub(crate) struct Core {
    pub descriptor: Value,
}

impl Core {
    pub fn compose(&self, base: &Tree, overlay: &Tree) -> Result<(Tree, Vec<Finding>), Error> {
        let mut out = run(
            Op::Compose,
            &json!({"descriptor": self.descriptor, "base": tree_json(base), "overlay": tree_json(overlay)}),
        )?;
        let tree = decode_tree(out["tree"].take())?;
        let findings = decode(Op::Compose, out["findings"].take())?;
        Ok((tree, findings))
    }

    pub fn merge(
        &self,
        base: &Tree,
        ours: &Tree,
        theirs: &Tree,
        resolutions: &[Resolution],
    ) -> Result<MergeOutput, Error> {
        let mut request = json!({
            "descriptor": self.descriptor,
            "base": tree_json(base),
            "ours": tree_json(ours),
            "theirs": tree_json(theirs),
        });
        if !resolutions.is_empty() {
            request["resolutions"] = serde_json::to_value(resolutions)
                .map_err(|e| Error::Invalid(format!("encode resolutions: {e}")))?;
        }
        let mut out = run(Op::Merge, &request)?;
        Ok(MergeOutput {
            merged: decode_tree(out["merged"].take())?,
            conflicts: decode(Op::Merge, out["conflicts"].take())?,
            entities: decode(Op::Merge, out["entities"].take())?,
        })
    }

    pub fn diff(&self, from: &Tree, to: &Tree) -> Result<Vec<Change>, Error> {
        let mut out = run(
            Op::Diff,
            &json!({"descriptor": self.descriptor, "from": tree_json(from), "to": tree_json(to)}),
        )?;
        decode(Op::Diff, out["changes"].take())
    }

    pub fn content_hash(&self, tree: &Tree) -> Result<String, Error> {
        let mut out = run(
            Op::ContentHash,
            &json!({"descriptor": self.descriptor, "tree": tree_json(tree)}),
        )?;
        decode(Op::ContentHash, out["contentHash"].take())
    }

    pub fn validate(&self, tree: &Tree) -> Result<Vec<Finding>, Error> {
        let mut out = run(
            Op::Validate,
            &json!({"descriptor": self.descriptor, "tree": tree_json(tree)}),
        )?;
        decode(Op::Validate, out["findings"].take())
    }
}
