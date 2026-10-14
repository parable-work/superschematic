//! Branches, the core's version graph on each instance (D32, over D17 and
//! D19), the port of `src/behaviors/core/branches.ts` (D58). A schema that
//! composes it makes each instance a graph's root: refs, a primary line and
//! drafts of it, over rows of the kinds the config names; commits, merges,
//! rebases and reverts; and a release pointer, whose history is the release
//! log. The root is never overlaid and is in no commit, so the instance's
//! own fields stay outside the graph.
//!
//! The config names the kinds: `kinds: { <kind>: { type, parent?, order?,
//! singleton?, units?, retentionDays? } }`, each kind's content the fields
//! of another type of the schema, which `parse_config` reads through the
//! target's types, so the compatibility rule keeps them as it keeps the
//! instance type's. `parse_config` derives the graph's descriptor (version
//! 3) from the config and the types: a kind's role and audit columns have
//! fixed names ([`ROLE_COLUMNS`]), a field may not take one, and each
//! field's value class follows graphdesc's rule for a compiled field: a
//! primitive's from its name, an enum's `enum`, a type's `json`, a catalog
//! scalar's the catalog's, a scalar the document defines the one its JSON
//! type gives, and each list level adds `[]`. A kind's images exclude
//! nothing and its actor column is `updated_by`. The version graph's core
//! checks the descriptor there, so a config it refuses fails the define.
//!
//! The operations are of instance scope. branch, save, commit, seal, merge,
//! rebase, revert, releaseCommit and discard write; refs, releases,
//! compose, materialize, released, diff and history read. Each takes what
//! the version graph engine's operation takes, refs and commits by id, and
//! every write through a ref its expected version; a ref or a commit that
//! is not the instance's is `invalid_argument` at the parameter that names
//! it. save holds each row's content to its kind's type with
//! `validate(type, value)`, and each value to its column's value class,
//! before the engine sees it; a merge or a rebase holds each resolution's
//! value to the same, and the row it leaves to the type. discard refuses
//! the primary line (`primary_line`). The engine's refusals are vetoes with
//! its stable codes, `released` before the instance's first release is
//! `not_found`, and refs and releases read the behavior's own tables, which
//! the engine has no operation for. releaseCommit is the version graph
//! engine's release under another name, since Lease has `release` and a
//! type's behaviors share one namespace of operation names.
//!
//! One read-only field, `release`, holds the number of the instance's
//! latest release, the release pointer's version, read from the behavior's
//! own table at each read; it is absent before the first release. So a
//! releaseCommit's event carries it, and Links pins a release, and reports
//! the latest, through it.
//!
//! The graph's tables are the behavior's own: the version graph's fixed
//! layout in the database's dialect (`fixed::layout`) under `sql.table`'s
//! names, which its migration creates, byte for byte the TypeScript
//! adapter's on SQLite, so a file the TypeScript engine wrote keeps working;
//! beside them `roots`, which maps each root to its instance, and `actors`,
//! which maps each actor to its subject. The version graph's Rust engine
//! reaches them through the call's SQL ([`crate::graph`]): one statement at
//! a time, on the behavior's own tables, with no trigger and no transaction
//! control, in the transaction of the operation that runs it, so an invoked
//! operation's savepoint rolls the graph back with the rest. On Postgres
//! the layout holds one writer per graph, which the engine's lock on the
//! namespace gives. A graph is named by its namespace and schema, so the
//! tables hold every schema's graphs; a root's id is the version-5 UUID of
//! its instance's id in [`ROOT_NAMESPACE`], since an instance id is any
//! string, and an actor the version-5 UUID of the principal's subject in
//! [`ACTOR_NAMESPACE`], which the behavior records so its reads return
//! subjects.
//!
//! initialize creates an instance's primary line, named by `primary`
//! (`main` by default). An instance created before its schema composed
//! Branches gets its primary line at its first write, in that write's
//! transaction, as its caller: a branch without `fromRef`, which then
//! branches from that line, an update, or a writing operation of another
//! behavior, through afterChange. Every Branches writing operation records
//! the root first, and one that is refused rolls the line back with the
//! rest. Deleting an instance deletes its graph.
//!
//! With `sweep` in the config, the sweep schedule runs on the schema: each
//! run discards the drafts idle past `abandonAfter` by invoking discard on
//! each instance, so each runs its guards and appends its event, then runs
//! the engine's sweep with abandoning off, which deletes the rows of refs
//! discarded past the grace, prunes history past each kind's retention and
//! writes missing snapshots. Without `sweep` the schedule is off there.
//!
//! configChange: a new version may add a kind, change a kind's fields as
//! the compatibility rule allows, and change a retention, `primary`,
//! `snapshotEvery` and `sweep`. Removing a kind, or changing a kind's type,
//! parent, order, singleton or a field's unit, is refused while the schema
//! has instances. Branches can be added to a schema that has instances and
//! cannot be removed from one. The schema epoch stays 0.

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, LazyLock, Mutex, PoisonError};
use std::time::Duration;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::paging::{base64url_decode, base64url_encode};
use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ConfigFieldKind, ConfigTypeField, Every, InstanceCx,
    InstanceView, Page, Schedule, ScheduleCx, Scope, SqlReader, SqlWriter,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{number_value, quote, stringify};
use superschematic_engine::storage::{identifier, Dialect, Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, Hooks, InstanceChange,
    OperationCx, Result, UuidSource, DEFAULT_PAGE_SIZE,
};
use superschematic_schema_ir::{builtin_scalar_value_classes, builtin_scalars};
use superschematic_versiongraph_engine as vg;
use superschematic_versiongraph_engine::fixed::{self, FixedLayout};
use superschematic_versiongraph_engine::{CommitOptions, EngineOn, KindEdits, Take};

use crate::graph::{self, sync, GraphCall, GraphFailure};

const NAME: &str = "Branches";

/// The namespace an actor is the version-5 UUID of a principal's subject in.
pub const ACTOR_NAMESPACE: &str = "e5df4b2e-719d-4507-8d3a-9474ef4afe81";

/// The namespace a root's id is the version-5 UUID of its instance's id in.
pub const ROOT_NAMESPACE: &str = "cc634206-0f43-4e6c-a60f-65184f26131e";

/// The name of each instance's primary line when the config gives none.
pub const DEFAULT_PRIMARY: &str = "main";

/// A kind's role and audit columns, by name, with their value classes: a
/// field of its type may not take one of these keys.
pub const ROLE_COLUMNS: [(&str, &str); 10] = [
    ("id", "uuid"),
    ("entity_key", "uuid"),
    ("ref_id", "uuid"),
    ("root_id", "uuid"),
    ("deleted_on_ref", "boolean"),
    ("_version", "integer"),
    ("created_at", "dateTime"),
    ("created_by", "uuid"),
    ("updated_at", "dateTime"),
    ("updated_by", "uuid"),
];

// The columns of a kind no merge compares or hash covers: the root's and
// the audit columns but the author, updated_by, which conflicts report.
const EXCLUDED_ROLES: [&str; 4] = ["created_at", "created_by", "root_id", "updated_at"];

// The columns of a row that name an actor, which a read returns as a
// subject.
const AUTHORS: [&str; 2] = ["created_by", "updated_by"];

const REF_COLUMNS: &str = "id, name, parent_ref_id, base_commit_id, head_commit_id, sealed_at, deleted_at, _version, created_at, created_by, updated_at, updated_by";

// The version graph engine's refusals a caller can meet, as vetoes with its
// stable codes, which the declaration lists.
static VETOES: LazyLock<Vec<String>> = LazyLock::new(|| {
    serde_json::from_str::<Value>(include_str!("../declarations/Branches.behavior.json"))
        .ok()
        .and_then(|declaration| declaration.get("vetoes").cloned())
        .and_then(|vetoes| match vetoes {
            Value::Array(vetoes) => Some(
                vetoes
                    .iter()
                    .filter_map(|veto| veto.get("code").and_then(Value::as_str))
                    .map(str::to_owned)
                    .collect(),
            ),
            _ => None,
        })
        .unwrap_or_default()
});

/// The version-5 UUID (RFC 9562) of `name` in `namespace`, a UUID in
/// either form, in its canonical form.
pub fn uuid_v5(namespace: &str, name: &str) -> String {
    let namespace = vg::canonical::uuid_hyphenated(namespace)
        .ok()
        .and_then(|hyphenated| uuid::Uuid::parse_str(&hyphenated).ok())
        .unwrap_or_else(uuid::Uuid::nil);
    let hyphenated = uuid::Uuid::new_v5(&namespace, name.as_bytes())
        .hyphenated()
        .to_string();
    vg::canonical::uuid(&hyphenated).unwrap_or(hyphenated)
}

/// A field's conflict unit, as `@conflictUnit` sets it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BranchesUnit {
    Atomic,
    Keyed,
    JsonSchema,
    Excluded,
}

impl BranchesUnit {
    /// The unit as the config writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            BranchesUnit::Atomic => "atomic",
            BranchesUnit::Keyed => "keyed",
            BranchesUnit::JsonSchema => "jsonSchema",
            BranchesUnit::Excluded => "excluded",
        }
    }

    fn parse(text: &str) -> Option<Self> {
        Some(match text {
            "atomic" => BranchesUnit::Atomic,
            "keyed" => BranchesUnit::Keyed,
            "jsonSchema" => BranchesUnit::JsonSchema,
            "excluded" => BranchesUnit::Excluded,
            _ => return None,
        })
    }
}

/// A kind's parent, as `@graphMember`'s: `key` holds the parent row's
/// entity key and `of` is the parent's kind.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BranchesParent {
    pub key: String,
    pub of: String,
}

/// One kind of the graph, as the config gives it, with what `parse_config`
/// read of its type.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BranchesKind {
    /// The type of the schema document whose fields are the kind's content.
    pub type_name: String,
    pub parent: Option<BranchesParent>,
    pub order: Option<String>,
    pub singleton: bool,
    /// The units the config sets, by field, in its order; a field it does
    /// not name is atomic.
    pub units: Vec<(String, BranchesUnit)>,
    pub retention_days: Option<i64>,
    /// The JSON keys of the type's fields, in the document's order, each
    /// with its value class: what a saved row holds beside `entity_key`.
    pub fields: Vec<(String, String)>,
}

impl BranchesKind {
    /// A field's value class; `None` for a key that is not a field.
    pub fn class(&self, field: &str) -> Option<&str> {
        self.fields
            .iter()
            .find(|(key, _)| key == field)
            .map(|(_, class)| class.as_str())
    }

    /// A field's unit: the config's, atomic when it names none.
    pub fn unit(&self, field: &str) -> BranchesUnit {
        self.units
            .iter()
            .find(|(key, _)| key == field)
            .map_or(BranchesUnit::Atomic, |(_, unit)| *unit)
    }

    fn has_field(&self, field: &str) -> bool {
        self.class(field).is_some()
    }
}

/// When the sweep runs on a schema, and what it keeps, in milliseconds but
/// `prune_batch`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BranchesSweep {
    pub interval_ms: i64,
    pub discard_grace: Option<i64>,
    pub prune_batch: Option<i64>,
    pub abandon_after: Option<i64>,
}

