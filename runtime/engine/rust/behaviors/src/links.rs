//! Links, the core's typed links between instances (D16, amended; D56),
//! the port of `src/behaviors/core/links.ts`. The config names each link,
//! the schema of the instance it points at, whether it is required, whether
//! it is pinned, and whether it holds many targets. A link holds one target
//! at a time, an instance of its schema in the same namespace, looked up as
//! any schema name is (the namespace, then the shared one); `link` points
//! it at a target, replacing the one it had; `unlink` clears it. A
//! many-valued link (`many`, with a `max` from 1 to 100) holds a list of
//! them, in the order they were linked: `link` adds a target, or pins again
//! one it holds, refusing one past max (`too_many`), and `unlink` removes
//! the one it names (`not_linked` when the link does not hold it). A create
//! may give links too, by name, in its parameters (initialize), a list for
//! a many-valued link with each target once, held to link's checks in the
//! create's transaction, so an instance holds them from its first event.
//! One read-only field, `targets`, holds every link the instance has, since
//! a declaration's fields are fixed and the config's names are not; a read
//! returns it as `Links.targets`, with a list for each many-valued link.
//!
//! A pinned link pins a revision of Revisions or a release of Branches, as
//! its config's `pinned` says (true and `"revision"` pin a revision,
//! `"release"` a release), and needs a target schema that composes that
//! behavior: `link` records the target's latest revision or release, or an
//! earlier one it names, and the field reports the target's latest beside
//! it (`latest`) and whether the target has moved past it (`stale`), read
//! through the target's revision or release field as the caller, one read
//! per target schema. Both move at a read, with no event of the linking
//! instance, so the field is volatile (D56). A row keeps a revision and a
//! release in columns of their own, so a config that changes what a link
//! pins finds no pin of the new kind in a row until the link is linked
//! again.
//!
//! Each target of a link is also a reference the engine records, under the
//! link's name, which hears the target's delete alone: no other change of
//! the target asks Links anything, so a target thousands of instances link
//! to costs its writes none of them. A required link is given at create,
//! which refuses an instance without it, can be moved and not unlinked, and
//! the delete of the target it points at is refused by `guard_reference`,
//! whoever the caller: an instance always holds it. A required many-valued
//! link holds at least one target: unlinking its last is refused, and so is
//! the delete of its last target. Any other target can be deleted:
//! `after_reference_change` unlinks it on each instance that holds it, as
//! the caller, that one target of a many-valued link, and that instance's
//! own event records it. Deleting an instance deletes its links.
//!
//! Every veto carries a code the declaration lists: `no_revision` and
//! `no_release` for a pinned link's target with none yet, at link or at
//! create (with the pointer of the create's entry), `too_many` for a target
//! past a many-valued link's max, `not_linked` for unlinking a target the
//! link does not hold, `required_link` for unlinking a required link or the
//! last target of a required many-valued one, and `required_target` for
//! deleting what one points at.
//!
//! `listLinked` is schema-level: the instances of the schema whose link
//! points at a target, or holds it, with each pinned link's pin, the
//! target's latest and staleness, a page at a time, which answers which
//! instances point at a superseded revision or release.
//!
//! Storage (D58): the table keeps a row per link, or per target of a
//! many-valued link, under a row number the database assigns (SQLite's
//! rowid, a Postgres identity column), which orders a many-valued link's
//! targets and `listLinked`'s pages on both databases. Its third migration
//! keys a row by its target as well as its link's name: on SQLite, as the
//! TypeScript engine does, by building the table again with its rows; on
//! Postgres, whose first migration keys the table with a unique index of
//! its own name, by swapping that index for the new key. A read of an
//! instance's links orders them by name in byte order, which Postgres reads
//! with the C collation.
//!
//! configChange: while the schema has instances, every link keeps its name
//! and schema and whether it holds many targets, and a many-valued link's
//! max does not fall, since instances may hold it; pinned may change (a
//! link made before its spec was pinned, or while it was pinned to the
//! other kind, records no pin of the kind until it is linked again), a
//! required link may become optional, max may rise, and optional links may
//! be added. A link that becomes required, or a new required one, is
//! refused, as a field made required is: an instance the live version
//! accepts may not hold it. A schema with no instance, in any namespace
//! that reads it, may change the config in any way, since no instance holds
//! a link or lacks one. Links can be added to a schema that has instances,
//! which start with none, unless it has a required link, and cannot be
//! removed from one: their links and references would stay behind.

use std::collections::HashMap;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, ConfigChange, Hears, IndexSpec, InstanceCx,
    InstanceView, Page, Reference, Scope, TableSpec, MAX_BATCH_READ,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Dialect, Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, Error, GuardRequest, Hooks, InstanceChange, Result,
};

const NAME: &str = "Links";

const COLUMNS: &str = "row, name, target_schema, target_id, revision, release";

/// What a pinned link pins: a revision of Revisions, or a release of
/// Branches.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum LinkPin {
    Revision,
    Release,
}

