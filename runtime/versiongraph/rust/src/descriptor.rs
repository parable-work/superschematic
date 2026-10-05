//! The graph descriptor (version 3): which columns of each kind's rows play
//! which role, how each content column merges, and the value class of every
//! column. It also names the graph's tables and what each kind's history
//! keeps, which only a storage adapter reads.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde::Deserialize;
use serde_json::Value;

use crate::error::Error;

/// The descriptor format this core reads.
pub const VERSION: u64 = 3;

/// The longest retention a kind's history may keep, in days: the largest
/// Postgres `INTEGER`, which the prune function's `retention_days` is, so
/// every adapter can honour any retention the core accepts.
pub const MAX_RETENTION_DAYS: u64 = 2_147_483_647;

/// The descriptor as it crosses the boundary.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Descriptor {
    #[serde(rename = "version")]
    _version: u64,
    /// The graph's name, for the code that wrote the descriptor. The core
    /// does not read it.
    #[serde(default, rename = "graph")]
    _graph: Option<String>,
    root: RootDescriptor,
    ref_table: String,
    commit_table: String,
    patch_table: String,
    release_table: String,
    snapshot_table: String,
    kinds: Vec<KindDescriptor>,
}

/// The graph root's table and key column, for a storage adapter.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RootDescriptor {
    table: String,
    key: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct KindDescriptor {
    kind: String,
    table: String,
    history_table: String,
    key: String,
    id: String,
    #[serde(rename = "ref")]
    ref_column: String,
    /// The column holding the graph root's key, for a storage adapter,
    /// which writes it on every row.
    #[serde(default)]
    root: Option<String>,
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
    /// What the kind's history keeps, for a storage adapter. Every kind
    /// has it, so a descriptor written before it is refused rather than
    /// read as keeping everything forever.
    history: HistoryDescriptor,
    columns: BTreeMap<String, ValueClass>,
}

/// What a kind's history images keep. The core checks it against the
/// kind's columns and does not read it otherwise.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct HistoryDescriptor {
    /// Days of history pruning keeps; absent for no retention. Read as a
    /// value, so the check names the kind.
    #[serde(default, deserialize_with = "present")]
    retention_days: Option<Value>,
    /// The columns every history image leaves out.
    exclude: Vec<String>,
    /// The column a delete's image names its actor in; absent for none.
    #[serde(default, deserialize_with = "present")]
    actor: Option<String>,
}

/// Reads a member that is present, so an absent member is `None` and a
/// `null` one is read as the member's type, which refuses it.
fn present<'de, D, T>(deserializer: D) -> Result<Option<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    T::deserialize(deserializer).map(Some)
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

impl Unit {
    fn name(self) -> &'static str {
        match self {
            Unit::Atomic => "atomic",
            Unit::Keyed => "keyed",
            Unit::JsonSchema => "jsonSchema",
        }
    }
}

/// The class of one value: what the schema runtime's JSON for a field's type
/// distinguishes. A storage adapter normalizes each column by its class; the
/// core checks only that the descriptor's classes fit the roles.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Element {
    String,
    Integer,
    Number,
    Boolean,
    Uuid,
    DateTime,
    Date,
    Time,
    Duration,
    Enum,
    Json,
}

impl Element {
    const ALL: [(Element, &'static str); 11] = [
        (Element::String, "string"),
        (Element::Integer, "integer"),
        (Element::Number, "number"),
        (Element::Boolean, "boolean"),
        (Element::Uuid, "uuid"),
        (Element::DateTime, "dateTime"),
        (Element::Date, "date"),
        (Element::Time, "time"),
        (Element::Duration, "duration"),
        (Element::Enum, "enum"),
        (Element::Json, "json"),
    ];
}

/// A column's value class: an element class, or a list (`[]`) or list of
/// lists (`[][]`) of one.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ValueClass {
    pub element: Element,
    /// 0 for one value, 1 for a list, 2 for a list of lists.
    pub depth: u8,
}

impl ValueClass {
    fn single(element: Element) -> Self {
        Self { element, depth: 0 }
    }