/// Branches' config, as `parse_config` returns it: the kinds and the
/// graph's descriptor, with the version graph's engine over it and the
/// fixed layout of each graph a call has run, kept for the calls after.
#[derive(Debug)]
pub struct BranchesConfig {
    /// The kinds by name, in name order.
    pub kinds: BTreeMap<String, BranchesKind>,
    pub primary: String,
    pub snapshot_every: i64,
    pub sweep: Option<BranchesSweep>,
    /// The graph's descriptor, version 3, as JSON text.
    pub descriptor: String,
    engine: vg::Engine,
    /// By dialect, the behavior's table prefix and the graph's name.
    layouts: Mutex<HashMap<(Dialect, String, String), Arc<FixedLayout>>>,
}

// A primitive's value class, by the names the schema runtime reads.
fn primitive_class(name: &str) -> Option<&'static str> {
    Some(match name {
        "string" | "String" | "ID" => "string",
        "Int" => "integer",
        "number" | "Float" => "number",
        "boolean" | "Boolean" => "boolean",
        _ => return None,
    })
}

// The value class of a scalar the document defines, by the JSON type of its
// values.
fn json_type_class(json_type: &str) -> Option<&'static str> {
    Some(match json_type {
        "string" => "string",
        "integer" => "integer",
        "number" => "number",
        "boolean" => "boolean",
        "object" | "array" | "any" => "json",
        _ => return None,
    })
}

/// A field's value class, or `None` when no rule gives it one: a catalog
/// scalar the layout has no class for (a vector, a point).
pub fn value_class(field: &ConfigTypeField) -> Option<String> {
    let element = match field.kind {
        ConfigFieldKind::Primitive => primitive_class(&field.type_name).map(str::to_owned),
        ConfigFieldKind::Enum => Some("enum".to_owned()),
        ConfigFieldKind::Type => Some("json".to_owned()),
        // A catalog scalar is the catalog's whatever the document declares
        // for it, as the engine validates it; any other is the document's.
        ConfigFieldKind::Scalar => {
            let key = field.type_name.replace('.', "_");
            if builtin_scalars().contains_key(&key) {
                builtin_scalar_value_classes().get(&key).cloned()
            } else {
                json_type_class(field.json_type.as_deref().unwrap_or_default()).map(str::to_owned)
            }
        }
    };
    element.map(|element| element + &"[]".repeat(usize::from(field.depth)))
}

// integer reads a number its schema holds to an integer, which JSON may
// write as 2 or 2.0.
fn integer(value: Option<&Value>) -> Option<i64> {
    match value {
        Some(Value::Number(number)) => {
            let read = number_value(number);
            (read.fract() == 0.0).then_some(read as i64)
        }
        _ => None,
    }
}

// js_string is a value as JavaScript's String() writes it.
fn js_string(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Null => "null".to_owned(),
        Value::Array(items) => items
            .iter()
            .map(|item| match item {
                Value::Null => String::new(),
                other => js_string(other),
            })
            .collect::<Vec<_>>()
            .join(","),
        Value::Object(_) => "[object Object]".to_owned(),
        other => stringify(other),
    }
}

// kind_of reads a kind's type and checks the kind against it.
fn kind_of(
    name: &str,
    raw: &Map<String, Value>,
    kinds: &Map<String, Value>,
    target: &ConfigTarget<'_>,
) -> Result<BranchesKind> {
    let at = format!("kind {name}");
    let type_name = raw.get("type").and_then(Value::as_str).unwrap_or_default();
    let Some(found) = target.types().get(type_name) else {
        let names = target.types().names().join(", ");
        return Err(Error::config(format!(
            "{at}: {type_name} is not a type of {} besides {} (its types: {})",
            target.schema(),
            target.type_name(),
            if names.is_empty() { "none" } else { &names }
        )));
    };
    let mut fields = Vec::with_capacity(found.fields.len());
    for field in &found.fields {
        if ROLE_COLUMNS.iter().any(|(role, _)| *role == field.key) {
            let roles: Vec<&str> = ROLE_COLUMNS.iter().map(|(role, _)| *role).collect();
            return Err(Error::config(format!(
                "{at}: {}.{} takes the name of a column every row of a kind has ({})",
                found.name,
                field.key,
                roles.join(", ")
            )));
        }
        let Some(class) = value_class(field) else {
            return Err(Error::config(format!(
                "{at}: {}.{} is of {}, which has no value class a graph's row can hold",
                found.name, field.key, field.type_name
            )));
        };
        fields.push((field.key.clone(), class));
    }
    let class_of = |key: &str| {
        fields
            .iter()
            .find(|(field, _)| field == key)
            .map(|(_, class)| class.as_str())
    };
    let role_field = |role: &str, key: &str, want: &str| -> Result<()> {
        let Some(class) = class_of(key) else {
            return Err(Error::config(format!(
                "{at}: its {role} {key} is not a field of {}",
                found.name
            )));
        };
        if class != want {
            return Err(Error::config(format!(
                "{at}: its {role} {key} holds {}, not a value of class {class}",
                if want == "uuid" {
                    "a parent row's entity key, a UUID"
                } else {
                    "an integer"
                }
            )));
        }
        Ok(())
    };
    let parent = match raw.get("parent").and_then(Value::as_object) {
        None => None,
        Some(parent) => {
            let key = parent
                .get("key")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let of = parent.get("of").and_then(Value::as_str).unwrap_or_default();
            if !kinds.contains_key(of) {
                let mut names: Vec<&str> = kinds.keys().map(String::as_str).collect();
                names.sort_unstable();
                return Err(Error::config(format!(
                    "{at}: its parent {of} is not a kind of the config (its kinds: {})",
                    names.join(", ")
                )));
            }
            role_field("parent key", key, "uuid")?;
            Some(BranchesParent {
                key: key.to_owned(),
                of: of.to_owned(),
            })
        }
    };
    let order = raw.get("order").and_then(Value::as_str).map(str::to_owned);
    if let Some(order) = &order {
        role_field("order", order, "integer")?;
    }
    let mut units = Vec::new();
    if let Some(given) = raw.get("units").and_then(Value::as_object) {
        for (key, unit) in given {
            let unit = unit
                .as_str()
                .and_then(BranchesUnit::parse)
                .unwrap_or(BranchesUnit::Atomic);
            let Some(class) = class_of(key) else {
                return Err(Error::config(format!(
                    "{at}: units names {key}, which is not a field of {}",
                    found.name
                )));
            };
            if matches!(unit, BranchesUnit::Keyed | BranchesUnit::JsonSchema) && class != "json" {
                return Err(Error::config(format!(
                    "{at}: a {} unit merges a JSON object, and {key} holds a value of class {class}",
                    unit.as_str()
                )));
            }
            let is_order = order.as_deref() == Some(key.as_str());
            let is_parent = parent.as_ref().is_some_and(|parent| parent.key == *key);
            if unit == BranchesUnit::Excluded && (is_order || is_parent) {
                return Err(Error::config(format!(
                    "{at}: {key} is its {}, which is content",
                    if is_order { "order" } else { "parent key" }
                )));
            }
            units.push((key.clone(), unit));
        }
    }
    Ok(BranchesKind {
        type_name: found.name.clone(),
        parent,
        order,
        singleton: raw.get("singleton") == Some(&Value::Bool(true)),
        units,
        retention_days: integer(raw.get("retentionDays")),
        fields,
    })
}

// descriptor_of is the graph's descriptor: each kind's fixed role and audit
// columns beside its type's fields, as graphdesc writes a compiled member's,
// with history that excludes nothing and names updated_by.
fn descriptor_of(schema: &str, kinds: &BTreeMap<String, BranchesKind>) -> String {
    let kinds: Vec<Value> = kinds
        .iter()
        .map(|(name, kind)| {
            let mut units = Map::new();
            let mut excluded: Vec<String> =
                EXCLUDED_ROLES.iter().map(|&role| role.to_owned()).collect();
            for (field, unit) in &kind.units {
                match unit {
                    BranchesUnit::Keyed | BranchesUnit::JsonSchema => {
                        units.insert(field.clone(), Value::from(unit.as_str()));
                    }
                    BranchesUnit::Excluded => excluded.push(field.clone()),
                    BranchesUnit::Atomic => {}
                }
            }
            excluded.sort_by(|a, b| superschematic_engine::json::compare_utf16(a, b));
            let mut out = Map::new();
            let mut put = |key: &str, value: Value| {
                out.insert(key.to_owned(), value);
            };
            put("kind", Value::from(name.as_str()));
            put("table", Value::from(name.as_str()));
            put("historyTable", Value::from(format!("{name}_history")));
            put("key", Value::from("entity_key"));
            put("id", Value::from("id"));
            put("ref", Value::from("ref_id"));
            put("root", Value::from("root_id"));
            put("tombstone", Value::from("deleted_on_ref"));
            put("version", Value::from("_version"));
            put("author", Value::from("updated_by"));
            if let Some(parent) = &kind.parent {
                put("parent", json!({"key": parent.key, "kind": parent.of}));
            }
            if let Some(order) = &kind.order {
                put("order", Value::from(order.as_str()));
            }
            if kind.singleton {
                put("singleton", Value::Bool(true));
            }
            if !units.is_empty() {
                put("units", Value::Object(units));
            }
            put(
                "excluded",
                Value::Array(excluded.into_iter().map(Value::from).collect()),
            );
            let mut history = Map::new();
            if let Some(days) = kind.retention_days {
                history.insert("retentionDays".into(), Value::from(days));
            }
            history.insert("exclude".into(), Value::Array(Vec::new()));
            history.insert("actor".into(), Value::from("updated_by"));
            put("history", Value::Object(history));
            let mut columns = Map::new();
            for (column, class) in ROLE_COLUMNS {
                columns.insert(column.to_owned(), Value::from(class));
            }
            for (field, class) in &kind.fields {
                columns.insert(field.clone(), Value::from(class.as_str()));
            }
            put("columns", Value::Object(columns));
            Value::Object(out)
        })
        .collect();
    stringify(&json!({
        "version": 3,
        "graph": schema,
        "root": {"table": schema, "key": "id"},
        "refTable": "ref",
        "commitTable": "commit",
        "patchTable": "patch",
        "releaseTable": "release",
        "snapshotTable": "snapshot_entry",
        "kinds": kinds,
    }))
}

fn parse_config(raw: &Value, target: &ConfigTarget<'_>) -> Result<BranchesConfig> {
    let empty = Map::new();
    let given = raw
        .get("kinds")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let mut kinds = BTreeMap::new();
    let mut names: Vec<&String> = given.keys().collect();
    names.sort_by(|a, b| superschematic_engine::json::compare_utf16(a, b));
    for name in names {
        let kind = given[name.as_str()].as_object().unwrap_or(&empty);
        kinds.insert(name.clone(), kind_of(name, kind, given, target)?);
    }
    let sweep = raw
        .get("sweep")
        .and_then(Value::as_object)
        .map(|sweep| BranchesSweep {
            interval_ms: integer(sweep.get("intervalMs")).unwrap_or_default(),
            discard_grace: integer(sweep.get("discardGrace")),
            prune_batch: integer(sweep.get("pruneBatch")),
            abandon_after: integer(sweep.get("abandonAfter")),
        });
    BranchesConfig::new(
        target.schema(),
        kinds,
        raw.get("primary")
            .and_then(Value::as_str)
            .unwrap_or(DEFAULT_PRIMARY)
            .to_owned(),
        integer(raw.get("snapshotEvery")).unwrap_or(64),
        sweep,
    )
}