impl LinkPin {
    /// The kind as a config and a message write it.
    pub fn as_str(self) -> &'static str {
        match self {
            LinkPin::Revision => "revision",
            LinkPin::Release => "release",
        }
    }

    /// The behavior the target's schema composes for the kind.
    pub fn behavior(self) -> &'static str {
        match self {
            LinkPin::Revision => "Revisions",
            LinkPin::Release => "Branches",
        }
    }

    /// The target's field that holds its latest of the kind (and the
    /// column a row keeps the pin in, and link's parameter for an earlier
    /// one).
    pub fn field(self) -> &'static str {
        self.as_str()
    }

    /// The veto when the target has none yet.
    fn code(self) -> &'static str {
        match self {
            LinkPin::Revision => "no_revision",
            LinkPin::Release => "no_release",
        }
    }

    // The target's field by qualified name.
    fn path(self) -> String {
        format!("{}.{}", self.behavior(), self.field())
    }
}

/// What a link of a Links config, as a schema holds it, pins (`linkPin`):
/// pinned true or `"revision"` pins a revision, `"release"` a release; any
/// other value pins nothing.
pub fn link_pin(link: &Value) -> Option<LinkPin> {
    match link.get("pinned") {
        Some(Value::Bool(true)) => Some(LinkPin::Revision),
        Some(Value::String(kind)) if kind == "revision" => Some(LinkPin::Revision),
        Some(Value::String(kind)) if kind == "release" => Some(LinkPin::Release),
        _ => None,
    }
}

/// Whether a link of a Links config, as a schema holds it, holds a list of
/// targets (`linkMany`): many true. What reads one target of a link refuses
/// one that does.
pub fn link_many(link: &Value) -> bool {
    link.get("many") == Some(&Value::Bool(true))
}

/// One link of a Links config.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LinkSpec {
    /// The schema of the instance the link points at.
    pub schema: String,
    /// Every create gives it; it is moved, never unlinked, and its target's
    /// delete is refused; a many-valued one keeps at least one target.
    pub required: bool,
    /// What it pins of the target; `None` when it pins nothing.
    pub pin: Option<LinkPin>,
    /// Whether it holds a list of targets, in the order they were linked.
    pub many: bool,
    /// For a many-valued link, the most targets it holds.
    pub max: Option<i64>,
}

/// Links' config, parsed: each link by name, in the config's order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LinksConfig {
    pub links: Vec<(String, LinkSpec)>,
}

impl LinksConfig {
    /// A link by name; `None` for a name the config does not give.
    pub fn get(&self, name: &str) -> Option<&LinkSpec> {
        self.links
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, link)| link)
    }

    /// Its links' names, in the config's order.
    pub fn names(&self) -> Vec<&str> {
        self.links.iter().map(|(name, _)| name.as_str()).collect()
    }
}

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// integer reads a parameter its schema holds to an integer, which JSON may
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

// pinned reads a pin a target holds: its revision or release field, a
// number; None when it has none yet.
fn pinned(record: &superschematic_engine::InstanceRecord, pin: LinkPin) -> Option<i64> {
    match record.behavior_field(pin.behavior(), pin.field()) {
        Some(Value::Number(number)) => Some(number_value(number) as i64),
        _ => None,
    }
}

// spec returns a link's spec, or refuses a name the config does not give.
fn spec<'c>(scope: &Scope<'c, LinksConfig>, operation: &str, name: &str) -> Result<&'c LinkSpec> {
    let config = scope.config();
    config.get(name).ok_or_else(|| {
        EngineError::operation_params(
            NAME,
            operation,
            vec![SchemaIssue::new(
                "/name",
                format!(
                    "{} has no link {name} (its links: {})",
                    scope.schema(),
                    config.names().join(", ")
                ),
            )],
        )
        .into()
    })
}

// latest reads a target's latest revision or release, through its revision
// or release field, as the caller; None when it has none yet.
fn latest<C>(scope: &Scope<'_, C>, pin: LinkPin, schema: &str, id: &str) -> Result<Option<i64>> {
    let path = pin.path();
    Ok(scope
        .instances()
        .get(schema, id, Some(&[path.as_str()]))?
        .and_then(|record| pinned(&record, pin)))
}

// held_pin is the pin a row keeps of the kind the link pins; None when it
// pins nothing, or keeps none of that kind.
fn held_pin(row: &Row, pin: Option<LinkPin>) -> Result<Option<i64>> {
    match pin {
        None => Ok(None),
        Some(pin) => Ok(row.opt_i64(pin.field())?),
    }
}

// held reads the instance's link rows, by name, each link's in the order
// its targets were linked. Names sort in byte order on both databases.
fn held(view: &InstanceView<'_, LinksConfig>) -> Result<Vec<Row>> {
    let table = view.sql().table("links")?;
    let select = format!(
        "SELECT {COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY name"
    );
    view.sql().all(
        Sql::per(
            format!("{select}, row"),
            format!("{select} COLLATE \"C\", row"),
        ),
        &key(view),
    )
}