    fn parse(text: &str) -> Option<Self> {
        let (base, depth) = if let Some(base) = text.strip_suffix("[][]") {
            (base, 2)
        } else if let Some(base) = text.strip_suffix("[]") {
            (base, 1)
        } else {
            (text, 0)
        };
        Element::ALL
            .iter()
            .find(|(_, name)| *name == base)
            .map(|(element, _)| Self {
                element: *element,
                depth,
            })
    }

    fn name(self) -> String {
        let base = Element::ALL
            .iter()
            .find(|(element, _)| *element == self.element)
            .map_or("", |(_, name)| name);
        format!("{base}{}", "[]".repeat(usize::from(self.depth)))
    }
}

impl<'de> Deserialize<'de> for ValueClass {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let text = String::deserialize(deserializer)?;
        ValueClass::parse(&text).ok_or_else(|| {
            serde::de::Error::custom(format!(
                "unknown value class {text:?}, expected one of {} (each also as a list, with [], or a list of lists, with [][])",
                Element::ALL
                    .iter()
                    .map(|(_, name)| format!("`{name}`"))
                    .collect::<Vec<_>>()
                    .join(", ")
            ))
        })
    }
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
    pub root: Option<String>,
    pub tombstone: String,
    pub version: String,
    pub author: Option<String>,
    pub parent: Option<Parent>,
    pub order: Option<String>,
    pub singleton: bool,
    units: BTreeMap<String, Unit>,
    excluded: BTreeSet<String>,
    columns: BTreeMap<String, ValueClass>,
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
            || self.root.as_deref() == Some(column)
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
        check_version(&value)?;
        let raw: Descriptor = serde_json::from_value(value)
            .map_err(|e| Error::descriptor(format!("descriptor: {e}")))?;
        let tables = [
            ("root table", &raw.root.table),
            ("root key", &raw.root.key),
            ("refTable", &raw.ref_table),
            ("commitTable", &raw.commit_table),
            ("patchTable", &raw.patch_table),
            ("releaseTable", &raw.release_table),
            ("snapshotTable", &raw.snapshot_table),
        ];
        if let Some((member, _)) = tables.iter().find(|(_, name)| name.is_empty()) {
            return Err(Error::descriptor(format!(
                "descriptor: the {member} is empty"
            )));
        }
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
        check_parent_classes(&kinds)?;
        Ok(Self { kinds, index })
    }
}

/// Refuse a descriptor of another format before reading its members, so a
/// version 1 descriptor (which has no `version`) is named as one.
fn check_version(value: &Value) -> Result<(), Error> {
    let Value::Object(members) = value else {
        return Ok(());
    };
    match members.get("version") {
        Some(Value::Number(n)) if n.as_u64() == Some(VERSION) => Ok(()),
        None => Err(Error::descriptor(format!(
            "descriptor: missing member \"version\"; this core reads descriptor version {VERSION}, and a descriptor without a version is version 1"
        ))),
        Some(other) => Err(Error::descriptor(format!(
            "descriptor: version {other} is not supported; this core reads descriptor version {VERSION}"
        ))),
    }
}