impl BranchesConfig {
    /// The config of `kinds` on `schema`'s instance type: its descriptor,
    /// and the version graph's engine over it, which checks it. A
    /// descriptor the version graph's core refuses is a config the
    /// behavior refuses.
    pub fn new(
        schema: &str,
        kinds: BTreeMap<String, BranchesKind>,
        primary: String,
        snapshot_every: i64,
        sweep: Option<BranchesSweep>,
    ) -> Result<Self> {
        let descriptor = descriptor_of(schema, &kinds);
        let engine = vg::Engine::unbound(
            &descriptor,
            vg::Options {
                schema_epoch: 0,
                walk_ceiling: 0,
                snapshot_every: usize::try_from(snapshot_every).unwrap_or(64),
            },
        )
        .map_err(|error| {
            Error::config(format!(
                "the graph's descriptor: {}",
                match &error {
                    vg::Error::Core(core) => core.message.clone(),
                    other => other.to_string(),
                }
            ))
        })?;
        Ok(BranchesConfig {
            kinds,
            primary,
            snapshot_every,
            sweep,
            descriptor,
            engine,
            layouts: Mutex::new(HashMap::new()),
        })
    }
}

// config_change is the rule for a new version, kind by kind.
fn config_change(
    before: Option<&BranchesConfig>,
    after: Option<&BranchesConfig>,
    instances: &dyn Fn() -> Result<bool>,
) -> Result<Option<String>> {
    let Some(before) = before else {
        return Ok(None);
    };
    let Some(after) = after else {
        return Ok(Some(
            "the graphs its instances root would stay behind with nothing to delete them"
                .to_owned(),
        ));
    };
    // No instance roots a graph: a delete deletes its instance's graph.
    if !instances()? {
        return Ok(None);
    }
    let parent = |parent: &Option<BranchesParent>| {
        parent.as_ref().map_or(
            Value::Null,
            |parent| json!({"key": parent.key, "of": parent.of}),
        )
    };
    let text = |text: &Option<String>| text.as_deref().map_or(Value::Null, Value::from);
    for (name, kind) in &before.kinds {
        let Some(next) = after.kinds.get(name) else {
            return Ok(Some(format!(
                "it removes kind {name}, whose rows the graphs hold"
            )));
        };
        let changed = |what: &str, from: Value, to: Value| {
            Some(format!(
                "kind {name}: its {what} changes from {} to {}",
                stringify(&from),
                stringify(&to)
            ))
        };
        if next.type_name != kind.type_name {
            return Ok(changed(
                "type",
                Value::from(kind.type_name.as_str()),
                Value::from(next.type_name.as_str()),
            ));
        }
        if next.parent != kind.parent {
            return Ok(changed(
                "parent",
                parent(&kind.parent),
                parent(&next.parent),
            ));
        }
        if next.order != kind.order {
            return Ok(changed("order", text(&kind.order), text(&next.order)));
        }
        if next.singleton != kind.singleton {
            return Ok(changed(
                "singleton",
                Value::Bool(kind.singleton),
                Value::Bool(next.singleton),
            ));
        }
        // A field the old type lacks held no value, so the new version may
        // give it any unit.
        for (field, _) in &kind.fields {
            let from = kind.unit(field);
            let to = next.unit(field);
            if next.has_field(field) && from != to {
                return Ok(changed(
                    &format!("unit of {field}"),
                    Value::from(from.as_str()),
                    Value::from(to.as_str()),
                ));
            }
        }
    }
    Ok(None)
}

// The SQLite statements that create roots and actors, as the TypeScript
// engine's migration wrote them, so a file either engine created holds the
// same schema.
fn roots_and_actors(dialect: Dialect, roots: &str, actors: &str) -> [String; 2] {
    match dialect {
        Dialect::Sqlite => [
            format!(
                "CREATE TABLE {roots} (\n          graph   TEXT NOT NULL,\n          root_id TEXT NOT NULL,\n          id      TEXT NOT NULL,\n          PRIMARY KEY (graph, root_id)\n        ) STRICT"
            ),
            format!(
                "CREATE TABLE {actors} (\n          actor   TEXT NOT NULL PRIMARY KEY,\n          subject TEXT NOT NULL\n        ) STRICT"
            ),
        ],
        // Text compares by bytes, as the layout's does, so a join of roots
        // and refs reads one collation.
        Dialect::Postgres => [
            format!(
                "CREATE TABLE {roots} (graph TEXT COLLATE \"C\" NOT NULL, root_id TEXT COLLATE \"C\" NOT NULL, id TEXT COLLATE \"C\" NOT NULL, PRIMARY KEY (graph, root_id))"
            ),
            format!(
                "CREATE TABLE {actors} (actor TEXT COLLATE \"C\" NOT NULL PRIMARY KEY, subject TEXT COLLATE \"C\" NOT NULL)"
            ),
        ],
    }
}

/// The fixed layout's dialect for the engine's.
pub fn layout_dialect(dialect: Dialect) -> fixed::Dialect {
    match dialect {
        Dialect::Sqlite => fixed::Dialect::Sqlite,
        Dialect::Postgres => fixed::Dialect::Postgres,
    }
}

fn migrations() -> Vec<BehaviorMigration> {
    vec![
        BehaviorMigration::new(1, "the version graph layout, roots and actors").up(|sql| {
            // A shipped migration never changes, and this one takes the
            // layout's statements from the version graph's engine as it runs:
            // the TypeScript adapter's on SQLite, which a test pins, so a
            // layout the engine changes needs a migration of its own.
            let name = |local: &str| sql.table(local).unwrap_or_default();
            let statements =
                fixed::layout(layout_dialect(sql.dialect()), &name).map_err(|error| {
                    superschematic_engine::storage::Error::from(Error::behavior(
                        NAME,
                        format!("the version graph's layout: {error}"),
                    ))
                })?;
            for statement in statements {
                sql.run(statement, &[])?;
            }
            for statement in
                roots_and_actors(sql.dialect(), &sql.table("roots")?, &sql.table("actors")?)
            {
                sql.run(statement, &[])?;
            }
            Ok(())
        }),
    ]
}

/// The tables the behavior reads itself, by its own names for them.
struct Tables {
    ref_: String,
    ref_history: String,
    commit: String,
    patch: String,
    snapshot: String,
    release: String,
    release_history: String,
    member: String,
    member_history: String,
    roots: String,
    actors: String,
}

impl Tables {
    fn of(sql: SqlReader<'_>) -> Result<Self> {
        Ok(Tables {
            ref_: sql.table("ref")?,
            ref_history: sql.table("ref_history")?,
            commit: sql.table("commit")?,
            patch: sql.table("patch")?,
            snapshot: sql.table("snapshot_entry")?,
            release: sql.table("release")?,
            release_history: sql.table("release_history")?,
            member: sql.table("member")?,
            member_history: sql.table("member_history")?,
            roots: sql.table("roots")?,
            actors: sql.table("actors")?,
        })
    }
}

/// A call's graph: its name, the behavior's tables, the call's SQL, and
/// the version graph's engine and layout over it at the call's time.
struct Graph<'c> {
    /// The graph's name: its namespace and schema.
    name: String,
    t: Tables,
    reader: SqlReader<'c>,
    /// The call's SQL that writes; `None` in a read.
    writer: Option<SqlWriter<'c>>,
    engine: &'c vg::Engine,
    layout: Arc<FixedLayout>,
    now: i64,
    /// Where the graph's new ids come from: the engine's UUID source.
    uuids: UuidSource,
    schema: &'c str,
    /// The instance a refusal names; empty for a schedule's run.
    id: &'c str,
}

impl<'c> Graph<'c> {
    // call is what one graph operation runs with.
    fn call(&self) -> GraphCall<'c> {
        GraphCall {
            reader: self.reader,
            writer: self.writer,
            now_ms: self.now,
            uuids: self.uuids.clone(),
        }
    }

    // run runs one graph operation, turning the engine's refusals into the
    // operation's.
    fn run<T>(
        &self,
        operation: &str,
        op: impl FnOnce(EngineOn<'_>) -> std::result::Result<T, vg::Error>,
    ) -> Result<T> {
        graph::run(self.engine, &self.layout, &self.call(), op)
            .map_err(|failure| refusal(self.schema, self.id, operation, failure))
    }

    // run_raw runs one graph operation whose failure is no caller's
    // refusal: an instance's primary line, a schedule's sweep.
    fn run_raw<T>(
        &self,
        op: impl FnOnce(EngineOn<'_>) -> std::result::Result<T, vg::Error>,
    ) -> Result<T> {
        graph::run(self.engine, &self.layout, &self.call(), op).map_err(|failure| match failure {
            GraphFailure::Engine(error) => error,
            GraphFailure::Graph(error) => Error::behavior(NAME, error.to_string()),
        })
    }

    // writer is the call's SQL that writes.
    fn writer(&self) -> Result<SqlWriter<'c>> {
        self.writer
            .ok_or_else(|| Error::behavior(NAME, "a read of the graph wrote"))
    }
}

// layout_of is the fixed layout of a graph under the behavior's names in
// the call's dialect, built once and kept with the config: its clock reads
// the time of the call that runs each operation.
fn layout_of(
    config: &BranchesConfig,
    dialect: Dialect,
    sql: SqlReader<'_>,
    graph: &str,
) -> Result<Arc<FixedLayout>> {
    let probe = sql.table("ref")?;
    let prefix = probe
        .strip_suffix("ref")
        .ok_or_else(|| Error::behavior(NAME, format!("the table name {probe} does not end ref")))?
        .to_owned();
    let key = (dialect, prefix.clone(), graph.to_owned());
    let mut layouts = config
        .layouts
        .lock()
        .unwrap_or_else(PoisonError::into_inner);
    if let Some(layout) = layouts.get(&key) {
        return Ok(Arc::clone(layout));
    }
    let layout = FixedLayout::with_dialect(
        layout_dialect(dialect),
        &config.descriptor,
        fixed::Options {
            graph: graph.to_owned(),
            table_name: Some(Arc::new(move |local: &str| {
                identifier(dialect, &format!("{prefix}{local}")).into_owned()
            })),
            clock: Some(graph::call_clock()),
            ids: Some(graph::call_ids()),
        },
    )
    .map_err(|error| Error::behavior(NAME, format!("the version graph's layout: {error}")))?;
    let layout = Arc::new(layout);
    layouts.insert(key, Arc::clone(&layout));
    Ok(layout)
}

// graph_of is the graph of the call's schema in its namespace, over the
// call's SQL, at the call's time.
fn graph_of<'c>(
    scope: &Scope<'c, BranchesConfig>,
    reader: SqlReader<'c>,
    writer: Option<SqlWriter<'c>>,
    id: &'c str,
) -> Result<Graph<'c>> {
    let name = format!("{}/{}", scope.namespace(), scope.schema());
    let layout = layout_of(scope.config(), scope.dialect(), reader, &name)?;
    Ok(Graph {
        name,
        t: Tables::of(reader)?,
        reader,
        writer,
        engine: &scope.config().engine,
        layout,
        now: scope.now(),
        uuids: scope.uuids(),
        schema: scope.schema(),
        id,
    })
}

// writing is the graph of a writing call on an instance.
fn writing<'c>(cx: &InstanceCx<'c, BranchesConfig>) -> Result<Graph<'c>> {
    let sql = cx.sql();
    graph_of(cx, *sql, Some(sql), cx.id())
}

// reading is the graph of a read-only operation on an instance.
fn reading<'c>(cx: &OperationCx<'c, BranchesConfig>) -> Result<Graph<'c>> {
    let sql = cx.sql();
    graph_of(cx, *sql, None, cx.id())
}