// held_by reads the rows of one link of the instance, in the order its
// targets were linked.
fn held_by<C>(view: &InstanceView<'_, C>, name: &str) -> Result<Vec<Row>> {
    let table = view.sql().table("links")?;
    let mut values = key(view);
    values.push(SqlValue::from(name));
    view.sql().all(
        format!("SELECT {COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ? ORDER BY row"),
        &values,
    )
}

/// Where a link's checks point when they refuse: link's own parameters, or
/// a create's entry of its parameters, or the item of a many-valued link's
/// list (`index`), the item itself for its id when it is the id alone.
#[derive(Clone, Copy)]
enum Refusals<'a> {
    Operation,
    Create {
        name: &'a str,
        index: Option<usize>,
        id_only: bool,
    },
}

impl Refusals<'_> {
    // param refuses at a parameter: the link's name, the target's id, the
    // revision or the release.
    fn param(self, cx: &InstanceCx<'_, LinksConfig>, at: &str, message: String) -> Error {
        match self {
            Refusals::Operation => EngineError::operation_params(
                NAME,
                "link",
                vec![SchemaIssue::new(format!("/{at}"), message)],
            )
            .into(),
            Refusals::Create {
                name,
                index,
                id_only,
            } => {
                let entry = format!("/behaviors/{NAME}/{name}");
                let item = index.map_or_else(|| entry.clone(), |index| format!("{entry}/{index}"));
                let path = if at == "name" {
                    entry
                } else if at == "id" && id_only {
                    item
                } else {
                    format!("{item}/{at}")
                };
                EngineError::create_params(cx.schema(), vec![SchemaIssue::new(path, message)])
                    .into()
            }
        }
    }

    // veto refuses with one of the declared codes; a create's carries the
    // pointer of its entry.
    fn veto(self, cx: &InstanceCx<'_, LinksConfig>, reason: String, code: &str) -> Error {
        let (action, details) = match self {
            Refusals::Operation => ("link", None),
            Refusals::Create { name, index, .. } => {
                let entry = format!("/behaviors/{NAME}/{name}");
                let item = index.map_or_else(|| entry.clone(), |index| format!("{entry}/{index}"));
                let mut details = Map::new();
                details.insert("path".into(), Value::from(item));
                ("create", Some(details))
            }
        };
        Error::veto(
            NAME,
            action,
            cx.schema(),
            cx.id(),
            Veto {
                reason,
                code: Some(code.to_owned()),
                details,
            },
        )
    }
}

/// What link and a create's entry name of a target: its id, and an earlier
/// revision or release to pin.
struct LinkTarget<'a> {
    id: &'a str,
    revision: Option<i64>,
    release: Option<i64>,
}

impl LinkTarget<'_> {
    fn of(&self, pin: LinkPin) -> Option<i64> {
        match pin {
            LinkPin::Revision => self.revision,
            LinkPin::Release => self.release,
        }
    }
}

// target_of reads a target as a create's entry gives it: its id, or an
// object with its id and a pin.
fn target_of(value: &Value) -> LinkTarget<'_> {
    match value {
        Value::String(id) => LinkTarget {
            id,
            revision: None,
            release: None,
        },
        _ => LinkTarget {
            id: value.get("id").and_then(Value::as_str).unwrap_or_default(),
            revision: integer(value.get("revision")),
            release: integer(value.get("release")),
        },
    }
}

