//! The layout document (D103): where the core's logical names of a
//! build-time project's rows live, as `internal/generator/behaviorlayout`
//! writes it and a generated ORM embeds it. Each table that composes
//! behaviors, by its type's name: the table, its key column and IR type,
//! the version column, a soft-deletable table's `deletedAt` column, its own
//! fields' columns, each use's owned columns, operations and sub-tables, the
//! queue its deadlines are armed in, the queue its change trigger writes
//! its writes to, and the type as the core runs it.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, PoisonError};

use serde::{Deserialize, Serialize};
#[cfg(any(feature = "postgres", feature = "sqlite", test))]
use superschematic_rules::Core;
use superschematic_rules::{Refusal, TypeSpec};

/// A database's layout document: each table that composes behaviors, by
/// its type's name.
///
/// It also keeps the define check of every type's spec, made once per core
/// a command runs on over the layout, as `rules.ts` keeps it per core and
/// layout: a type whose uses or configs that core refuses is refused with
/// that refusal on every command on it.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Layout {
    pub types: BTreeMap<String, Table>,
    // Read by the host loop, which a build with no store leaves out.
    #[serde(skip)]
    #[cfg_attr(
        not(any(feature = "postgres", feature = "sqlite", test)),
        allow(dead_code)
    )]
    checked: Checks,
}

/// How many cores' define checks a layout keeps; a check by one more
/// forgets them all, to be made again.
#[cfg_attr(
    not(any(feature = "postgres", feature = "sqlite", test)),
    allow(dead_code)
)]
const MAX_CHECKED_CORES: usize = 16;

/// The refusal of each type whose spec a core refused.
type Refusals = Arc<BTreeMap<String, Refusal>>;

/// A layout's define checks, each by the id of the core that made it
/// ([`Core::id`]). A clone of a layout makes its own.
#[derive(Default)]
struct Checks(Mutex<Vec<(u64, Refusals)>>);

impl Clone for Checks {
    fn clone(&self) -> Self {
        Checks::default()
    }
}

impl std::fmt::Debug for Checks {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let cores = self.0.lock().unwrap_or_else(PoisonError::into_inner).len();
        f.debug_struct("Checks").field("cores", &cores).finish()
    }
}

impl Layout {
    /// A layout of tables, each by its type's name.
    pub fn new(tables: impl IntoIterator<Item = Table>) -> Self {
        Layout {
            types: tables
                .into_iter()
                .map(|table| (table.type_name.clone(), table))
                .collect(),
            checked: Checks::default(),
        }
    }

    /// The document as the generator writes it.
    pub fn from_json(text: &str) -> Result<Layout, serde_json::Error> {
        serde_json::from_str(text)
    }

    /// A type's table.
    pub fn table(&self, type_name: &str) -> Option<&Table> {
        self.types.get(type_name)
    }

    /// The define check of each type by `core`, made once per core: the
    /// refusal of each type `check` refuses. A core is known by its id,
    /// which changes when it adds a behavior, so a core made where a
    /// dropped one was makes its own.
    #[cfg(any(feature = "postgres", feature = "sqlite", test))]
    pub(crate) fn checked(
        &self,
        core: &Core,
        check: impl FnOnce(&BTreeMap<String, Table>) -> BTreeMap<String, Refusal>,
    ) -> Refusals {
        let id = core.id();
        let mut checks = self
            .checked
            .0
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        if let Some((_, refusals)) = checks.iter().find(|(by, _)| *by == id) {
            return refusals.clone();
        }
        let refusals = Arc::new(check(&self.types));
        if checks.len() >= MAX_CHECKED_CORES {
            checks.clear();
        }
        checks.push((id, refusals.clone()));
        refusals
    }
}

/// The layout of one table that composes behaviors.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Table {
    /// The type's name, as the core names it.
    #[serde(rename = "type")]
    pub type_name: String,
    /// The table, unquoted.
    pub table: String,
    /// The primary key. The core's key of a row is the key's wire form (a
    /// UUID's base62 text), which the adapter converts by its type.
    pub key: Column,
    /// The version column, `_version`, which `@optimistic`'s trigger bumps
    /// on every update.
    pub version: String,
    /// The condition a soft-deletable table's live rows meet; `None` for a
    /// table without `deletedAt`. The adapter reads and writes a live row
    /// alone, so the core refuses a command on a row the repository holds
    /// deleted as `not_found`, and a soft delete another makes under a
    /// command moves the row.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub live: Option<Live>,
    /// The own fields the core reads and a create and an update write, in
    /// column order.
    #[serde(default)]
    pub own: Vec<Column>,
    /// The uses on the table, field-anchored and implemented, in the
    /// type's list order.
    #[serde(default)]
    pub uses: Vec<Use>,
    /// The queue the table's deadlines are armed in, one message per row,
    /// use and deadline; `None` for a table whose behaviors arm none.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub deadlines: Option<Deadlines>,
    /// The queue the table's change trigger writes a message of for each
    /// write its behaviors hear, whoever makes it (D103); `None` for a
    /// table nothing hears.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub changes: Option<Changes>,
    /// The type as the core runs it.
    pub spec: TypeSpec,
}

