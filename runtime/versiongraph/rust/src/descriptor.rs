//! The graph descriptor: which columns of each kind's rows play which role,
//! and how each content column merges.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde::Deserialize;
use serde_json::Value;

use crate::error::Error;

/// The descriptor as it crosses the boundary.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Descriptor {
    /// The graph's name, for the code that wrote the descriptor. The core
    /// does not read it.
    #[serde(default, rename = "graph")]
    _graph: Option<String>,
    kinds: Vec<KindDescriptor>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct KindDescriptor {
    kind: String,
    key: String,
    id: String,
    #[serde(rename = "ref")]
    ref_column: String,
    tombstone: String,
    version: String,
    #[serde(default)]
    author: Option<String>,
    #[serde(default)]
    parent: Option<ParentDescriptor>,
    #[serde(default)]
    order: Option<String>,
    #[serde(default)]
    singleton: bool,
    #[serde(default)]
    units: BTreeMap<String, Unit>,
    #[serde(default)]
    excluded: Vec<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct ParentDescriptor {
    key: String,
    kind: String,
}

/// How a content column merges.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Unit {
    /// The whole column is one unit.
    Atomic,
    /// Each top-level key of a JSON object is a unit.
    Keyed,
    /// A JSON Schema object: each entry of `properties` (recursively), each
    /// name's membership in `required`, and every other keyword.
    JsonSchema,
}

/// The containment edge of a kind: `column` holds the parent row's entity key,
/// and `kind` is the parent's kind (an index into [`Graph::kinds`]).
#[derive(Debug, Clone)]
pub struct Parent {
    pub column: String,
    pub kind: usize,
}

/// One entity kind of the graph.
#[derive(Debug, Clone)]
pub struct Kind {
    pub name: String,
    pub key: String,
    pub id: String,
    pub ref_column: String,
    pub tombstone: String,
    pub version: String,
    pub author: Option<String>,
    pub parent: Option<Parent>,
    pub order: Option<String>,
    pub singleton: bool,
    units: BTreeMap<String, Unit>,
    excluded: BTreeSet<String>,
}

impl Kind {
    /// Whether `column` is content: not one of the graph's role columns and
    /// not excluded. Only content is compared, merged, diffed and hashed.
    pub fn is_content(&self, column: &str) -> bool {
        !self.is_role(column) && !self.excluded.contains(column)
    }

    fn is_role(&self, column: &str) -> bool {
        column == self.key
            || column == self.id
            || column == self.ref_column
            || column == self.tombstone
            || column == self.version
            || self.author.as_deref() == Some(column)
    }

    /// The merge unit of a content column; `atomic` unless the descriptor
    /// says otherwise.
    pub fn unit(&self, column: &str) -> Unit {
        self.units.get(column).copied().unwrap_or(Unit::Atomic)
    }
}

/// A checked descriptor.
#[derive(Debug, Clone)]
pub struct Graph {
    pub kinds: Vec<Kind>,
    index: HashMap<String, usize>,
}

impl Graph {
    pub fn kind_index(&self, name: &str) -> Option<usize> {
        self.index.get(name).copied()
    }

    /// Read and check a descriptor.
    pub fn from_value(value: Value) -> Result<Self, Error> {
        let raw: Descriptor = serde_json::from_value(value)
            .map_err(|e| Error::descriptor(format!("descriptor: {e}")))?;
        let mut index = HashMap::new();
        for (i, kind) in raw.kinds.iter().enumerate() {
            if kind.kind.is_empty() {
                return Err(Error::descriptor(format!(
                    "descriptor: kinds[{i}] has an empty kind name"
                )));
            }
            if index.insert(kind.kind.clone(), i).is_some() {
                return Err(Error::descriptor(format!(
                    "descriptor: kind {:?} is declared more than once",
                    kind.kind
                )));
            }
        }
        let mut kinds = Vec::with_capacity(raw.kinds.len());
        for raw_kind in raw.kinds {
            kinds.push(check_kind(raw_kind, &index)?);
        }
        Ok(Self { kinds, index })
    }
}

fn check_kind(raw: KindDescriptor, index: &HashMap<String, usize>) -> Result<Kind, Error> {
    let name = raw.kind;
    let roles = [
        ("key", &raw.key),
        ("id", &raw.id),
        ("ref", &raw.ref_column),
        ("tombstone", &raw.tombstone),
        ("version", &raw.version),
    ];
    let mut seen: BTreeMap<&str, &str> = BTreeMap::new();
    for (role, column) in roles
        .iter()
        .map(|(r, c)| (*r, c.as_str()))
        .chain(raw.author.as_deref().map(|c| ("author", c)))
    {
        if column.is_empty() {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} has an empty {role} column"
            )));
        }
        if let Some(other) = seen.insert(column, role) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} uses column {column:?} as both {other} and {role}"
            )));
        }
    }
    let parent = match raw.parent {
        None => None,
        Some(p) => {
            let Some(&kind) = index.get(&p.kind) else {
                return Err(Error::descriptor(format!(
                    "descriptor: kind {name:?} has parent kind {:?}, which the descriptor does not declare",
                    p.kind
                )));
            };
            Some(Parent {
                column: p.key,
                kind,
            })
        }
    };
    let excluded: BTreeSet<String> = raw.excluded.into_iter().collect();
    let kind = Kind {
        name,
        key: raw.key,
        id: raw.id,
        ref_column: raw.ref_column,
        tombstone: raw.tombstone,
        version: raw.version,
        author: raw.author,
        parent,
        order: raw.order,
        singleton: raw.singleton,
        units: raw.units,
        excluded,
    };
    let structural = kind
        .parent
        .as_ref()
        .map(|p| ("parent key", p.column.as_str()))
        .into_iter()
        .chain(kind.order.as_deref().map(|c| ("order", c)));
    for (role, column) in structural {
        if !kind.is_content(column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {:?} {role} column {column:?} must be a content column, not a role or excluded column",
                kind.name
            )));
        }
    }
    for column in kind.units.keys() {
        if !kind.is_content(column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {:?} gives a conflict unit to {column:?}, which is a role or excluded column",
                kind.name
            )));
        }
    }
    Ok(kind)
}