// set_link points a link at a target, with link's checks: a revision only
// for a link that pins revisions, whose schema composes Revisions, a
// release only for one that pins releases, whose schema composes Branches,
// each no later than the target's latest; a target that exists, read as the
// caller, with a revision or a release to pin; and for a many-valued link
// that does not hold it yet, room under max. It records the link and its
// reference, replacing the target a link that holds one had in its row,
// adding the target to a many-valued link or pinning again the one it
// holds, and returns what link returns.
fn set_link(
    cx: &InstanceCx<'_, LinksConfig>,
    name: &str,
    link: &LinkSpec,
    given: &LinkTarget<'_>,
    refuse: Refusals<'_>,
) -> Result<Value> {
    let pin = link.pin;
    for kind in [LinkPin::Revision, LinkPin::Release] {
        if given.of(kind).is_some() && pin != Some(kind) {
            let how = match pin {
                None => "is not pinned".to_owned(),
                Some(pin) => format!("is pinned to a {}", pin.as_str()),
            };
            return Err(refuse.param(
                cx,
                kind.as_str(),
                format!("link {name} {how}, so it records no {}", kind.as_str()),
            ));
        }
    }
    if let Some(pin) = pin {
        if cx.schemas().config(&link.schema, pin.behavior())?.is_none() {
            return Err(refuse.param(
                cx,
                "name",
                format!(
                    "link {name} is pinned to a {}, but {} does not compose {}",
                    pin.as_str(),
                    link.schema,
                    pin.behavior()
                ),
            ));
        }
    }
    let path = pin.map(LinkPin::path);
    let fields: Vec<&str> = path.iter().map(String::as_str).collect();
    let Some(target) = cx.instances().get(&link.schema, given.id, Some(&fields))? else {
        return Err(refuse.param(
            cx,
            "id",
            format!("{} {} does not exist", link.schema, given.id),
        ));
    };
    let mut value = None;
    if let Some(pin) = pin {
        let Some(current) = pinned(&target, pin) else {
            return Err(refuse.veto(
                cx,
                format!(
                    "{} {} has no {} to pin yet",
                    link.schema,
                    given.id,
                    pin.as_str()
                ),
                pin.code(),
            ));
        };
        let wanted = given.of(pin);
        if let Some(wanted) = wanted.filter(|wanted| *wanted > current) {
            return Err(refuse.param(
                cx,
                pin.as_str(),
                format!(
                    "{} {} has {}s 1 to {current}, not {wanted}",
                    link.schema,
                    given.id,
                    pin.as_str()
                ),
            ));
        }
        value = Some(wanted.unwrap_or(current));
    }
    let revision = value.filter(|_| pin == Some(LinkPin::Revision));
    let release = value.filter(|_| pin == Some(LinkPin::Release));
    let table = cx.sql().table("links")?;
    // A link that holds one target keeps one row, which a move updates; a
    // many-valued link keeps a row per target, which pinning again updates.
    let rows = held_by(cx, name)?;
    let row = if link.many {
        let mut found = None;
        for row in &rows {
            if row.text("target_id")? == given.id {
                found = Some(row);
                break;
            }
        }
        found
    } else {
        rows.first()
    };
    let pins = [
        SqlValue::from(revision),
        SqlValue::from(release),
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::Integer(cx.now()),
    ];
    match row {
        None => {
            if let Some(max) = link
                .max
                .filter(|max| link.many && rows.len() as i64 >= *max)
            {
                return Err(refuse.veto(
                    cx,
                    format!("link {name} holds {max} targets already, its max"),
                    "too_many",
                ));
            }
            let mut values = key(cx);
            values.extend([
                SqlValue::from(name),
                SqlValue::from(link.schema.as_str()),
                SqlValue::from(given.id),
            ]);
            values.extend(pins);
            cx.sql().run(
                format!(
                    "INSERT INTO {table} (namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at)
                     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
                ),
                &values,
            )?;
        }
        Some(row) => {
            if !link.many {
                cx.references()
                    .remove(row.text("target_schema")?, row.text("target_id")?, name)?;
            }
            let mut values = vec![
                SqlValue::from(link.schema.as_str()),
                SqlValue::from(given.id),
            ];
            values.extend(pins);
            values.push(SqlValue::Integer(row.i64("row")?));
            cx.sql().run(
                format!("UPDATE {table} SET target_schema = ?, target_id = ?, revision = ?, release = ?, created_by = ?, created_at = ? WHERE row = ?"),
                &values,
            )?;
        }
    }
    cx.references()
        .add(&link.schema, given.id, name, Some(Hears::Delete))?;
    let mut out = Map::new();
    out.insert("name".into(), Value::from(name));
    out.insert("schema".into(), Value::from(link.schema.as_str()));
    out.insert("id".into(), Value::from(given.id));
    if let Some(revision) = revision {
        out.insert("revision".into(), Value::from(revision));
    }
    if let Some(release) = release {
        out.insert("release".into(), Value::from(release));
    }
    Ok(Value::Object(out))
}

// link_of is a link as the field and listLinked give it: the pin of the
// kind the link pins, and with the target's latest of that kind, the
// latest and whether the target has moved past the pin.
fn link_of(row: &Row, current: Option<i64>, pin: Option<LinkPin>) -> Result<Map<String, Value>> {
    let mut out = Map::new();
    out.insert("schema".into(), Value::from(row.text("target_schema")?));
    out.insert("id".into(), Value::from(row.text("target_id")?));
    let (Some(pin), Some(value)) = (pin, held_pin(row, pin)?) else {
        return Ok(out);
    };
    out.insert(pin.as_str().into(), Value::from(value));
    if let Some(current) = current {
        out.insert("latest".into(), Value::from(current));
        out.insert("stale".into(), Value::Bool(current > value));
    }
    Ok(out)
}

// latest_of reads the latest revision or release of each pinned target, as
// the caller, one read per target schema (MAX_BATCH_READ ids at a time):
// by schema, id and pin, without the targets that have none yet.
fn latest_of(
    view: &InstanceView<'_, LinksConfig>,
    wanted: &[(String, String, LinkPin)],
) -> Result<HashMap<(String, String, LinkPin), i64>> {
    let mut by_schema: Vec<(&str, Vec<&str>, Vec<LinkPin>)> = Vec::new();
    for (schema, id, pin) in wanted {
        let at = match by_schema.iter().position(|(known, _, _)| known == schema) {
            Some(at) => at,
            None => {
                by_schema.push((schema, Vec::new(), Vec::new()));
                by_schema.len() - 1
            }
        };
        let entry = &mut by_schema[at];
        if !entry.1.contains(&id.as_str()) {
            entry.1.push(id);
        }
        if !entry.2.contains(pin) {
            entry.2.push(*pin);
        }
    }
    let mut found = HashMap::new();
    for (schema, ids, pins) in &by_schema {
        let paths: Vec<String> = pins.iter().map(|pin| pin.path()).collect();
        let fields: Vec<&str> = paths.iter().map(String::as_str).collect();
        for chunk in ids.chunks(MAX_BATCH_READ) {
            for (id, record) in view.instances().get_many(schema, chunk, Some(&fields))? {
                found.insert((schema.to_string(), id), record);
            }
        }
    }
    let mut out = HashMap::new();
    for (schema, id, pin) in wanted {
        if let Some(value) = found
            .get(&(schema.clone(), id.clone()))
            .and_then(|record| pinned(record, *pin))
        {
            out.insert((schema.clone(), id.clone(), *pin), value);
        }
    }
    Ok(out)
}