/// The deadline queue of a table whose behaviors arm deadlines (D103): a
/// queue of its database the loader adds (`<Type>Deadline`), whose live
/// message of a row, a use and a name is the deadline's arming, due when
/// it falls due. Each member is a column, unquoted, beside the queue's own
/// (`id`, `state`, `due_at`, `claim_token`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Deadlines {
    /// The queue's name in the IR (`TicketDeadline`).
    pub queue: String,
    /// The queue's table.
    pub table: String,
    /// The row's key, bound as the row table's key binds.
    pub row_key: String,
    /// The use's id.
    #[serde(rename = "use")]
    pub use_id: String,
    /// The deadline's name.
    pub name: String,
    /// The time it was armed to fall due at, in epoch milliseconds.
    pub at: String,
    /// The payload it was armed with, as JSON.
    pub payload: String,
}

/// The change queue of a table whose writes its behaviors hear (D103): a
/// queue of its database the loader adds (`<Type>Change`), keyed by the
/// row's key and serialized, to which the table's trigger writes a message
/// for each insert, update and delete of a row, whoever makes it, and
/// which a behavior worker runs through the core's change command. Each
/// member is a column, unquoted, beside the queue's own (`id`, `state`,
/// `claim_token`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Changes {
    /// The queue's name in the IR (`TicketChange`).
    pub queue: String,
    /// The queue's table.
    pub table: String,
    /// The row's key, as the row table's key column holds it.
    pub row_key: String,
    /// What the write did: `insert`, `update` or `delete`.
    pub write: String,
    /// When it was made, in epoch milliseconds, by the database's clock.
    pub at: String,
    /// The row's columns before the write, as JSON by column; NULL for an
    /// insert.
    pub before: String,
    /// The row's columns after the write, as JSON by column; NULL for a
    /// delete.
    pub after: String,
}

/// What a live row of a soft-deletable table holds: NULL in its
/// `deletedAt` column.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Live {
    /// The `deletedAt` column, unquoted.
    pub column: String,
}

/// A field of a table and the column that holds it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Column {
    /// The field's name in the IR: the member a generated row holds it
    /// under, and the name the core gives an own field.
    pub field: String,
    /// Its column, unquoted.
    pub column: String,
    /// Its IR type's name: `string`, `number`, `boolean` or a scalar
    /// (`Temporal.DateTime`, `Identity.UUID`).
    #[serde(rename = "type")]
    pub type_name: String,
    /// How the adapter converts it; `None` for none.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub convert: Option<Convert>,
}

impl Column {
    /// A column that holds the core's value as it is.
    pub fn new(
        field: impl Into<String>,
        column: impl Into<String>,
        type_name: impl Into<String>,
    ) -> Self {
        Column {
            field: field.into(),
            column: column.into(),
            type_name: type_name.into(),
            convert: None,
        }
    }

    /// The column converted as `convert` says.
    pub fn converted(mut self, convert: Convert) -> Self {
        self.convert = Some(convert);
        self
    }
}

/// How the adapter converts a value between the core's JSON and its column.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Convert {
    /// A timestamp the core holds as epoch milliseconds: bound as the text
    /// `Date.prototype.toISOString` writes, which a Postgres `TIMESTAMPTZ`
    /// parses and a SQLite `TEXT` holds, and read back from either.
    EpochMillis,
    /// A conversion this adapter does not know, from a newer generator: the
    /// value is bound and read as it is, as Go's and TypeScript's adapters
    /// read one, so the document still loads.
    #[serde(other)]
    Unknown,
}

/// A behavior's declared field and the column it takes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OwnedField {
    /// The declared name, as the core names it in a row's owned fields
    /// (`enteredAt`).
    pub name: String,
    #[serde(flatten)]
    pub column: Column,
    /// The use's value field, the anchor field itself.
    #[serde(default, skip_serializing_if = "is_false")]
    pub value: bool,
}

fn is_false(flag: &bool) -> bool {
    !*flag
}