/// A parent key column holds the parent's entity key, so it has the class
/// of the parent kind's key column.
fn check_parent_classes(kinds: &[Kind]) -> Result<(), Error> {
    for kind in kinds {
        let Some(parent) = &kind.parent else {
            continue;
        };
        let of = &kinds[parent.kind];
        let (own, theirs) = (kind.columns[&parent.column], of.columns[&of.key]);
        if own != theirs {
            return Err(Error::descriptor(format!(
                "descriptor: kind {:?} parent key column {:?} is {}, and kind {:?} key column {:?} is {}; a parent key holds the parent's entity key",
                kind.name,
                parent.column,
                own.name(),
                of.name,
                of.key,
                theirs.name()
            )));
        }
    }
    Ok(())
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
        .chain(raw.root.as_deref().map(|c| ("root", c)))
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
    for (member, table) in [("table", &raw.table), ("historyTable", &raw.history_table)] {
        if table.is_empty() {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} has an empty {member}"
            )));
        }
    }
    if raw.columns.contains_key("") {
        return Err(Error::descriptor(format!(
            "descriptor: kind {name:?} has a column with an empty name in its columns"
        )));
    }
    let named = roles
        .iter()
        .map(|(role, column)| (*role, column.as_str()))
        .chain(raw.root.as_deref().map(|c| ("root", c)))
        .chain(raw.author.as_deref().map(|c| ("author", c)))
        .chain(raw.parent.as_ref().map(|p| ("parent key", p.key.as_str())))
        .chain(raw.order.as_deref().map(|c| ("order", c)))
        .chain(raw.units.keys().map(|c| ("unit", c.as_str())))
        .chain(raw.excluded.iter().map(|c| ("excluded", c.as_str())));
    for (role, column) in named {
        if !raw.columns.contains_key(column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} {role} column {column:?} is not in its columns"
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
    let history = raw.history;
    let kind = Kind {
        name,
        key: raw.key,
        id: raw.id,
        ref_column: raw.ref_column,
        root: raw.root,
        tombstone: raw.tombstone,
        version: raw.version,
        author: raw.author,
        parent,
        order: raw.order,
        singleton: raw.singleton,
        units: raw.units,
        excluded,
        columns: raw.columns,
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
    let class_rules = [
        ("tombstone", Some(&kind.tombstone), Element::Boolean),
        ("order", kind.order.as_ref(), Element::Integer),
    ];
    for (role, column, element) in class_rules {
        let Some(column) = column else {
            continue;
        };
        let class = kind.columns[column];
        if class != ValueClass::single(element) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {:?} {role} column {column:?} is {}, not {}",
                kind.name,
                class.name(),
                ValueClass::single(element).name()
            )));
        }
    }
    for (column, unit) in &kind.units {
        let class = kind.columns[column];
        if *unit != Unit::Atomic && class != ValueClass::single(Element::Json) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {:?} gives the {} conflict unit to {column:?}, which is {}; only a json column has keys to merge",
                kind.name,
                unit.name(),
                class.name()
            )));
        }
    }
    check_history(&kind, &history)?;
    Ok(kind)
}

/// The role columns a history image is found and read by: the kind's
/// roles less its author, which only names who wrote a row.
fn history_roles(kind: &Kind) -> impl Iterator<Item = (&'static str, &str)> {
    [
        ("key", kind.key.as_str()),
        ("id", kind.id.as_str()),
        ("ref", kind.ref_column.as_str()),
        ("tombstone", kind.tombstone.as_str()),
        ("version", kind.version.as_str()),
    ]
    .into_iter()
    .chain(kind.root.as_deref().map(|c| ("root", c)))
}

/// A retention is a whole number of days from 1 to [`MAX_RETENTION_DAYS`].
/// An excluded column is one of the kind's columns, once, and is not
/// content, since a commit is read back from history images; nor is it a
/// role column a history image is found and read by. The actor is a column
/// of the kind that history keeps, and is not one of those role columns
/// either.
fn check_history(kind: &Kind, history: &HistoryDescriptor) -> Result<(), Error> {
    let name = &kind.name;
    if let Some(days) = &history.retention_days {
        if days
            .as_u64()
            .is_none_or(|days| days == 0 || days > MAX_RETENTION_DAYS)
        {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history retentionDays is {days}, not an integer from 1 to {MAX_RETENTION_DAYS}"
            )));
        }
    }
    let mut excluded = BTreeSet::new();
    for column in &history.exclude {
        if !kind.columns.contains_key(column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history excludes {column:?}, which is not in its columns"
            )));
        }
        if let Some((role, _)) = history_roles(kind).find(|(_, c)| c == column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history excludes its {role} column {column:?}; history images are found and read by it"
            )));
        }
        if kind.is_content(column) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history excludes {column:?}, which is content; a commit's rows are read back from history images"
            )));
        }
        if !excluded.insert(column.as_str()) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history excludes {column:?} more than once"
            )));
        }
    }
    if let Some(actor) = &history.actor {
        if !kind.columns.contains_key(actor) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history actor column {actor:?} is not in its columns"
            )));
        }
        if let Some((role, _)) = history_roles(kind).find(|(_, c)| c == actor) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history actor column {actor:?} is its {role} column"
            )));
        }
        if excluded.contains(actor.as_str()) {
            return Err(Error::descriptor(format!(
                "descriptor: kind {name:?} history actor column {actor:?} is excluded from history, so a delete's image cannot name its actor in it"
            )));
        }
    }
    Ok(())
}