/// The root id of an instance.
fn root_of(id: &str) -> String {
    uuid_v5(ROOT_NAMESPACE, id)
}

// actor_of is the actor of the call's principal, recorded with its subject
// so a read returns the subject.
fn actor_of(graph: &Graph<'_>, subject: &str) -> Result<String> {
    let actor = uuid_v5(ACTOR_NAMESPACE, subject);
    graph.writer()?.run(
        format!(
            "INSERT INTO {} (actor, subject) VALUES (?, ?) ON CONFLICT (actor) DO NOTHING",
            graph.t.actors
        ),
        &[SqlValue::from(actor.as_str()), SqlValue::from(subject)],
    )?;
    Ok(actor)
}

// is_root reports whether the instance has been recorded as a root.
fn is_root(graph: &Graph<'_>, id: &str) -> Result<bool> {
    Ok(graph
        .reader
        .get(
            format!(
                "SELECT 1 AS found FROM {} WHERE graph = ? AND root_id = ?",
                graph.t.roots
            ),
            &[
                SqlValue::from(graph.name.as_str()),
                SqlValue::from(root_of(id)),
            ],
        )?
        .is_some())
}

// ensure_root records the instance as a root and creates its primary line,
// once: at its create, or at the first write of one created before its
// schema composed Branches. It returns the call's graph.
fn ensure_root<'c>(cx: &InstanceCx<'c, BranchesConfig>) -> Result<Graph<'c>> {
    let graph = writing(cx)?;
    if !is_root(&graph, cx.id())? {
        let root = root_of(cx.id());
        graph.writer()?.run(
            format!(
                "INSERT INTO {} (graph, root_id, id) VALUES (?, ?, ?)",
                graph.t.roots
            ),
            &[
                SqlValue::from(graph.name.as_str()),
                SqlValue::from(root.as_str()),
                SqlValue::from(cx.id()),
            ],
        )?;
        let actor = actor_of(&graph, &cx.principal().subject)?;
        let primary = cx.config().primary.as_str();
        graph.run_raw(|e| sync(e.create_primary(&actor, &root, primary)))?;
    }
    Ok(graph)
}

fn params_refusal(operation: &str, path: impl Into<String>, message: impl Into<String>) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

// canonical_id reads a UUID parameter, in either form, as its canonical
// form.
fn canonical_id(operation: &str, path: &str, value: &Value) -> Result<String> {
    vg::canonical::uuid(&js_string(value)).map_err(|_| {
        params_refusal(
            operation,
            path,
            format!("{} is not a UUID", stringify(value)),
        )
    })
}

// own_ref reads a parameter that names a live ref of the instance's graph.
fn own_ref(graph: &Graph<'_>, operation: &str, name: &str, value: &Value) -> Result<String> {
    let id = canonical_id(operation, &format!("/{name}"), value)?;
    let row = graph.reader.get(
        format!(
            "SELECT root_id FROM {} WHERE graph = ? AND id = ? AND deleted_at IS NULL",
            graph.t.ref_
        ),
        &[
            SqlValue::from(graph.name.as_str()),
            SqlValue::from(id.as_str()),
        ],
    )?;
    let root = root_of(graph.id);
    match row {
        Some(row) if row.opt_text("root_id")? == Some(root.as_str()) => Ok(id),
        _ => Err(params_refusal(
            operation,
            format!("/{name}"),
            format!(
                "{} {} has no ref {}",
                graph.schema,
                graph.id,
                js_string(value)
            ),
        )),
    }
}

// primary_of is the id of the instance's primary line: its live ref with no
// parent, which discard never takes.
fn primary_of(graph: &Graph<'_>) -> Result<String> {
    let row = graph.reader.get(
        format!(
            "SELECT id FROM {} WHERE graph = ? AND root_id = ? AND parent_ref_id IS NULL AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1",
            graph.t.ref_
        ),
        &[SqlValue::from(graph.name.as_str()), SqlValue::from(root_of(graph.id))],
    )?;
    match row {
        Some(row) => Ok(row.text("id")?.to_owned()),
        None => Err(params_refusal(
            "branch",
            "/fromRef",
            format!(
                "{} {} has no primary line to branch from",
                graph.schema, graph.id
            ),
        )),
    }
}

// own_commit reads a parameter that names a commit of the instance's graph.
fn own_commit(graph: &Graph<'_>, operation: &str, name: &str, value: &Value) -> Result<String> {
    let id = canonical_id(operation, &format!("/{name}"), value)?;
    let row = graph.reader.get(
        format!(
            "SELECT root_id FROM {} WHERE graph = ? AND id = ?",
            graph.t.commit
        ),
        &[
            SqlValue::from(graph.name.as_str()),
            SqlValue::from(id.as_str()),
        ],
    )?;
    let root = root_of(graph.id);
    match row {
        Some(row) if row.opt_text("root_id")? == Some(root.as_str()) => Ok(id),
        _ => Err(params_refusal(
            operation,
            format!("/{name}"),
            format!(
                "{} {} has no commit {}",
                graph.schema,
                graph.id,
                js_string(value)
            ),
        )),
    }
}

fn veto(
    schema: &str,
    id: &str,
    operation: &str,
    code: &str,
    reason: String,
    details: Option<Map<String, Value>>,
) -> Error {
    Error::veto(
        NAME,
        operation,
        schema,
        id,
        Veto {
            reason,
            code: Some(code.to_owned()),
            details,
        },
    )
}

// refusal turns what the version graph engine refuses into the refusal a
// caller of the operation gets: a veto with the engine's code, not_found,
// or invalid_argument for a resolution or a value it cannot read. A
// statement the behavior engine refused is the engine's error as it is.
fn refusal(schema: &str, id: &str, operation: &str, failure: GraphFailure) -> Error {
    let error = match failure {
        GraphFailure::Engine(error) => return error,
        GraphFailure::Graph(error) => error,
    };
    match error {
        vg::Error::VersionConflict => veto(
            schema,
            id,
            operation,
            "version_conflict",
            "the ref or the release pointer is no longer at the version the call names".to_owned(),
            None,
        ),
        vg::Error::InvalidTree(findings) => {
            let reason = vg::Error::InvalidTree(findings.clone()).to_string();
            let mut details = Map::new();
            details.insert(
                "findings".into(),
                serde_json::to_value(&findings).unwrap_or(Value::Array(Vec::new())),
            );
            veto(schema, id, operation, "invalid_tree", reason, Some(details))
        }
        vg::Error::NotFound => EngineError::new(
            EngineErrorCode::NotFound,
            format!("{schema} {id}: not found"),
        )
        .into(),
        vg::Error::NameTaken(name) => veto(
            schema,
            id,
            operation,
            "name_taken",
            format!(
                "the root already has a live ref of that name: {}",
                quote(&name)
            ),
            None,
        ),
        vg::Error::Core(core)
            if matches!(core.code, "invalid_resolution" | "unmatched_resolution") =>
        {
            params_refusal(operation, "/resolutions", core.message)
        }
        vg::Error::Canonical(canonical) => params_refusal(operation, "", canonical.to_string()),
        other => match other.code() {
            Some(code) if VETOES.iter().any(|known| known == code) => {
                let code = code.to_owned();
                veto(schema, id, operation, &code, other.to_string(), None)
            }
            _ => Error::behavior(NAME, other.to_string()),
        },
    }
}

// subjects_of maps actors to the subjects they were recorded for; an actor
// with no record maps to itself.
fn subjects_of<'s>(
    graph: &Graph<'_>,
    actors: impl IntoIterator<Item = &'s str>,
) -> Result<HashMap<String, String>> {
    let mut wanted: Vec<&str> = Vec::new();
    for actor in actors {
        if !wanted.contains(&actor) {
            wanted.push(actor);
        }
    }
    let mut subjects = HashMap::new();
    for chunk in wanted.chunks(500) {
        let marks = vec!["?"; chunk.len()].join(", ");
        let params: Vec<SqlValue> = chunk.iter().map(|&actor| SqlValue::from(actor)).collect();
        for row in graph.reader.all(
            format!(
                "SELECT actor, subject FROM {} WHERE actor IN ({marks})",
                graph.t.actors
            ),
            &params,
        )? {
            subjects.insert(
                row.text("actor")?.to_owned(),
                row.text("subject")?.to_owned(),
            );
        }
    }
    Ok(subjects)
}

fn subject(subjects: &HashMap<String, String>, actor: &str) -> String {
    subjects
        .get(actor)
        .cloned()
        .unwrap_or_else(|| actor.to_owned())
}

// rows_of reads canonical rows, each actor as its subject, each row's
// columns in name order, as its canonical JSON has them.
fn rows_of(graph: &Graph<'_>, rows: &[Value]) -> Result<Vec<Value>> {
    let subjects = subjects_of(
        graph,
        rows.iter()
            .flat_map(|row| AUTHORS.iter().filter_map(move |column| row.get(*column)))
            .filter_map(Value::as_str),
    )?;
    Ok(rows
        .iter()
        .map(|row| {
            let Value::Object(members) = row else {
                return row.clone();
            };
            let mut names: Vec<&String> = members.keys().collect();
            names.sort();
            let mut out = Map::new();
            for name in names {
                let value = &members[name.as_str()];
                let value = match value {
                    Value::String(actor) if AUTHORS.contains(&name.as_str()) => {
                        Value::from(subject(&subjects, actor))
                    }
                    other => other.clone(),
                };
                out.insert(name.clone(), value);
            }
            Value::Object(out)
        })
        .collect())
}

// tree_of reads a tree, kind by kind, each actor as its subject.
fn tree_of(graph: &Graph<'_>, tree: &vg::Tree) -> Result<Value> {
    let mut out = Map::new();
    for (kind, rows) in tree {
        out.insert(kind.clone(), Value::Array(rows_of(graph, rows)?));
    }
    Ok(Value::Object(out))
}

fn tree_result(graph: &Graph<'_>, result: &vg::TreeResult) -> Result<Value> {
    let mut out = Map::new();
    out.insert("tree".into(), tree_of(graph, &result.tree)?);
    out.insert(
        "contentHash".into(),
        Value::from(result.content_hash.as_str()),
    );
    out.insert(
        "findings".into(),
        serde_json::to_value(&result.findings).unwrap_or(Value::Array(Vec::new())),
    );
    Ok(Value::Object(out))
}

// date_time is a stored time as the operations return it.
fn date_time(micros: i64) -> Result<String> {
    fixed::micros_to_date_time(micros).map_err(|error| Error::behavior(NAME, error.to_string()))
}