/// One use of a behavior on a table: anchored on a field, or implemented
/// by the type (`implements Timers<Ticket, { ... }>`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Use {
    /// The use's id: its anchor field's name, or the behavior's for a use
    /// the type implements.
    pub id: String,
    pub behavior: String,
    /// The member a repository and an SDK give the use's operations: its
    /// field, or the behavior's storage key in camel case (`timers`).
    /// Empty in a document written before it was, which then reads as the
    /// use's id.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub member: String,
    /// The anchor field; `None` for a type anchor.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub anchor: Option<String>,
    #[serde(default)]
    pub fields: Vec<OwnedField>,
    #[serde(default)]
    pub operations: Vec<Operation>,
    /// The sub-tables the behavior keeps per row, in its declaration's
    /// order.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tables: Vec<SubTable>,
}

impl Use {
    /// The anchor field, when the use is anchored on one.
    pub fn anchor(&self) -> Option<&str> {
        self.anchor.as_deref().filter(|anchor| !anchor.is_empty())
    }

    /// The member its operations sit under: `member`, or the id when the
    /// document names none.
    pub fn member(&self) -> &str {
        if self.member.is_empty() {
            &self.id
        } else {
            &self.member
        }
    }

    /// One of its sub-tables, by its declared name.
    pub fn table(&self, name: &str) -> Option<&SubTable> {
        self.tables.iter().find(|table| table.name == name)
    }
}

/// A sub-table a use keeps per row (D103): a child table of the row, with
/// a foreign key to it, each of its rows keyed within the row by its key
/// field, which only the behavior's operations write.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SubTable {
    /// The declared name, as the core names the table (`deadlines`).
    pub name: String,
    /// The table, unquoted.
    pub table: String,
    /// The column holding the row's key, bound as the row table's key
    /// binds.
    pub row: String,
    /// The declared key field and its column.
    pub key: OwnedField,
    /// The other declared fields, in the declaration's order, the key left
    /// out.
    #[serde(default)]
    pub fields: Vec<OwnedField>,
}

impl SubTable {
    /// A declared field other than the key, by its name.
    pub fn field(&self, name: &str) -> Option<&OwnedField> {
        self.fields.iter().find(|field| field.name == name)
    }
}

