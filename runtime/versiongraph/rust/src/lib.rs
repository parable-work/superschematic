//! The version-graph core: compose, merge, diff, hash and validate trees of
//! versioned rows.
//!
//! Every operation takes one JSON document and returns one. A graph
//! descriptor says, per entity kind, which row columns are the entity key,
//! row id, ref, tombstone, version and author, how a kind nests under
//! another, which column orders siblings, and how each content column merges.
//! Rows are JSON objects keyed by column name, as Postgres `to_jsonb` renders
//! them. The core is pure: no IO, clock or randomness. The Go binding calls it
//! through the C ABI in [`ffi`], and the browser through the same exports
//! built for `wasm32-unknown-unknown`. `runtime/versiongraph/README.md` is the
//! contract; the vectors in `runtime/versiongraph/testdata/vectors` are its
//! executable form.

mod compose;
mod descriptor;
mod diff;
mod error;
pub mod ffi;
mod finding;
mod hash;
mod merge;
mod tree;
mod validate;

use serde_json::{Map, Value};

pub use error::Error;
pub use merge::{Resolution, Take};

/// An operation of the core.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Op {
    Compose,
    Merge,
    Diff,
    ContentHash,
    Validate,
}

impl Op {
    pub const ALL: [Op; 5] = [
        Op::Compose,
        Op::Merge,
        Op::Diff,
        Op::ContentHash,
        Op::Validate,
    ];

    /// The operation's name, as the vectors spell it.
    pub fn name(self) -> &'static str {
        match self {
            Op::Compose => "compose",
            Op::Merge => "merge",
            Op::Diff => "diff",
            Op::ContentHash => "content_hash",
            Op::Validate => "validate",
        }
    }

    pub fn from_name(name: &str) -> Option<Op> {
        Op::ALL.into_iter().find(|op| op.name() == name)
    }
}

/// Run one operation on a JSON input and return its output document.
pub fn run(op: Op, input: &[u8]) -> Result<Value, Error> {
    let input: Value = serde_json::from_slice(input)
        .map_err(|e| Error::json(format!("{}: input: {e}", op.name())))?;
    match op {
        Op::Compose => {
            let mut m = members(op, input, &["descriptor", "base", "overlay"], &[])?;
            let graph = descriptor::Graph::from_value(take(&mut m, "descriptor"))?;
            let base = checked(&graph, "base", take(&mut m, "base"))?;
            let overlay = checked(&graph, "overlay", take(&mut m, "overlay"))?;
            Ok(compose::compose(&graph, &base, &overlay))
        }
        Op::Merge => {
            let mut m = members(
                op,
                input,
                &["descriptor", "base", "ours", "theirs"],
                &["resolutions"],
            )?;
            let graph = descriptor::Graph::from_value(take(&mut m, "descriptor"))?;
            let base = checked(&graph, "base", take(&mut m, "base"))?;
            let ours = checked(&graph, "ours", take(&mut m, "ours"))?;
            let theirs = checked(&graph, "theirs", take(&mut m, "theirs"))?;
            let resolutions = match m.remove("resolutions") {
                None => Vec::new(),
                Some(value) => serde_json::from_value(value)
                    .map_err(|e| Error::resolution(format!("resolutions: {e}")))?,
            };
            merge::merge(&graph, &base, &ours, &theirs, resolutions)
        }
        Op::Diff => {
            let mut m = members(op, input, &["descriptor", "from", "to"], &[])?;
            let graph = descriptor::Graph::from_value(take(&mut m, "descriptor"))?;
            let from = checked(&graph, "from", take(&mut m, "from"))?;
            let to = checked(&graph, "to", take(&mut m, "to"))?;
            Ok(diff::diff(&graph, &from, &to))
        }
        Op::ContentHash => {
            let mut m = members(op, input, &["descriptor", "tree"], &[])?;
            let graph = descriptor::Graph::from_value(take(&mut m, "descriptor"))?;
            let tree = checked(&graph, "tree", take(&mut m, "tree"))?;
            Ok(hash::content_hash(&graph, &tree))
        }
        Op::Validate => {
            let mut m = members(op, input, &["descriptor", "tree"], &[])?;
            let graph = descriptor::Graph::from_value(take(&mut m, "descriptor"))?;
            let tree = tree::parse(&graph, "tree", take(&mut m, "tree"))?;
            Ok(validate::validate(&graph, &tree))
        }
    }
}

/// Run one operation and render its result: the output document and `true`,
/// or `{"error":{"code","message"}}` and `false`.
pub fn run_json(op: Op, input: &[u8]) -> (String, bool) {
    match run(op, input) {
        Ok(output) => (output.to_string(), true),
        Err(error) => (error_document(&error), false),
    }
}

pub(crate) fn error_document(error: &Error) -> String {
    serde_json::json!({ "error": error }).to_string()
}

/// The input's members, refusing a missing required one or an unknown one.
fn members(
    op: Op,
    input: Value,
    required: &[&str],
    optional: &[&str],
) -> Result<Map<String, Value>, Error> {
    let name = op.name();
    let Value::Object(members) = input else {
        return Err(Error::request(format!(
            "{name}: the input is a JSON object"
        )));
    };
    for member in required {
        if !members.contains_key(*member) {
            return Err(Error::request(format!("{name}: missing member {member:?}")));
        }
    }
    if let Some(unknown) = members
        .keys()
        .find(|k| !required.contains(&k.as_str()) && !optional.contains(&k.as_str()))
    {
        return Err(Error::request(format!(
            "{name}: unknown member {unknown:?}"
        )));
    }
    Ok(members)
}

fn take(members: &mut Map<String, Value>, member: &str) -> Value {
    members.remove(member).unwrap_or(Value::Null)
}

fn checked(graph: &descriptor::Graph, side: &str, value: Value) -> Result<tree::Tree, Error> {
    let tree = tree::parse(graph, side, value)?;
    tree::check(graph, side, &tree)?;
    Ok(tree)
}