// The table as Links' first migration made it: a row per link, keyed by
// its name. Postgres keys it with a unique index of its own name, which the
// third migration swaps for the key by target.
fn first_table(dialect: Dialect) -> TableSpec {
    let text = || ColumnSpec::text().not_null();
    let table = TableSpec::new("links")
        .row_id("row")
        .column("namespace", text())
        .column("schema", text())
        .column("id", text())
        .column("name", text())
        .column("target_schema", text())
        .column("target_id", text())
        .column("revision", ColumnSpec::integer());
    let table = table
        .column("created_by", text())
        .column("created_at", ColumnSpec::integer().not_null());
    let keyed = match dialect {
        Dialect::Sqlite => table.unique(&["namespace", "schema", "id", "name"]),
        Dialect::Postgres => table
            .index(IndexSpec::new("links_key", &["namespace", "schema", "id", "name"]).unique()),
    };
    keyed.index(by_target())
}

// The index listLinked reads a target's links through, in row order.
fn by_target() -> IndexSpec {
    IndexSpec::new(
        "links_by_target",
        &["namespace", "schema", "name", "target_id", "row"],
    )
}

fn migrations() -> Vec<BehaviorMigration> {
    vec![
        BehaviorMigration::new(1, "links").up(|sql| sql.create_table(&first_table(sql.dialect()))),
        // A link that pins a release of Branches keeps it beside the
        // revision column, which a link that pins a revision of Revisions
        // keeps.
        BehaviorMigration::new(2, "release pins").up(|sql| {
            sql.run(
                format!(
                    "ALTER TABLE {} ADD COLUMN release {}",
                    sql.table("links")?,
                    ColumnSpec::integer().definition(sql.dialect())?
                ),
                &[],
            )?;
            Ok(())
        }),
        // A many-valued link keeps a row per target, so a row is keyed by
        // its target as well as its link's name.
        BehaviorMigration::new(3, "many-valued links").up(|sql| {
            let links = sql.table("links")?;
            let key = "namespace, schema, id, name, target_schema, target_id";
            match sql.dialect() {
                // SQLite cannot change a table's key: the table is built
                // again with it, its rows copied with their row, which
                // listLinked pages by and a many-valued link's targets are
                // ordered by.
                Dialect::Sqlite => {
                    let text = || ColumnSpec::text().not_null();
                    sql.create_table(
                        &TableSpec::new("links_next")
                            .row_id("row")
                            .column("namespace", text())
                            .column("schema", text())
                            .column("id", text())
                            .column("name", text())
                            .column("target_schema", text())
                            .column("target_id", text())
                            .column("revision", ColumnSpec::integer())
                            .column("release", ColumnSpec::integer())
                            .column("created_by", text())
                            .column("created_at", ColumnSpec::integer().not_null())
                            .unique(&[
                                "namespace",
                                "schema",
                                "id",
                                "name",
                                "target_schema",
                                "target_id",
                            ]),
                    )?;
                    let next = sql.table("links_next")?;
                    let columns = "row, namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at";
                    sql.run(
                        format!("INSERT INTO {next} ({columns}) SELECT {columns} FROM {links}"),
                        &[],
                    )?;
                    sql.run(format!("DROP TABLE {links}"), &[])?;
                    sql.run(format!("ALTER TABLE {next} RENAME TO {links}"), &[])?;
                    sql.run(
                        format!(
                            "CREATE INDEX {} ON {links} (namespace, schema, name, target_id, row)",
                            sql.table("links_by_target")?
                        ),
                        &[],
                    )?;
                }
                // Postgres swaps the unique index the first migration named.
                Dialect::Postgres => {
                    sql.run(format!("DROP INDEX {}", sql.table("links_key")?), &[])?;
                    sql.run(
                        format!(
                            "CREATE UNIQUE INDEX {} ON {links} ({key})",
                            sql.table("links_target_key")?
                        ),
                        &[],
                    )?;
                }
            }
            Ok(())
        }),
    ]
}

/// Links: typed links from an instance to others, by name.
#[derive(Clone, Copy, Debug, Default)]
pub struct Links;