/// An operation a behavior declares on a row.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Operation {
    pub name: String,
    #[serde(default)]
    pub writes: bool,
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// The document reads as behaviorlayout writes it, and writes back so.
    #[test]
    fn the_document_reads_as_the_generator_writes_it() {
        let document = json!({
            "types": {
                "Order": {
                    "type": "Order",
                    "table": "order",
                    "key": {"field": "id", "column": "id", "type": "Identity.UUID"},
                    "version": "_version",
                    "live": {"column": "deleted_at"},
                    "own": [{"field": "title", "column": "title", "type": "string"}],
                    "uses": [{
                        "id": "status",
                        "behavior": "Workflow",
                        "member": "status",
                        "anchor": "status",
                        "fields": [
                            {"name": "status", "field": "status", "column": "status", "type": "string", "value": true},
                            {"name": "enteredAt", "field": "statusEnteredAt", "column": "status_entered_at", "type": "Temporal.DateTime", "convert": "epochMillis"}
                        ],
                        "operations": [{"name": "transition", "writes": true}]
                    }],
                    "spec": {"name": "Order", "uses": [{"behavior": "Workflow", "config": {"states": ["draft"]}, "anchor": {"field": "status"}}]}
                }
            }
        });
        let layout = Layout::from_json(&document.to_string()).expect("it reads");
        let order = layout.table("Order").expect("Order");
        assert_eq!(
            order.live.as_ref().map(|live| live.column.as_str()),
            Some("deleted_at")
        );
        assert_eq!(order.uses[0].anchor(), Some("status"));
        assert!(order.uses[0].fields[0].value);
        assert_eq!(order.uses[0].fields[1].column.column, "status_entered_at");
        assert_eq!(
            order.uses[0].fields[1].column.convert,
            Some(Convert::EpochMillis)
        );
        assert_eq!(serde_json::to_value(&layout).expect("it writes"), document);
        assert!(Layout::new([order.clone()]).table("Order").is_some());
        assert_eq!(order.uses[0].member(), "status");
        assert!(order.deadlines.is_none() && order.uses[0].tables.is_empty());

        // A conversion this adapter does not know loads as none.
        let mut newer = document;
        newer["types"]["Order"]["uses"][0]["fields"][1]["convert"] = json!("epochMicros");
        let layout = Layout::from_json(&newer.to_string()).expect("it reads");
        assert_eq!(
            layout.table("Order").expect("Order").uses[0].fields[1]
                .column
                .convert,
            Some(Convert::Unknown)
        );
    }

    /// A table that implements Timers (D103): its use's member and
    /// sub-table, and the queue its deadlines are armed in, as the
    /// generator writes them.
    #[test]
    fn a_use_s_sub_tables_and_the_tables_deadline_queue_read_as_the_generator_writes_them() {
        let document = json!({
            "types": {
                "Ticket": {
                    "type": "Ticket",
                    "table": "ticket",
                    "key": {"field": "id", "column": "id", "type": "Identity.UUID"},
                    "version": "_version",
                    "own": [{"field": "title", "column": "title", "type": "string"}],
                    "uses": [{
                        "id": "Timers",
                        "behavior": "Timers",
                        "member": "timers",
                        "fields": [],
                        "operations": [
                            {"name": "fireTimer", "writes": true},
                            {"name": "resetTimer", "writes": true},
                            {"name": "snoozeTimer", "writes": true}
                        ],
                        "tables": [{
                            "name": "deadlines",
                            "table": "ticket_timers_deadlines",
                            "row": "ticket_id",
                            "key": {"name": "name", "field": "name", "column": "name", "type": "string"},
                            "fields": [
                                {"name": "baseAt", "field": "baseAt", "column": "base_at", "type": "Temporal.DateTime", "convert": "epochMillis"},
                                {"name": "armedFrom", "field": "armedFrom", "column": "armed_from", "type": "string"},
                                {"name": "dueAt", "field": "dueAt", "column": "due_at", "type": "Temporal.DateTime", "convert": "epochMillis"},
                                {"name": "fired", "field": "fired", "column": "fired", "type": "boolean"},
                                {"name": "firedAt", "field": "firedAt", "column": "fired_at", "type": "Temporal.DateTime", "convert": "epochMillis"},
                                {"name": "outcome", "field": "outcome", "column": "outcome", "type": "string"}
                            ]
                        }]
                    }],
                    "deadlines": {
                        "queue": "TicketDeadline",
                        "table": "ticket_deadline_queue",
                        "rowKey": "row_key",
                        "use": "use_id",
                        "name": "name",
                        "at": "at",
                        "payload": "payload"
                    },
                    "spec": {"name": "Ticket", "uses": [{"behavior": "Timers", "config": {"deadlines": {}}, "anchor": "type"}]}
                }
            }
        });
        let layout = Layout::from_json(&document.to_string()).expect("it reads");
        let ticket = layout.table("Ticket").expect("Ticket");
        let timers = &ticket.uses[0];
        assert_eq!((timers.member(), timers.anchor()), ("timers", None));
        let deadlines = timers.table("deadlines").expect("deadlines");
        assert_eq!(
            (deadlines.row.as_str(), deadlines.key.column.column.as_str()),
            ("ticket_id", "name")
        );
        assert_eq!(
            deadlines.field("dueAt").map(|field| field.column.convert),
            Some(Some(Convert::EpochMillis))
        );
        assert!(deadlines.field("name").is_none());
        let queue = ticket.deadlines.as_ref().expect("its queue");
        assert_eq!(
            (
                queue.table.as_str(),
                queue.row_key.as_str(),
                queue.use_id.as_str()
            ),
            ("ticket_deadline_queue", "row_key", "use_id")
        );
        assert_eq!(serde_json::to_value(&layout).expect("it writes"), document);
    }

    /// The document behaviorlayout writes for fixture-timers-db, as its
    /// test holds it, reads: each table's Timers sub-table and its queue
    /// (D103).
    #[test]
    fn the_generators_document_of_a_timers_database_reads() {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(
            "../../../../internal/generator/behaviorlayout/testdata/fixture-timers-db.layout.json",
        );
        let text = std::fs::read_to_string(&path)
            .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
        let layout = Layout::from_json(&text).expect("it reads");
        for (type_name, table) in [("Ticket", "ticket"), ("Invoice", "invoice")] {
            let row = layout.table(type_name).expect("the table");
            let timers = row
                .uses
                .iter()
                .find(|used| used.behavior == "Timers")
                .expect("its Timers");
            assert_eq!((timers.member(), timers.anchor()), ("timers", None));
            let deadlines = timers.table("deadlines").expect("its deadlines");
            assert_eq!(deadlines.table, format!("{table}_timers_deadlines"));
            assert_eq!(deadlines.row, format!("{table}_id"));
            assert_eq!(
                row.deadlines.as_ref().map(|queue| queue.table.as_str()),
                Some(format!("{table}_deadline_queue").as_str())
            );
        }
    }
}