fn refs_of(graph: &Graph<'_>, rows: &[Row]) -> Result<Vec<Value>> {
    let mut actors = Vec::new();
    for row in rows {
        actors.push(row.text("created_by")?);
        actors.push(row.text("updated_by")?);
    }
    let subjects = subjects_of(graph, actors)?;
    rows.iter()
        .map(|row| {
            let optional = |column: &str| -> Result<Value> {
                Ok(row.opt_text(column)?.map_or(Value::Null, Value::from))
            };
            let mut out = Map::new();
            out.insert("id".into(), Value::from(row.text("id")?));
            out.insert("name".into(), Value::from(row.text("name")?));
            out.insert("parent".into(), optional("parent_ref_id")?);
            out.insert("base".into(), optional("base_commit_id")?);
            out.insert("head".into(), optional("head_commit_id")?);
            out.insert(
                "sealed".into(),
                Value::Bool(!row.value("sealed_at")?.is_null()),
            );
            out.insert(
                "discarded".into(),
                Value::Bool(!row.value("deleted_at")?.is_null()),
            );
            out.insert("version".into(), Value::from(row.i64("_version")?));
            out.insert(
                "createdAt".into(),
                Value::from(date_time(row.i64("created_at")?)?),
            );
            out.insert(
                "createdBy".into(),
                Value::from(subject(&subjects, row.text("created_by")?)),
            );
            out.insert(
                "updatedAt".into(),
                Value::from(date_time(row.i64("updated_at")?)?),
            );
            out.insert(
                "updatedBy".into(),
                Value::from(subject(&subjects, row.text("updated_by")?)),
            );
            Ok(Value::Object(out))
        })
        .collect()
}

// ref_of reads a ref of the graph as the operations return it.
fn ref_of(graph: &Graph<'_>, id: &str) -> Result<Value> {
    let row = graph
        .reader
        .get(
            format!(
                "SELECT {REF_COLUMNS} FROM {} WHERE graph = ? AND id = ?",
                graph.t.ref_
            ),
            &[SqlValue::from(graph.name.as_str()), SqlValue::from(id)],
        )?
        .ok_or_else(|| Error::behavior(NAME, format!("ref {id} is gone after its write")))?;
    Ok(refs_of(graph, &[row])?.remove(0))
}

fn commits_of(graph: &Graph<'_>, commits: &[Option<vg::Commit>]) -> Result<Vec<Value>> {
    let subjects = subjects_of(
        graph,
        commits
            .iter()
            .flatten()
            .map(|commit| commit.created_by.as_str()),
    )?;
    Ok(commits
        .iter()
        .map(|commit| match commit {
            None => Value::Null,
            Some(commit) => {
                let mut out = Map::new();
                out.insert("id".into(), Value::from(commit.id.as_str()));
                out.insert("ref".into(), Value::from(commit.ref_id.as_str()));
                out.insert(
                    "parent".into(),
                    commit.parent.as_deref().map_or(Value::Null, Value::from),
                );
                out.insert("message".into(), Value::from(commit.message.as_str()));
                out.insert(
                    "sequence".into(),
                    commit.sequence.map_or(Value::Null, Value::from),
                );
                out.insert(
                    "contentHash".into(),
                    Value::from(commit.content_hash.as_str()),
                );
                out.insert("createdAt".into(), Value::from(commit.created_at.as_str()));
                out.insert(
                    "createdBy".into(),
                    Value::from(subject(&subjects, &commit.created_by)),
                );
                out.insert("snapshot".into(), Value::Bool(commit.snapshot));
                Value::Object(out)
            }
        })
        .collect())
}

fn commit_of(graph: &Graph<'_>, commit: Option<vg::Commit>) -> Result<Value> {
    Ok(commits_of(graph, &[commit])?.remove(0))
}

fn conflicts_of(graph: &Graph<'_>, conflicts: &[vg::Conflict]) -> Result<Vec<Value>> {
    let subjects = subjects_of(
        graph,
        conflicts
            .iter()
            .flat_map(|conflict| [&conflict.ours_author, &conflict.theirs_author])
            .filter_map(|author| author.as_ref().and_then(Value::as_str)),
    )?;
    Ok(conflicts
        .iter()
        .map(|conflict| {
            let mut out = Map::new();
            out.insert("kind".into(), Value::from(conflict.kind.as_str()));
            out.insert(
                "entityKey".into(),
                Value::from(conflict.entity_key.as_str()),
            );
            out.insert("path".into(), Value::from(conflict.path.as_str()));
            for (side, value) in [
                ("base", &conflict.base),
                ("ours", &conflict.ours),
                ("theirs", &conflict.theirs),
            ] {
                if let Some(value) = value {
                    out.insert(side.into(), value.clone());
                }
            }
            for (side, author) in [
                ("oursAuthor", &conflict.ours_author),
                ("theirsAuthor", &conflict.theirs_author),
            ] {
                if let Some(Value::String(actor)) = author {
                    out.insert(side.into(), Value::from(subject(&subjects, actor)));
                }
            }
            Value::Object(out)
        })
        .collect())
}

// refs_cursor reads refs' cursor: the key of the last ref of the page
// before, (whether it is a draft, its creation time, its id); the key
// before every ref's for the first page.
fn refs_cursor(cursor: Option<&str>) -> Result<(i64, i64, String)> {
    let Some(cursor) = cursor else {
        return Ok((-1, 0, String::new()));
    };
    let refused = || params_refusal("refs", "/cursor", "is not a cursor this operation returned");
    let decoded = String::from_utf8_lossy(&base64url_decode(cursor)).into_owned();
    let Ok(Value::Array(key)) = serde_json::from_str::<Value>(&decoded) else {
        return Err(refused());
    };
    let number = |value: &Value| match value {
        Value::Number(number) => Some(number_value(number)),
        _ => None,
    };
    match key.as_slice() {
        [draft, created, Value::String(id)] => {
            let draft = number(draft).filter(|draft| *draft == 0.0 || *draft == 1.0);
            let created = number(created).filter(|created| {
                created.fract() == 0.0 && created.abs() <= 9_007_199_254_740_991.0
            });
            match (draft, created) {
                (Some(draft), Some(created)) => Ok((draft as i64, created as i64, id.clone())),
                _ => Err(refused()),
            }
        }
        _ => Err(refused()),
    }
}

// pointer turns a validation issue's path (`steps[0].name`) into a JSON
// pointer under base.
fn pointer(base: &str, path: &str) -> String {
    let mut out = base.to_owned();
    let chars: Vec<char> = path.chars().collect();
    let mut at = 0;
    while at < chars.len() {
        let c = chars[at];
        if !matches!(c, '.' | '[' | ']') {
            let start = at;
            while at < chars.len() && !matches!(chars[at], '.' | '[' | ']') {
                at += 1;
            }
            let token: String = chars[start..at].iter().collect();
            out.push('/');
            out.push_str(&escape(&token));
            continue;
        }
        if c == '[' {
            let mut end = at + 1;
            while end < chars.len() && chars[end].is_ascii_digit() {
                end += 1;
            }
            if end > at + 1 && end < chars.len() && chars[end] == ']' {
                let token: String = chars[at + 1..end].iter().collect();
                out.push('/');
                out.push_str(&token);
                at = end + 1;
                continue;
            }
        }
        at += 1;
    }
    out
}

// escape escapes one reference token of a JSON pointer.
fn escape(key: &str) -> String {
    key.replace('~', "~0").replace('/', "~1")
}

// class_issues holds each field's value to its column's value class, as the
// adapter canonicalizes it, so a value no class rule reads is refused at its
// field rather than when the engine writes it.
fn class_issues(kind: &BranchesKind, content: &Map<String, Value>, at: &str) -> Vec<SchemaIssue> {
    let mut issues = Vec::new();
    for (field, class) in &kind.fields {
        let Some(value) = content.get(field).filter(|value| !value.is_null()) else {
            continue;
        };
        if let Err(error) = vg::canonical::postgres_value(class, value) {
            issues.push(SchemaIssue::new(
                format!("{at}/{}", escape(field)),
                format!("is no {class} value: {}", error.message),
            ));
        }
    }
    issues
}

// edits_of checks a save's edits and returns them as the engine takes
// them: each upsert row's content held to its kind's type, entity keys
// canonical, and each field the row leaves out null, so a row is its
// entity's whole content.
fn edits_of(cx: &OperationCx<'_, BranchesConfig>, raw: &Value) -> Result<vg::Edits> {
    let mut issues = Vec::new();
    let mut edits = vg::Edits::new();
    let kinds = &cx.config().kinds;
    let empty = Map::new();
    for (name, given) in raw.as_object().unwrap_or(&empty) {
        let at = pointer("/edits", name);
        let Some(kind) = kinds.get(name) else {
            let names: Vec<&str> = kinds.keys().map(String::as_str).collect();
            issues.push(SchemaIssue::new(
                at,
                format!(
                    "{name} is not a kind of the graph (its kinds: {})",
                    names.join(", ")
                ),
            ));
            continue;
        };
        let list = |member: &str| -> Vec<Value> {
            given
                .get(member)
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default()
        };
        let mut upsert = Vec::new();
        for (index, value) in list("upsert").iter().enumerate() {
            let row_at = format!("{at}/upsert/{index}");
            let mut content = value.as_object().cloned().unwrap_or_default();
            let key = content.shift_remove("entity_key");
            let refused = cx.validate(&kind.type_name, &Value::Object(content.clone()))?;
            for issue in &refused {
                issues.push(SchemaIssue::new(
                    pointer(&row_at, &issue.path),
                    issue.message.clone(),
                ));
            }
            // A value the type accepts may still be one its column's class
            // refuses (1.5 of a scalar whose JSON type is integer).
            if refused.is_empty() {
                issues.extend(class_issues(kind, &content, &row_at));
            }
            let mut row = Map::new();
            if let Some(key) = key.filter(|key| !key.is_null()) {
                match vg::canonical::uuid(&js_string(&key)) {
                    Ok(canonical) => {
                        row.insert("entity_key".into(), Value::from(canonical));
                    }
                    Err(_) => issues.push(SchemaIssue::new(
                        format!("{row_at}/entity_key"),
                        format!("{} is not a UUID", stringify(&key)),
                    )),
                }
            }
            for (field, _) in &kind.fields {
                row.insert(
                    field.clone(),
                    content.get(field).cloned().unwrap_or(Value::Null),
                );
            }
            upsert.push(Value::Object(row));
        }
        let mut keys = |member: &str| -> Vec<String> {
            let mut out = Vec::new();
            for (index, key) in list(member).iter().enumerate() {
                match vg::canonical::uuid(&js_string(key)) {
                    Ok(canonical) => out.push(canonical),
                    Err(_) => issues.push(SchemaIssue::new(
                        format!("{at}/{member}/{index}"),
                        format!("{} is not a UUID", stringify(key)),
                    )),
                }
            }
            out
        };
        let delete = keys("delete");
        let unset = keys("unset");
        edits.insert(
            name.clone(),
            KindEdits {
                upsert,
                delete,
                unset,
            },
        );
    }
    if !issues.is_empty() {
        return Err(EngineError::operation_params(NAME, "save", issues).into());
    }
    Ok(edits)
}

/// A resolution that gives a value, whose merged row is held to its kind's
/// type: its index in the resolutions, its kind and its entity.
struct ValueResolution {
    index: usize,
    kind: String,
    entity_key: String,
}