impl Behavior for Links {
    type Config = LinksConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Links.behavior.json")
    }

    // The configSchema holds the shape: each link's schema, and a max for
    // a many-valued one alone.
    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<LinksConfig> {
        let empty = Map::new();
        let given = raw
            .get("links")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let links = js_keys(given)
            .into_iter()
            .map(|name| {
                let link = &given[name];
                let many = link_many(link);
                (
                    name.to_owned(),
                    LinkSpec {
                        schema: link
                            .get("schema")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned(),
                        required: link.get("required") == Some(&Value::Bool(true)),
                        pin: link_pin(link),
                        many,
                        max: if many { integer(link.get("max")) } else { None },
                    },
                )
            })
            .collect();
        Ok(LinksConfig { links })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(config_change),
            guidance: Some(|_, config, target| crate::guidance::links::guidance(config, target)),
            create_params_schema: Some(|_, config, _| {
                crate::guidance::links::create_params(config)
            }),
            migrations: migrations(),
            initialize: Some(initialize),
            operations: vec![
                ("link", |_, cx, params| {
                    let name = params
                        .get("name")
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    let link = spec(cx, "link", name)?;
                    let given = LinkTarget {
                        id: params.get("id").and_then(Value::as_str).unwrap_or_default(),
                        revision: integer(params.get("revision")),
                        release: integer(params.get("release")),
                    };
                    set_link(cx, name, link, &given, Refusals::Operation)
                }),
                ("unlink", unlink),
            ],
            schema_operations: vec![("listLinked", |_, cx, params| {
                let name = params
                    .get("name")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let link = spec(cx, "listLinked", name)?;
                let id = params.get("id").and_then(Value::as_str).unwrap_or_default();
                let request = page_request(NAME, "listLinked", params)?;
                let pin = link.pin;
                let current = match pin {
                    Some(pin) => latest(cx, pin, &link.schema, id)?,
                    None => None,
                };
                let stale = params.get("stale") == Some(&Value::Bool(true));
                let stale_below = match (stale, pin, current) {
                    (false, _, _) => None,
                    (true, Some(pin), Some(current)) => Some((pin, current)),
                    (true, _, _) => return Ok(json!({"items": [], "next": null})),
                };
                let table = cx.sql().table("links")?;
                let mut values = vec![
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from(name),
                    SqlValue::from(id),
                    SqlValue::Integer(request.after),
                ];
                if let Some((_, current)) = stale_below {
                    values.push(SqlValue::Integer(current));
                }
                values.push(SqlValue::Integer(request.limit + 1));
                let rows = cx.sql().all(
                    format!(
                        "SELECT {COLUMNS}, id FROM {table}
                         WHERE namespace = ? AND schema = ? AND name = ? AND target_id = ? AND row > ?{}
                         ORDER BY row LIMIT ?",
                        stale_below.map_or(String::new(), |(pin, _)| format!(
                            " AND {} < ?",
                            pin.field()
                        ))
                    ),
                    &values,
                )?;
                let numbered = rows
                    .into_iter()
                    .map(|row| Ok((row.i64("row")?, row)))
                    .collect::<Result<Vec<_>>>()?;
                let found = page(numbered, request.limit, |(row, _)| *row);
                let items = found
                    .items
                    .iter()
                    .map(|(_, row)| {
                        let record = link_of(row, current, pin)?;
                        let mut item = Map::new();
                        item.insert("id".into(), Value::from(row.text("id")?));
                        for member in ["revision", "release", "latest", "stale"] {
                            if let Some(value) = record.get(member) {
                                item.insert(member.into(), value.clone());
                            }
                        }
                        Ok(Value::Object(item))
                    })
                    .collect::<Result<Vec<_>>>()?;
                Ok(Page {
                    items,
                    next: found.next,
                }
                .to_json())
            })],
            fields: vec![("targets", targets)],
            guard_reference: Some(guard_reference),
            after_reference_change: Some(|_, cx, reference, change| {
                // A target's delete unlinks it, through unlink, as the
                // caller: the one target of a many-valued link.
                if matches!(change, InstanceChange::Delete) {
                    let many = cx
                        .config()
                        .get(&reference.key)
                        .is_some_and(|link| link.many);
                    let mut params = Map::new();
                    params.insert("name".into(), Value::from(reference.key.as_str()));
                    if many {
                        params.insert("id".into(), Value::from(reference.id.as_str()));
                    }
                    cx.instances().invoke(
                        cx.schema(),
                        cx.id(),
                        "unlink",
                        &Value::Object(params),
                        None,
                    )?;
                }
                Ok(())
            }),
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    let table = cx.sql().table("links")?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn config_change(
    _: &Links,
    before: Option<&LinksConfig>,
    after: Option<&LinksConfig>,
    change: &ConfigChange<'_>,
) -> Result<Option<String>> {
    let Some(after) = after else {
        return Ok(Some(
            "the links and references its instances hold would stay behind".to_owned(),
        ));
    };
    let Some(before) = before else {
        let required: Vec<&str> = after
            .links
            .iter()
            .filter(|(_, link)| link.required)
            .map(|(name, _)| name.as_str())
            .collect();
        return Ok((!required.is_empty()).then(|| {
            format!(
                "the instances that exist hold no link {}, which is required",
                required.join(", ")
            )
        }));
    };
    // No instance holds a link, or lacks one, that the new config refuses.
    if !change.instances()? {
        return Ok(None);
    }
    for (name, link) in &before.links {
        let Some(next) = after.get(name) else {
            return Ok(Some(format!(
                "link {name} is gone, and its instances may hold it"
            )));
        };
        if next.schema != link.schema {
            return Ok(Some(format!(
                "link {name} points at {}, not {}, in the instances that hold it",
                link.schema, next.schema
            )));
        }
        if next.required && !link.required {
            return Ok(Some(format!(
                "link {name} becomes required, and an instance the live version accepts may not hold it"
            )));
        }
        if next.many != link.many {
            return Ok(Some(format!(
                "link {name} holds {} in the instances that hold it, not {}",
                if link.many {
                    "a list of targets"
                } else {
                    "one target"
                },
                if next.many { "a list" } else { "one" }
            )));
        }
        if let (true, Some(max), Some(next_max)) = (link.many, link.max, next.max) {
            if next_max < max {
                return Ok(Some(format!(
                    "link {name} may hold {max} targets in the instances that hold it, more than {next_max}"
                )));
            }
        }
    }
    for (name, link) in &after.links {
        if link.required && before.get(name).is_none() {
            return Ok(Some(format!(
                "link {name} is new and required, and an instance the live version accepts holds none"
            )));
        }
    }
    Ok(None)
}

// initialize holds a create's links to the config: every name one the
// config gives, every required link among them, a list for each
// many-valued link, of at least one target when it is required, at most
// max and each target once; each set with link's checks.
fn initialize(_: &Links, cx: &InstanceCx<'_, LinksConfig>, params: &Value) -> Result<()> {
    let config = cx.config();
    let empty = Map::new();
    let given = params.as_object().unwrap_or(&empty);
    let mut issues = Vec::new();
    for name in js_keys(given) {
        let value = &given[name];
        let entry = format!("/behaviors/{NAME}/{name}");
        let Some(link) = config.get(name) else {
            issues.push(SchemaIssue::new(
                entry,
                format!(
                    "{} has no link {name} (its links: {})",
                    cx.schema(),
                    config.names().join(", ")
                ),
            ));
            continue;
        };
        match (link.many, value.as_array()) {
            (false, Some(_)) => issues.push(SchemaIssue::new(
                entry,
                format!("link {name} holds one target, so a create gives one, not a list"),
            )),
            (false, None) => {}
            (true, None) => issues.push(SchemaIssue::new(
                entry,
                format!("link {name} holds a list of targets, so a create gives a list"),
            )),
            (true, Some(items)) => {
                let max = link.max.unwrap_or(i64::MAX);
                if items.len() as i64 > max {
                    issues.push(SchemaIssue::new(
                        entry,
                        format!(
                            "link {name} holds at most {max} targets, and the create gives {}",
                            items.len()
                        ),
                    ));
                } else if link.required && items.is_empty() {
                    issues.push(SchemaIssue::new(
                        entry,
                        format!(
                            "link {name} is required, so a create of {} gives at least one target",
                            cx.schema()
                        ),
                    ));
                } else {
                    let mut seen: Vec<&str> = Vec::new();
                    for (index, item) in items.iter().enumerate() {
                        let id = target_of(item).id;
                        if seen.contains(&id) {
                            issues.push(SchemaIssue::new(
                                format!("{entry}/{index}"),
                                format!(
                                    "link {name} holds each target once, and the create gives {} {id} again",
                                    link.schema
                                ),
                            ));
                        }
                        seen.push(id);
                    }
                }
            }
        }
    }
    for (name, link) in &config.links {
        if link.required && !given.contains_key(name) {
            issues.push(SchemaIssue::new(
                format!("/behaviors/{NAME}"),
                format!(
                    "link {name} is required, so a create of {} gives it",
                    cx.schema()
                ),
            ));
        }
    }
    if !issues.is_empty() {
        return Err(EngineError::create_params(cx.schema(), issues).into());
    }
    for name in js_keys(given) {
        let value = &given[name];
        let Some(link) = config.get(name) else {
            continue;
        };
        match value.as_array() {
            Some(items) => {
                for (index, item) in items.iter().enumerate() {
                    let refuse = Refusals::Create {
                        name,
                        index: Some(index),
                        id_only: item.is_string(),
                    };
                    set_link(cx, name, link, &target_of(item), refuse)?;
                }
            }
            None => {
                let refuse = Refusals::Create {
                    name,
                    index: None,
                    id_only: value.is_string(),
                };
                set_link(cx, name, link, &target_of(value), refuse)?;
            }
        }
    }
    Ok(())
}

// unlink reads nothing of the target, which may be the instance whose
// delete is clearing its links. A many-valued link names the target it
// removes; a link that holds one may name it, which must be the one it
// holds.
fn unlink(
    _: &Links,
    cx: &superschematic_engine::OperationCx<'_, LinksConfig>,
    params: &Value,
) -> Result<Value> {
    let name = params
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let link = spec(cx, "unlink", name)?;
    let id = params.get("id").and_then(Value::as_str);
    let param = |at: &str, message: String| -> Error {
        EngineError::operation_params(NAME, "unlink", vec![SchemaIssue::new(at, message)]).into()
    };
    if link.many && id.is_none() {
        return Err(param(
            "/id",
            format!("link {name} holds a list of targets, so unlink names the one it removes"),
        ));
    }
    let rows = held_by(cx, name)?;
    if rows.is_empty() && id.is_none() {
        return Err(param(
            "/name",
            format!("{} {} has no link {name}", cx.schema(), cx.id()),
        ));
    }
    let veto = |reason: String, code: &str| {
        Error::veto(
            NAME,
            "unlink",
            cx.schema(),
            cx.id(),
            Veto {
                reason,
                code: Some(code.to_owned()),
                details: None,
            },
        )
    };
    let row = match id {
        None => rows.first(),
        Some(id) => {
            let mut found = None;
            for row in &rows {
                if row.text("target_id")? == id {
                    found = Some(row);
                    break;
                }
            }
            found
        }
    };
    let Some(row) = row else {
        return Err(veto(
            format!(
                "link {name} does not hold {} {}",
                link.schema,
                id.unwrap_or("undefined")
            ),
            "not_linked",
        ));
    };
    if link.required && rows.len() == 1 {
        return Err(veto(
            if link.many {
                format!(
                    "link {name} is required and {} {} is its last target: link another before unlinking it",
                    link.schema,
                    id.unwrap_or("undefined")
                )
            } else {
                format!("link {name} is required: it can be moved with link, not unlinked")
            },
            "required_link",
        ));
    }
    let table = cx.sql().table("links")?;
    cx.sql().run(
        format!("DELETE FROM {table} WHERE row = ?"),
        &[SqlValue::Integer(row.i64("row")?)],
    )?;
    let target_schema = row.text("target_schema")?;
    let target_id = row.text("target_id")?;
    cx.references().remove(target_schema, target_id, name)?;
    let mut out = Map::new();
    out.insert("name".into(), Value::from(name));
    out.insert("schema".into(), Value::from(target_schema));
    out.insert("id".into(), Value::from(target_id));
    if let Some(revision) = row.opt_i64("revision")? {
        out.insert("revision".into(), Value::from(revision));
    }
    if let Some(release) = row.opt_i64("release")? {
        out.insert("release".into(), Value::from(release));
    }
    Ok(Value::Object(out))
}

// targets is every link the instance holds, by name: a list for each
// many-valued link, each pinned link with its target's latest, read as the
// caller.
fn targets(_: &Links, view: &InstanceView<'_, LinksConfig>) -> Result<Option<Value>> {
    let rows = held(view)?;
    if rows.is_empty() {
        return Ok(None);
    }
    let config = view.config();
    let pin_of = |row: &Row| -> Result<Option<LinkPin>> {
        Ok(config.get(row.text("name")?).and_then(|link| link.pin))
    };
    let mut wanted = Vec::new();
    for row in &rows {
        if let Some(pin) = pin_of(row)? {
            if held_pin(row, Some(pin))?.is_some() {
                wanted.push((
                    row.text("target_schema")?.to_owned(),
                    row.text("target_id")?.to_owned(),
                    pin,
                ));
            }
        }
    }
    let current = latest_of(view, &wanted)?;
    let mut out = Map::new();
    for row in &rows {
        let name = row.text("name")?;
        let pin = pin_of(row)?;
        let latest = pin.and_then(|pin| {
            current
                .get(&(
                    row.text("target_schema").unwrap_or_default().to_owned(),
                    row.text("target_id").unwrap_or_default().to_owned(),
                    pin,
                ))
                .copied()
        });
        let record = Value::Object(link_of(row, latest, pin)?);
        if config.get(name).is_some_and(|link| link.many) {
            match out.get_mut(name) {
                Some(Value::Array(list)) => list.push(record),
                _ => {
                    out.insert(name.to_owned(), Value::Array(vec![record]));
                }
            }
        } else {
            out.insert(name.to_owned(), record);
        }
    }
    Ok(Some(Value::Object(out)))
}

// guard_reference refuses the delete of a target a required link points
// at, whoever the caller, and of a required many-valued link's last
// target. The reason names the schema, not the instance, which the caller
// may not be able to read.
fn guard_reference(
    _: &Links,
    view: &InstanceView<'_, LinksConfig>,
    reference: &Reference,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    if !matches!(request, GuardRequest::Delete { .. }) {
        return Ok(None);
    }
    let Some(link) = view.config().get(&reference.key) else {
        return Ok(None);
    };
    if !link.required || (link.many && held_by(view, &reference.key)?.len() > 1) {
        return Ok(None);
    }
    Ok(Some(Veto {
        reason: format!(
            "an instance of {} links to it through its required link {}{}",
            view.schema(),
            reference.key,
            if link.many {
                ", as its last target"
            } else {
                ""
            }
        ),
        code: Some("required_target".to_owned()),
        details: None,
    }))
}