// resolutions_of reads a merge's or a rebase's resolutions as the engine
// takes them. A value is held to the value class of the field it sets; the
// row it leaves is held to its kind's type once the engine has merged
// (check_resolved), so it returns the resolutions that give one.
fn resolutions_of(
    cx: &OperationCx<'_, BranchesConfig>,
    operation: &str,
    raw: Option<&Value>,
) -> Result<(Vec<vg::Resolution>, Vec<ValueResolution>)> {
    let mut issues = Vec::new();
    let mut resolutions = Vec::new();
    let mut values = Vec::new();
    let given = raw.and_then(Value::as_array).cloned().unwrap_or_default();
    let empty = Map::new();
    for (index, resolution) in given.iter().enumerate() {
        let resolution = resolution.as_object().unwrap_or(&empty);
        let at = format!("/resolutions/{index}");
        let kind = resolution
            .get("kind")
            .map_or_else(|| "undefined".to_owned(), js_string);
        let Some(spec) = cx.config().kinds.get(&kind) else {
            issues.push(SchemaIssue::new(
                format!("{at}/kind"),
                format!("{kind} is not a kind of the graph"),
            ));
            continue;
        };
        let given_key = resolution.get("entityKey").cloned().unwrap_or(Value::Null);
        let Ok(entity_key) = vg::canonical::uuid(&js_string(&given_key)) else {
            issues.push(SchemaIssue::new(
                format!("{at}/entityKey"),
                format!("{} is not a UUID", stringify(&given_key)),
            ));
            continue;
        };
        let path = resolution
            .get("path")
            .map_or_else(|| "undefined".to_owned(), js_string);
        let take = resolution.get("take");
        if take.is_some() == resolution.contains_key("value") {
            issues.push(SchemaIssue::new(
                at,
                "a resolution gives take or value, one of them",
            ));
            continue;
        }
        if let Some(take) = take {
            let take = match take.as_str() {
                Some("base") => Take::Base,
                Some("ours") => Take::Ours,
                _ => Take::Theirs,
            };
            resolutions.push(vg::Resolution {
                kind,
                entity_key,
                path,
                take: Some(take),
                value: None,
            });
            continue;
        }
        let value = resolution.get("value").cloned().unwrap_or(Value::Null);
        // A whole entity (path "") is settled with take, which the core
        // holds to; a value sets one unit, a field or a key below one.
        let tokens: Vec<String> = if path.is_empty() {
            Vec::new()
        } else {
            path.chars()
                .skip(1)
                .collect::<String>()
                .split('/')
                .map(|part| part.replace("~1", "/").replace("~0", "~"))
                .collect()
        };
        if let [field] = tokens.as_slice() {
            if spec.has_field(field) {
                let mut content = Map::new();
                content.insert(field.clone(), value.clone());
                for issue in class_issues(spec, &content, &format!("{at}/value")) {
                    issues.push(SchemaIssue::new(format!("{at}/value"), issue.message));
                }
            }
        }
        resolutions.push(vg::Resolution {
            kind: kind.clone(),
            entity_key: entity_key.clone(),
            path,
            take: None,
            value: Some(value),
        });
        values.push(ValueResolution {
            index,
            kind,
            entity_key,
        });
    }
    if !issues.is_empty() {
        return Err(EngineError::operation_params(NAME, operation, issues).into());
    }
    Ok((resolutions, values))
}

// check_resolved holds each entity a value resolution settled, as the ref
// now composes it, to its kind's type, as save holds a row: a resolution
// whose value leaves a row its type refuses is refused at its value, and the
// operation, with what it wrote, rolls back.
fn check_resolved(
    cx: &OperationCx<'_, BranchesConfig>,
    graph: &Graph<'_>,
    operation: &str,
    ref_id: &str,
    values: &[ValueResolution],
) -> Result<()> {
    if values.is_empty() {
        return Ok(());
    }
    let composed = graph.run(operation, |e| sync(e.compose(ref_id)))?;
    let mut issues = Vec::new();
    for value in values {
        let Some(kind) = cx.config().kinds.get(&value.kind) else {
            continue;
        };
        let row = composed.tree.get(&value.kind).and_then(|rows| {
            rows.iter().find(|row| {
                row.get("entity_key").and_then(Value::as_str) == Some(value.entity_key.as_str())
            })
        });
        // No row: the resolution deleted the entity.
        let Some(row) = row else {
            continue;
        };
        let mut content = Map::new();
        for (field, _) in &kind.fields {
            if let Some(cell) = row.get(field) {
                content.insert(field.clone(), cell.clone());
            }
        }
        for issue in cx.validate(&kind.type_name, &Value::Object(content))? {
            issues.push(SchemaIssue::new(
                format!("/resolutions/{}/value", value.index),
                format!(
                    "{} {}{}: {}",
                    value.kind,
                    value.entity_key,
                    if issue.path.is_empty() {
                        String::new()
                    } else {
                        format!(" {}", issue.path)
                    },
                    issue.message
                ),
            ));
        }
    }
    if !issues.is_empty() {
        return Err(EngineError::operation_params(NAME, operation, issues).into());
    }
    Ok(())
}

// commit_options reads a commit's message and tag from the parameters.
fn commit_options(params: &Value) -> CommitOptions {
    CommitOptions {
        message: params
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        tag: params.get("tag") == Some(&Value::Bool(true)),
    }
}

// delete_graph deletes an instance's graph: its history, rows, release
// pointer, commits and refs, and its record as a root. A ref's head and base
// point at its commits, and a commit at its ref, so the refs let go of their
// commits first.
fn delete_graph(cx: &InstanceCx<'_, BranchesConfig>) -> Result<()> {
    let sql = cx.sql();
    let t = Tables::of(*sql)?;
    let graph = format!("{}/{}", cx.namespace(), cx.schema());
    let root = root_of(cx.id());
    let pair = [
        SqlValue::from(graph.as_str()),
        SqlValue::from(root.as_str()),
    ];
    let triple = [
        SqlValue::from(graph.as_str()),
        SqlValue::from(graph.as_str()),
        SqlValue::from(root.as_str()),
    ];
    sql.run(
        Sql::per(
            format!(
                "DELETE FROM {} WHERE graph = ? AND json_extract(data, '$.root_id') = ?",
                t.member_history
            ),
            format!(
                "DELETE FROM {} WHERE graph = ? AND CAST(data AS JSONB) ->> 'root_id' = ?",
                t.member_history
            ),
        ),
        &pair,
    )?;
    sql.run(
        format!(
            "DELETE FROM {} WHERE graph = ? AND id IN (SELECT id FROM {} WHERE graph = ? AND root_id = ?)",
            t.ref_history, t.ref_
        ),
        &triple,
    )?;
    sql.run(
        format!(
            "DELETE FROM {} WHERE graph = ? AND id IN (SELECT id FROM {} WHERE graph = ? AND root_id = ?)",
            t.release_history, t.release
        ),
        &triple,
    )?;
    sql.run(
        format!("DELETE FROM {} WHERE graph = ? AND root_id = ?", t.release),
        &pair,
    )?;
    sql.run(
        format!("DELETE FROM {} WHERE graph = ? AND root_id = ?", t.member),
        &pair,
    )?;
    for table in [&t.snapshot, &t.patch] {
        sql.run(
            format!(
                "DELETE FROM {table} WHERE graph = ? AND commit_id IN (SELECT id FROM {} WHERE graph = ? AND root_id = ?)",
                t.commit
            ),
            &triple,
        )?;
    }
    sql.run(
        format!(
            "UPDATE {} SET head_commit_id = NULL, base_commit_id = NULL WHERE graph = ? AND root_id = ?",
            t.ref_
        ),
        &pair,
    )?;
    sql.run(
        format!("DELETE FROM {} WHERE graph = ? AND root_id = ?", t.commit),
        &pair,
    )?;
    sql.run(
        format!("DELETE FROM {} WHERE graph = ? AND root_id = ?", t.ref_),
        &pair,
    )?;
    sql.run(
        format!("DELETE FROM {} WHERE graph = ? AND root_id = ?", t.roots),
        &pair,
    )?;
    Ok(())
}

// version reads a write's expected version.
fn version(params: &Value, name: &str) -> i64 {
    integer(params.get(name)).unwrap_or_default()
}

fn param<'p>(params: &'p Value, name: &str) -> &'p Value {
    params.get(name).unwrap_or(&Value::Null)
}

fn branch(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    // Without fromRef, the draft branches from the primary line, which
    // ensure_root has just made for an instance that predates Branches.
    let from = match params.get("fromRef") {
        None => primary_of(&graph)?,
        Some(value) => own_ref(&graph, "branch", "fromRef", value)?,
    };
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let name = param(params, "name").as_str().unwrap_or_default();
    let made = graph.run("branch", |e| sync(e.branch(&actor, &from, name)))?;
    ref_of(&graph, &made.id)
}

fn save(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let ref_id = own_ref(&graph, "save", "ref", param(params, "ref"))?;
    let edits = edits_of(cx, param(params, "edits"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    let result = graph.run("save", |e| sync(e.save(&actor, &ref_id, expected, &edits)))?;
    let mut saved = Map::new();
    for (kind, rows) in &result.saved {
        saved.insert(kind.clone(), Value::Array(rows_of(&graph, rows)?));
    }
    Ok(json!({"ref": ref_of(&graph, &result.ref_.id)?, "saved": saved}))
}

fn commit(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let ref_id = own_ref(&graph, "commit", "ref", param(params, "ref"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    let options = commit_options(params);
    let result = graph.run("commit", |e| {
        sync(e.commit(&actor, &ref_id, expected, &options))
    })?;
    Ok(
        json!({"ref": ref_of(&graph, &result.ref_.id)?, "commit": commit_of(&graph, result.commit)?}),
    )
}

fn seal(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let ref_id = own_ref(&graph, "seal", "ref", param(params, "ref"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    let result = graph.run("seal", |e| sync(e.seal(&actor, &ref_id, expected)))?;
    Ok(
        json!({"ref": ref_of(&graph, &result.ref_.id)?, "commit": commit_of(&graph, result.commit)?}),
    )
}

// merged is a merge's or a rebase's result, held to the kinds' types when
// it wrote.
fn merged(
    cx: &OperationCx<'_, BranchesConfig>,
    graph: &Graph<'_>,
    operation: &str,
    result: vg::MergeResult,
    values: &[ValueResolution],
) -> Result<Value> {
    if result.conflicts.is_empty() {
        check_resolved(cx, graph, operation, &result.ref_.id, values)?;
    }
    Ok(json!({
        "ref": ref_of(graph, &result.ref_.id)?,
        "commit": commit_of(graph, result.commit)?,
        "conflicts": conflicts_of(graph, &result.conflicts)?,
    }))
}

fn merge(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let source = own_ref(&graph, "merge", "source", param(params, "source"))?;
    let target = own_ref(&graph, "merge", "target", param(params, "target"))?;
    let (resolutions, values) = resolutions_of(cx, "merge", params.get("resolutions"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "targetVersion");
    let options = commit_options(params);
    let result = graph.run("merge", |e| {
        sync(e.merge(&actor, &source, &target, expected, &resolutions, &options))
    })?;
    merged(cx, &graph, "merge", result, &values)
}

fn rebase(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let draft = own_ref(&graph, "rebase", "draft", param(params, "draft"))?;
    let (resolutions, values) = resolutions_of(cx, "rebase", params.get("resolutions"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    let result = graph.run("rebase", |e| {
        sync(e.rebase(&actor, &draft, expected, &resolutions))
    })?;
    merged(cx, &graph, "rebase", result, &values)
}

fn revert(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let ref_id = own_ref(&graph, "revert", "ref", param(params, "ref"))?;
    let to = own_commit(&graph, "revert", "toCommit", param(params, "toCommit"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    let result = graph.run("revert", |e| sync(e.revert(&actor, &ref_id, expected, &to)))?;
    Ok(
        json!({"ref": ref_of(&graph, &result.ref_.id)?, "commit": commit_of(&graph, result.commit)?}),
    )
}

fn release_commit(
    _: &Branches,
    cx: &OperationCx<'_, BranchesConfig>,
    params: &Value,
) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let commit = own_commit(&graph, "releaseCommit", "commit", param(params, "commit"))?;
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let root = root_of(cx.id());
    let expected = version(params, "version");
    // The version graph engine's release: releaseCommit names it so a type
    // can compose Branches with Lease, whose release ends a lease.
    let release = graph.run("releaseCommit", |e| {
        sync(e.release(&actor, &root, &commit, expected))
    })?;
    Ok(json!({"commit": release.commit, "version": release.version}))
}

fn discard(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = ensure_root(cx)?;
    let ref_id = own_ref(&graph, "discard", "ref", param(params, "ref"))?;
    // Every draft branches from the primary line and merges into it, and the
    // instance gets no other.
    if ref_of(&graph, &ref_id)?.get("parent") == Some(&Value::Null) {
        return Err(veto(
            cx.schema(),
            cx.id(),
            "discard",
            "primary_line",
            "the primary line is not discarded: every draft branches from it and merges into it"
                .to_owned(),
            None,
        ));
    }
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let expected = version(params, "version");
    graph.run("discard", |e| sync(e.discard(&actor, &ref_id, expected)))?;
    ref_of(&graph, &ref_id)
}

fn refs(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let limit = integer(params.get("limit")).unwrap_or(DEFAULT_PAGE_SIZE);
    let (draft, created, id) = refs_cursor(params.get("cursor").and_then(Value::as_str))?;
    let graph = reading(cx)?;
    // The primary line first, then the drafts in the order they were
    // created: a key every ref keeps for good, which no renumbering of rows
    // moves.
    let draft_key = "CASE WHEN parent_ref_id IS NULL THEN 0 ELSE 1 END";
    let rows = graph.reader.all(
        format!(
            "SELECT {REF_COLUMNS}, {draft_key} AS draft FROM {}
             WHERE graph = ? AND root_id = ? AND deleted_at IS NULL
               AND ({draft_key}, created_at, id) > (?, ?, ?)
             ORDER BY {draft_key}, created_at, id LIMIT ?",
            graph.t.ref_
        ),
        &[
            SqlValue::from(graph.name.as_str()),
            SqlValue::from(root_of(cx.id())),
            SqlValue::Integer(draft),
            SqlValue::Integer(created),
            SqlValue::from(id),
            SqlValue::Integer(limit + 1),
        ],
    )?;
    let shown = usize::try_from(limit).unwrap_or(0).min(rows.len());
    let items = refs_of(&graph, &rows[..shown])?;
    let next = if rows.len() > shown && shown > 0 {
        let last = &rows[shown - 1];
        let key = json!([
            last.i64("draft")?,
            last.i64("created_at")?,
            last.text("id")?
        ]);
        Value::from(base64url_encode(stringify(&key).as_bytes()))
    } else {
        Value::Null
    };
    Ok(json!({"items": items, "next": next}))
}

fn releases(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let request = page_request(NAME, "releases", params)?;
    let graph = reading(cx)?;
    let rows = graph.reader.all(
        format!(
            "SELECT h._version AS version, h.data FROM {} AS h JOIN {} AS r ON r.id = h.id
             WHERE r.graph = ? AND r.root_id = ? AND h._version > ?
             ORDER BY h._version LIMIT ?",
            graph.t.release_history, graph.t.release
        ),
        &[
            SqlValue::from(graph.name.as_str()),
            SqlValue::from(root_of(cx.id())),
            SqlValue::Integer(request.after),
            SqlValue::Integer(request.limit + 1),
        ],
    )?;
    let mut images = Vec::with_capacity(rows.len());
    for row in &rows {
        let image: Value = serde_json::from_str(row.text("data")?).map_err(|error| {
            Error::behavior(
                NAME,
                format!("a release's history image is not JSON: {error}"),
            )
        })?;
        images.push((row.i64("version")?, image));
    }
    let subjects = subjects_of(
        &graph,
        images
            .iter()
            .filter_map(|(_, image)| image.get("updated_by").and_then(Value::as_str)),
    )?;
    let records: Vec<(i64, Value)> = images
        .iter()
        .map(|(version, image)| {
            let text = |key: &str| image.get(key).cloned().unwrap_or(Value::Null);
            let released_by = image
                .get("updated_by")
                .and_then(Value::as_str)
                .map_or(Value::Null, |actor| Value::from(subject(&subjects, actor)));
            (
                *version,
                json!({
                    "version": version,
                    "commit": text("commit_id"),
                    "releasedAt": text("updated_at"),
                    "releasedBy": released_by,
                }),
            )
        })
        .collect();
    let found = page(records, request.limit, |(version, _)| *version);
    Ok(Page {
        items: found.items.into_iter().map(|(_, record)| record).collect(),
        next: found.next,
    }
    .to_json())
}

fn compose(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = reading(cx)?;
    let ref_id = own_ref(&graph, "compose", "ref", param(params, "ref"))?;
    let result = graph.run("compose", |e| sync(e.compose(&ref_id)))?;
    tree_result(&graph, &result)
}

fn materialize(
    _: &Branches,
    cx: &OperationCx<'_, BranchesConfig>,
    params: &Value,
) -> Result<Value> {
    let graph = reading(cx)?;
    let commit = own_commit(&graph, "materialize", "commit", param(params, "commit"))?;
    let result = graph.run("materialize", |e| sync(e.materialize(&commit)))?;
    tree_result(&graph, &result)
}

fn released(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, _: &Value) -> Result<Value> {
    let graph = reading(cx)?;
    let root = root_of(cx.id());
    let found = graph.reader.get(
        format!(
            "SELECT 1 AS found FROM {} WHERE graph = ? AND root_id = ?",
            graph.t.release
        ),
        &[
            SqlValue::from(graph.name.as_str()),
            SqlValue::from(root.as_str()),
        ],
    )?;
    if found.is_none() {
        return Err(EngineError::new(
            EngineErrorCode::NotFound,
            format!("{} {} has not been released", cx.schema(), cx.id()),
        )
        .into());
    }
    let result = graph.run("released", |e| sync(e.released(&root)))?;
    let mut out = Map::new();
    out.insert(
        "release".into(),
        json!({"commit": result.release.commit, "version": result.release.version}),
    );
    if let Value::Object(tree) = tree_result(&graph, &result.tree)? {
        out.extend(tree);
    }
    Ok(Value::Object(out))
}

fn diff(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = reading(cx)?;
    let from = own_commit(&graph, "diff", "from", param(params, "from"))?;
    let to = own_commit(&graph, "diff", "to", param(params, "to"))?;
    let changes = graph.run("diff", |e| sync(e.diff(&from, &to)))?;
    let rows: Vec<Value> = changes
        .iter()
        .filter_map(|change| change.row.clone())
        .collect();
    let mut rows = rows_of(&graph, &rows)?.into_iter();
    let changes: Vec<Value> = changes
        .iter()
        .map(|change| {
            let mut out = Map::new();
            out.insert("kind".into(), Value::from(change.kind.as_str()));
            out.insert("entityKey".into(), Value::from(change.entity_key.as_str()));
            out.insert("operation".into(), Value::from(change.operation.as_str()));
            if change.row.is_some() {
                out.insert("row".into(), rows.next().unwrap_or(Value::Null));
            }
            Value::Object(out)
        })
        .collect();
    Ok(json!({"changes": changes}))
}

fn history(_: &Branches, cx: &OperationCx<'_, BranchesConfig>, params: &Value) -> Result<Value> {
    let graph = reading(cx)?;
    let ref_id = own_ref(&graph, "history", "ref", param(params, "ref"))?;
    let commits = graph.run("history", |e| sync(e.history(&ref_id)))?;
    let commits: Vec<Option<vg::Commit>> = commits.into_iter().map(Some).collect();
    Ok(json!({"commits": commits_of(&graph, &commits)?}))
}

// release_field is the release pointer's version, through its unique index
// on the graph and the root; absent before the first release.
fn release_field(_: &Branches, view: &InstanceView<'_, BranchesConfig>) -> Result<Option<Value>> {
    let sql = view.sql();
    let row = sql.get(
        format!(
            "SELECT _version FROM {} WHERE graph = ? AND root_id = ?",
            sql.table("release")?
        ),
        &[
            SqlValue::from(format!("{}/{}", view.namespace(), view.schema())),
            SqlValue::from(root_of(view.id())),
        ],
    )?;
    row.map(|row| Ok(Value::from(row.i64("_version")?)))
        .transpose()
}

fn after_change(
    _: &Branches,
    cx: &InstanceCx<'_, BranchesConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    match change {
        // An instance created before its schema composed Branches gets its
        // primary line at its first write.
        InstanceChange::Update { .. } | InstanceChange::Operation { .. } => {
            ensure_root(cx)?;
        }
        InstanceChange::Delete => delete_graph(cx)?,
        InstanceChange::Create => {}
    }
    Ok(())
}

// sweep is one run of the sweep schedule on the schema in a namespace.
fn sweep(_: &Branches, cx: &ScheduleCx<'_, BranchesConfig>) -> Result<()> {
    let Some(sweep) = cx.config().sweep else {
        return Ok(());
    };
    let sql = cx.sql();
    let graph = graph_of(cx, *sql, Some(sql), "")?;
    if let Some(abandon_after) = sweep.abandon_after {
        // Each idle draft is discarded through its instance's discard, so its
        // guards run and it appends its event; one a guard vetoes stays.
        let idle = graph.reader.all(
            format!(
                "SELECT r.id, r._version, o.id AS instance FROM {} AS r
                 JOIN {} AS o ON o.graph = r.graph AND o.root_id = r.root_id
                 WHERE r.graph = ? AND r.deleted_at IS NULL AND r.parent_ref_id IS NOT NULL AND r.updated_at < ?
                 ORDER BY r.updated_at, r.id",
                graph.t.ref_, graph.t.roots
            ),
            &[
                SqlValue::from(graph.name.as_str()),
                SqlValue::Integer(cx.now().saturating_sub(abandon_after).saturating_mul(1000)),
            ],
        )?;
        for draft in &idle {
            let params = json!({"ref": draft.text("id")?, "version": draft.i64("_version")?});
            match cx.instances().invoke(
                cx.schema(),
                draft.text("instance")?,
                "discard",
                &params,
                None,
            ) {
                Ok(_) => {}
                Err(error) if error.is_veto() => {}
                Err(error) => return Err(error),
            }
        }
    }
    let actor = actor_of(&graph, &cx.principal().subject)?;
    let options = vg::SweepOptions {
        actor,
        discard_grace: Duration::from_millis(
            u64::try_from(sweep.discard_grace.unwrap_or(0)).unwrap_or(0),
        ),
        abandon_after: Duration::ZERO,
        prune_batch: sweep.prune_batch.unwrap_or(0),
    };
    graph.run_raw(|e| sync(e.sweep(&options)))?;
    Ok(())
}

/// Branches: a version graph on each instance, kept in the behavior's own
/// tables on SQLite or Postgres.
#[derive(Clone, Copy, Debug, Default)]
pub struct Branches;

impl Behavior for Branches {
    type Config = BranchesConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Branches.behavior.json")
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<BranchesConfig> {
        parse_config(raw, target)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, change| {
                config_change(before, after, &|| change.instances())
            }),
            guidance: Some(|_, config, target| crate::guidance::branches::guidance(config, target)),
            migrations: migrations(),
            initialize: Some(|_, cx, _| {
                ensure_root(cx)?;
                Ok(())
            }),
            operations: vec![
                ("branch", branch),
                ("save", save),
                ("commit", commit),
                ("seal", seal),
                ("merge", merge),
                ("rebase", rebase),
                ("revert", revert),
                ("releaseCommit", release_commit),
                ("discard", discard),
                ("refs", refs),
                ("releases", releases),
                ("compose", compose),
                ("materialize", materialize),
                ("released", released),
                ("diff", diff),
                ("history", history),
            ],
            fields: vec![("release", release_field)],
            after_change: Some(after_change),
            schedules: vec![(
                "sweep",
                Schedule {
                    every: Every::Config(|_, config: &BranchesConfig| {
                        Ok(config.sweep.map(|sweep| sweep.interval_ms))
                    }),
                    run: sweep,
                },
            )],
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// RFC 9562, Appendix A.4: the DNS namespace and www.example.com.
    #[test]
    fn uuid_v5_is_rfc_9562s_version_5_uuid_in_its_canonical_form() {
        let id = uuid_v5("6ba7b810-9dad-11d1-80b4-00c04fd430c8", "www.example.com");
        assert_eq!(
            vg::canonical::uuid_hyphenated(&id).expect("hyphenated"),
            "2ed6657d-e927-568b-95e1-2665a8aea6a2"
        );
        let actor =
            vg::canonical::uuid_hyphenated(&uuid_v5(ACTOR_NAMESPACE, "alice")).expect("hyphenated");
        assert_eq!(actor.chars().nth(14), Some('5'));
        assert!(id.len() <= 22 && id.chars().all(|c| c.is_ascii_alphanumeric()));
        // A namespace in its canonical form names the same one.
        let canonical =
            vg::canonical::uuid("6ba7b810-9dad-11d1-80b4-00c04fd430c8").expect("canonical");
        assert_eq!(uuid_v5(&canonical, "www.example.com"), id);
    }

    /// An issue's path becomes a JSON pointer under its row, as the
    /// TypeScript regular expression reads it.
    #[test]
    fn an_issue_path_is_a_pointer_under_its_row() {
        assert_eq!(pointer("/edits", "step"), "/edits/step");
        assert_eq!(pointer("/r", "steps[0].name"), "/r/steps/0/name");
        assert_eq!(pointer("/r", ""), "/r");
        assert_eq!(pointer("/r", "a/b~c"), "/r/a~1b~0c");
        assert_eq!(pointer("/r", "x[a]"), "/r/x/a");
        assert_eq!(pointer("/r", "x[12]"), "/r/x/12");
    }

    /// The veto codes come from the declaration.
    #[test]
    fn the_veto_codes_are_the_declarations() {
        assert_eq!(VETOES.len(), 12);
        assert!(VETOES.iter().any(|code| code == "primary_line"));
    }

    /// Migration 1 runs the layout as the version graph's engine gives it,
    /// and a file it already created keeps what it created then: were the
    /// statements to change, a new file would get the new layout and an old
    /// one would not, so the change must come as migration 2. These are the
    /// TypeScript adapter's statements byte for byte, the hash
    /// `test/core-branches.test.ts` pins.
    #[test]
    fn the_first_migrations_layout_is_pinned_and_is_the_typescript_adapters() {
        let statements = fixed::layout(fixed::Dialect::Sqlite, &|local| {
            format!("bhv_branches__{local}")
        })
        .expect("the layout");
        assert_eq!(
            (
                statements.len(),
                superschematic_engine::json::sha256_hex(&statements.join("\n"))
            ),
            (
                21,
                "f3583c44e0634b535f54adb1637dabed879ee18179e53327c13334a11e45f49d".to_owned()
            )
        );
    }

    fn field(
        key: &str,
        type_name: &str,
        kind: ConfigFieldKind,
        json_type: Option<&str>,
        depth: u8,
    ) -> ConfigTypeField {
        ConfigTypeField {
            key: key.to_owned(),
            type_name: type_name.to_owned(),
            kind,
            json_type: json_type.map(str::to_owned),
            depth,
            optional: true,
        }
    }

    /// A field's value class follows graphdesc's rule: a primitive's by its
    /// name, an enum's, a type's json, a catalog scalar's the catalog's, a
    /// document scalar's by its JSON type, and [] per list level.
    #[test]
    fn a_fields_value_class_follows_graphdescs_rule() {
        use ConfigFieldKind::{Enum, Primitive, Scalar, Type};
        let cases = [
            (field("s", "string", Primitive, None, 0), Some("string")),
            (field("S", "String", Primitive, None, 0), Some("string")),
            (field("id2", "ID", Primitive, None, 0), Some("string")),
            (field("n", "Int", Primitive, None, 0), Some("integer")),
            (field("f", "number", Primitive, None, 0), Some("number")),
            (field("F", "Float", Primitive, None, 0), Some("number")),
            (field("b", "boolean", Primitive, None, 0), Some("boolean")),
            (field("B", "Boolean", Primitive, None, 0), Some("boolean")),
            (field("e", "Unit", Enum, None, 0), Some("enum")),
            (field("t", "Step", Type, None, 0), Some("json")),
            (field("tl", "Step", Type, None, 1), Some("json[]")),
            (field("l", "string", Primitive, None, 1), Some("string[]")),
            (field("ll", "Int", Primitive, None, 2), Some("integer[][]")),
            (
                field("u", "Identity.UUID", Scalar, Some("object"), 0),
                Some("uuid"),
            ),
            (
                field("dt", "Temporal.DateTime", Scalar, Some("string"), 0),
                Some("dateTime"),
            ),
            (
                field("d", "Temporal.Date", Scalar, Some("string"), 0),
                Some("date"),
            ),
            (
                field("tm", "Temporal.Time", Scalar, Some("string"), 0),
                Some("time"),
            ),
            (
                field("dur", "Temporal.Duration", Scalar, Some("string"), 0),
                Some("duration"),
            ),
            (
                field("i64", "Generic.Int64", Scalar, Some("integer"), 0),
                Some("integer"),
            ),
            (
                field("p", "Generic.Probability", Scalar, Some("number"), 0),
                Some("number"),
            ),
            (
                field("em", "Contact.Email", Scalar, Some("string"), 1),
                Some("string[]"),
            ),
            (
                field("grams", "Grams", Scalar, Some("integer"), 0),
                Some("integer"),
            ),
            (
                field("ratio", "Ratio", Scalar, Some("number"), 0),
                Some("number"),
            ),
            (
                field("flag", "Flag", Scalar, Some("boolean"), 0),
                Some("boolean"),
            ),
            (
                field("code", "Code", Scalar, Some("string"), 0),
                Some("string"),
            ),
            (
                field("blob", "Blob", Scalar, Some("object"), 0),
                Some("json"),
            ),
            (field("arr", "Arr", Scalar, Some("array"), 0), Some("json")),
            (
                field("anything", "Anything", Scalar, Some("any"), 0),
                Some("json"),
            ),
            (field("at", "Geo.Location", Scalar, Some("object"), 0), None),
        ];
        for (field, class) in cases {
            assert_eq!(value_class(&field).as_deref(), class, "{}", field.key);
        }
    }

    /// Each kind has the fixed role and audit columns beside its fields,
    /// updated_by as its author and actor, and images that exclude nothing.
    #[test]
    fn the_descriptor_gives_each_kind_its_roles_beside_its_fields() {
        let classes = |fields: &[(&str, &str)]| -> Vec<(String, String)> {
            fields
                .iter()
                .map(|(key, class)| ((*key).to_owned(), (*class).to_owned()))
                .collect()
        };
        let mut kinds = BTreeMap::new();
        kinds.insert(
            "step".to_owned(),
            BranchesKind {
                type_name: "Step".into(),
                parent: None,
                order: Some("position".into()),
                singleton: false,
                units: vec![
                    ("timings".into(), BranchesUnit::Keyed),
                    ("notes".into(), BranchesUnit::Excluded),
                ],
                retention_days: Some(30),
                fields: classes(&[
                    ("instruction", "string"),
                    ("position", "integer"),
                    ("timings", "json"),
                    ("notes", "string"),
                ]),
            },
        );
        kinds.insert(
            "ingredient".to_owned(),
            BranchesKind {
                type_name: "Ingredient".into(),
                parent: Some(BranchesParent {
                    key: "stepKey".into(),
                    of: "step".into(),
                }),
                order: None,
                singleton: false,
                units: Vec::new(),
                retention_days: None,
                fields: classes(&[("stepKey", "uuid")]),
            },
        );
        kinds.insert(
            "cover".to_owned(),
            BranchesKind {
                type_name: "Ingredient".into(),
                parent: None,
                order: None,
                singleton: true,
                units: Vec::new(),
                retention_days: None,
                fields: classes(&[("stepKey", "uuid")]),
            },
        );
        let config = BranchesConfig::new("Recipe", kinds, DEFAULT_PRIMARY.to_owned(), 64, None)
            .expect("a config the version graph's core takes");
        let descriptor: Value = serde_json::from_str(&config.descriptor).expect("JSON");
        let mut shape = descriptor.clone();
        shape["kinds"] = Value::Array(
            descriptor["kinds"]
                .as_array()
                .expect("kinds")
                .iter()
                .map(|kind| kind["kind"].clone())
                .collect(),
        );
        assert_eq!(
            shape,
            json!({
                "version": 3, "graph": "Recipe", "root": {"table": "Recipe", "key": "id"},
                "refTable": "ref", "commitTable": "commit", "patchTable": "patch",
                "releaseTable": "release", "snapshotTable": "snapshot_entry",
                "kinds": ["cover", "ingredient", "step"]
            })
        );
        let mut columns = json!({
            "id": "uuid", "entity_key": "uuid", "ref_id": "uuid", "root_id": "uuid",
            "deleted_on_ref": "boolean", "_version": "integer", "created_at": "dateTime",
            "created_by": "uuid", "updated_at": "dateTime", "updated_by": "uuid"
        });
        for (field, class) in [
            ("instruction", "string"),
            ("position", "integer"),
            ("timings", "json"),
            ("notes", "string"),
        ] {
            columns[field] = json!(class);
        }
        assert_eq!(
            descriptor["kinds"][2],
            json!({
                "kind": "step", "table": "step", "historyTable": "step_history", "key": "entity_key",
                "id": "id", "ref": "ref_id", "root": "root_id", "tombstone": "deleted_on_ref",
                "version": "_version", "author": "updated_by", "order": "position",
                "units": {"timings": "keyed"},
                "excluded": ["created_at", "created_by", "notes", "root_id", "updated_at"],
                "history": {"retentionDays": 30, "exclude": [], "actor": "updated_by"},
                "columns": columns
            })
        );
        assert_eq!(
            [
                &descriptor["kinds"][0]["singleton"],
                &descriptor["kinds"][1]["parent"],
                &descriptor["kinds"][1]["history"]
            ],
            [
                &json!(true),
                &json!({"key": "stepKey", "kind": "step"}),
                &json!({"exclude": [], "actor": "updated_by"})
            ]
        );
    }

    /// A JavaScript String() of each JSON value.
    #[test]
    fn values_read_as_javascripts_string_reads_them() {
        assert_eq!(js_string(&json!("a")), "a");
        assert_eq!(js_string(&json!(5)), "5");
        assert_eq!(js_string(&json!(null)), "null");
        assert_eq!(js_string(&json!(["a", null, 2])), "a,,2");
        assert_eq!(js_string(&json!({"a": 1})), "[object Object]");
    }
}
