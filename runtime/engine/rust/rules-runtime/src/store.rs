//! The runtime store (D117): the handler's reads and writes of its rows,
//! sub-table rows, shared rows, deadlines, events and changes, each mapped
//! onto the type's own table and the tables the loader adds beside it, as
//! the layout document the compiler wrote at the last publish names them.
//!
//! Each request holds the database's one writer from its start, so no row
//! moves under it: a write is not fenced on the version it read, since a
//! table's own foreign keys may have written the row first in the same unit
//! (a delete's `CASCADE`, a `SET NULL`), and the handler's own cascade then
//! finds it written. A table with a version column has it moved on by one
//! at each update, as its trigger would.

use std::collections::BTreeMap;
use std::sync::Arc;

use serde_json::{Map, Value};
use superschematic_rules::{
    Code, Refusal, RowChange, RowImage, SharedRow, SharedRowChange, SubRow, SubRowChange, WriteKind,
};
use superschematic_rules_adapter::{SharedTable, SubTable};
use superschematic_rules_handler::{
    Armed, Declarations, DueDeadline, HeldSubRow, QueuedChange, QueuedEvent, Store, StoreAnswer,
    StoreRead, StoreWrite, StoredRow, Unit, Version,
};

use crate::conn::Conn;
use crate::db::{
    delete_rows, insert_row, json_param, placeholder, quote, select_rows, timestamp, update_rows,
    Db,
};
use crate::record::{self, Record, Served, Types, RECORD_TABLE};
use crate::values::{
    core_from_column, core_to_column, internal, key_column, key_of, own_from_column, own_to_column,
    Dialect,
};

/// Why a unit stopped: a fence that did not hold, or a refusal.
enum Stop {
    Moved,
    Refused(Refusal),
}

impl From<Refusal> for Stop {
    fn from(refusal: Refusal) -> Self {
        Stop::Refused(refusal)
    }
}

/// The runtime store over a database connection.
pub struct RuntimeStore {
    pub(crate) db: Conn,
    pub(crate) core: Arc<superschematic_rules::Core>,
    pub(crate) declarations: Arc<Declarations>,
    /// The types of the model the database was last read at.
    types: Arc<Types>,
    /// The change queue's messages the last read of it numbered: each
    /// number's queue table and message id.
    queued: BTreeMap<i64, (String, Value)>,
    next_seq: i64,
}

/// The record's row and the runner's state of its service, as a request's
/// transaction reads them.
struct State {
    service: String,
    recorded: String,
    model_hash: Option<String>,
    plan_hash: Option<String>,
}

impl RuntimeStore {
    pub(crate) fn new(
        mut db: Conn,
        core: Arc<superschematic_rules::Core>,
        declarations: Arc<Declarations>,
    ) -> Result<Self, Refusal> {
        let created = match db.dialect() {
            Dialect::Sqlite => format!(
                "CREATE TABLE IF NOT EXISTS {RECORD_TABLE} (
                   service TEXT PRIMARY KEY, model_hash TEXT NOT NULL, documents TEXT NOT NULL,
                   layout TEXT NOT NULL, versions TEXT NOT NULL,
                   published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')))"
            ),
            Dialect::Postgres => format!(
                "CREATE TABLE IF NOT EXISTS {RECORD_TABLE} (
                   service TEXT PRIMARY KEY, model_hash TEXT NOT NULL, documents TEXT NOT NULL,
                   layout TEXT NOT NULL, versions TEXT NOT NULL,
                   published_at TIMESTAMPTZ NOT NULL DEFAULT now())"
            ),
        };
        db.batch(&created)?;
        Ok(RuntimeStore {
            db,
            core,
            declarations,
            types: Arc::new(Types::default()),
            queued: BTreeMap::new(),
            next_seq: 0,
        })
    }

    pub(crate) fn dialect(&self) -> Dialect {
        self.db.dialect()
    }

    /// The record of the database's last publish, read in the caller's
    /// transaction; `None` before its first.
    pub(crate) fn read_record(&mut self) -> Result<Option<Record>, Refusal> {
        let rows = self.db.query(
            &format!(
                "SELECT service, model_hash, documents, layout, versions FROM {RECORD_TABLE} ORDER BY service"
            ),
            &[],
        )?;
        if rows.len() > 1 {
            return Err(internal(format!(
                "the database records {} services in {RECORD_TABLE}; a runtime database holds one",
                rows.len()
            )));
        }
        let Some(row) = rows.into_iter().next() else {
            return Ok(None);
        };
        let text = |name: &str| -> Result<String, Refusal> {
            row.get(name)
                .and_then(Value::as_str)
                .map(str::to_owned)
                .ok_or_else(|| internal(format!("{RECORD_TABLE}.{name} holds no text")))
        };
        let parse = |name: &str| -> Result<Value, Refusal> {
            serde_json::from_str(&text(name)?)
                .map_err(|error| internal(format!("{RECORD_TABLE}.{name} holds no JSON: {error}")))
        };
        let documents = match parse("documents")? {
            Value::Array(documents) => documents,
            _ => return Err(internal(format!("{RECORD_TABLE}.documents is no list"))),
        };
        Ok(Some(Record {
            service: text("service")?,
            model_hash: text("model_hash")?,
            documents,
            layout: serde_json::from_value(parse("layout")?).map_err(|error| {
                internal(format!("{RECORD_TABLE}.layout is no layout: {error}"))
            })?,
            versions: serde_json::from_value(parse("versions")?)
                .map_err(|error| internal(format!("{RECORD_TABLE}.versions is no map: {error}")))?,
        }))
    }

    /// Writes the record of a publish, replacing the one before it, in the
    /// caller's transaction.
    pub(crate) fn write_record(&mut self, record: &Record) -> Result<(), Refusal> {
        let params = [
            Value::String(record.service.clone()),
            Value::String(record.model_hash.clone()),
            Value::String(json_text(&record.documents)?),
            Value::String(json_text(&record.layout)?),
            Value::String(json_text(&record.versions)?),
        ];
        self.db
            .execute(&format!("DELETE FROM {RECORD_TABLE}"), &[])?;
        let p = |n| placeholder(self.dialect(), n);
        let sql = format!(
            "INSERT INTO {RECORD_TABLE} (service, model_hash, documents, layout, versions) VALUES ({}, {}, {}, {}, {})",
            p(1),
            p(2),
            p(3),
            p(4),
            p(5)
        );
        self.db.execute(&sql, &params)?;
        Ok(())
    }

    /// The record's service, its model, and the runner's state of it.
    fn state(&mut self) -> Result<Option<State>, Refusal> {
        let rows = self.db.query(
            &format!("SELECT service, model_hash FROM {RECORD_TABLE} ORDER BY service"),
            &[],
        )?;
        let Some(row) = rows.into_iter().next() else {
            return Ok(None);
        };
        let text = |row: &Map<String, Value>, name: &str| {
            row.get(name).and_then(Value::as_str).map(str::to_owned)
        };
        let service = text(&row, "service").unwrap_or_default();
        let recorded = text(&row, "model_hash").unwrap_or_default();
        let mut state = State {
            service,
            recorded,
            model_hash: None,
            plan_hash: None,
        };
        if self.db.has_table("superschematic_schema_state")? {
            let rows = self.db.query(
                &format!(
                    "SELECT model_hash, plan_hash FROM superschematic_schema_state WHERE service = {}",
                    placeholder(self.dialect(), 1)
                ),
                &[Value::String(state.service.clone())],
            )?;
            if let Some(row) = rows.into_iter().next() {
                state.model_hash = text(&row, "model_hash");
                state.plan_hash = text(&row, "plan_hash").filter(|hash| !hash.is_empty());
            }
        }
        Ok(Some(state))
    }

    /// Reads the types again when the model the database is at has moved
    /// since they were read: what makes every request follow a publish,
    /// whoever made it, with no restart. A plan in progress, or a model the
    /// record does not hold, serves no request.
    fn follow(&mut self) -> Result<(), Refusal> {
        let Some(state) = self.state()? else {
            if !self.types.model_hash.is_empty() || !self.types.types.is_empty() {
                self.types = Arc::new(Types::default());
            }
            return Ok(());
        };
        if let Some(plan) = &state.plan_hash {
            return Err(Refusal::new(
                Code::Busy,
                format!(
                    "{} is being migrated: plan {plan} is in progress, and a request waits until a publish finishes it",
                    state.service
                ),
            ));
        }
        let at = state.model_hash.clone().unwrap_or_default();
        if at != state.recorded {
            return Err(Refusal::new(
                Code::Busy,
                format!(
                    "{} is at model {at}, and its runtime record holds the layout of model {}: a publish is recording its layout, or the database was migrated outside runtime mode; publish its schemas again",
                    state.service, state.recorded
                ),
            ));
        }
        if self.types.model_hash == at && !at.is_empty() {
            return Ok(());
        }
        let record = self
            .read_record()?
            .ok_or_else(|| internal("the runtime record went while it was read"))?;
        self.types = Arc::new(record::serve(&record, &self.core, &self.declarations)?);
        Ok(())
    }

    /// Serves the types of a record from now, as a publish that recorded it
    /// does in its own process.
    pub(crate) fn serve_record(&mut self, record: &Record) -> Result<(), Refusal> {
        self.types = Arc::new(record::serve(record, &self.core, &self.declarations)?);
        Ok(())
    }

    // A row's id as its key column binds it.
    fn key(served: &Served, id: &str) -> Result<Value, Refusal> {
        key_column(&served.model.key.kind, id, &served.table.type_name)
    }

    fn stored_row(&self, served: &Served, row: &Map<String, Value>) -> Result<StoredRow, Refusal> {
        let dialect = self.dialect();
        let table = &served.table;
        let what = |column: &str| format!("{}.{column}", table.type_name);
        let key = row
            .get(&table.key.column)
            .ok_or_else(|| internal(format!("{} read no key column", table.table)))?;
        let id = key_of(&served.model.key.kind, key, &what(&table.key.column))?;
        let version = if table.version.is_empty() {
            1
        } else {
            row.get(&table.version).and_then(Value::as_i64).unwrap_or(1)
        };
        let mut data = Map::new();
        for (field, column) in &served.own {
            let held = row.get(&column.column).unwrap_or(&Value::Null);
            if let Some(value) = own_from_column(field, held, dialect, &what(&column.column))? {
                data.insert(field.name.clone(), value);
            }
        }
        let mut owned: BTreeMap<String, Map<String, Value>> = BTreeMap::new();
        for used in &table.uses {
            let mut values = Map::new();
            for field in &used.fields {
                let held = row.get(&field.column.column).unwrap_or(&Value::Null);
                if let Some(value) =
                    core_from_column(&field.column, held, dialect, &what(&field.column.column))?
                {
                    values.insert(field.name.clone(), value);
                }
            }
            if !values.is_empty() {
                owned.insert(used.id.clone(), values);
            }
        }
        Ok(StoredRow {
            id,
            version,
            data,
            owned,
        })
    }

    fn row(&mut self, served: &Served, id: &str) -> Result<Option<StoredRow>, Refusal> {
        let key = Self::key(served, id)?;
        let rows = select_rows(
            &mut self.db,
            &served.table.table,
            &[(&served.table.key.column, key)],
        )?;
        rows.first()
            .map(|row| self.stored_row(served, row))
            .transpose()
    }

    fn rows(&mut self, served: &Served) -> Result<Vec<StoredRow>, Refusal> {
        let rows = select_rows(&mut self.db, &served.table.table, &[])?;
        let mut out = rows
            .iter()
            .map(|row| self.stored_row(served, row))
            .collect::<Result<Vec<_>, _>>()?;
        out.sort_by(|a, b| a.id.cmp(&b.id));
        Ok(out)
    }

    /// The columns a whole row's write sets: each own field the model holds
    /// and each owned field the layout lays out, a field with no value
    /// NULL.
    fn row_columns(
        &self,
        served: &Served,
        data: &Map<String, Value>,
        owned: &BTreeMap<String, Map<String, Value>>,
        whole: bool,
    ) -> Result<Map<String, Value>, Refusal> {
        let dialect = self.dialect();
        let table = &served.table;
        let mut columns = Map::new();
        for (field, column) in &served.own {
            let value = data.get(&field.name).unwrap_or(&Value::Null);
            if value.is_null() && !whole {
                continue;
            }
            let what = format!("{}.{}", table.type_name, field.name);
            columns.insert(
                column.column.clone(),
                own_to_column(field, value, dialect, &what)?,
            );
        }
        for used in &table.uses {
            for field in &used.fields {
                let value = owned
                    .get(&used.id)
                    .and_then(|fields| fields.get(&field.name))
                    .unwrap_or(&Value::Null);
                if value.is_null() && !whole {
                    continue;
                }
                let what = format!("{}.{}", table.type_name, field.column.field);
                columns.insert(
                    field.column.column.clone(),
                    core_to_column(&field.column, value, dialect, &what)?,
                );
            }
        }
        // An owned field the layout gives no column is one the core
        // computes at a read (Lease's active), which no row holds.
        Ok(columns)
    }

    fn sub_table<'s>(
        served: &'s Served,
        use_id: &str,
        name: &str,
    ) -> Result<&'s SubTable, Refusal> {
        served
            .table
            .uses
            .iter()
            .find(|used| used.id == use_id)
            .and_then(|used| used.table(name))
            .ok_or_else(|| {
                internal(format!(
                    "{}'s use {use_id} has no sub-table {name} in its layout",
                    served.table.type_name
                ))
            })
    }

    fn shared_table<'s>(
        served: &'s Served,
        use_id: &str,
        name: &str,
    ) -> Result<&'s SharedTable, Refusal> {
        served
            .table
            .uses
            .iter()
            .find(|used| used.id == use_id)
            .and_then(|used| used.shared.iter().find(|shared| shared.name == name))
            .ok_or_else(|| {
                internal(format!(
                    "{}'s use {use_id} has no shared table {name} in its layout",
                    served.table.type_name
                ))
            })
    }

    fn sub_row(&self, sub: &SubTable, row: &Map<String, Value>) -> Result<SubRow, Refusal> {
        let dialect = self.dialect();
        let what = |column: &str| format!("{}.{column}", sub.table);
        let key = core_from_column(
            &sub.key.column,
            row.get(&sub.key.column.column).unwrap_or(&Value::Null),
            dialect,
            &what(&sub.key.column.column),
        )?
        .map(|key| match key {
            Value::String(text) => text,
            other => other.to_string(),
        })
        .unwrap_or_default();
        let mut values = Map::new();
        for field in &sub.fields {
            let held = row.get(&field.column.column).unwrap_or(&Value::Null);
            if let Some(value) =
                core_from_column(&field.column, held, dialect, &what(&field.column.column))?
            {
                values.insert(field.name.clone(), value);
            }
        }
        Ok(SubRow::new(key, values))
    }

    fn sub_rows(
        &mut self,
        served: &Served,
        id: Option<&str>,
        use_id: &str,
        name: &str,
    ) -> Result<Vec<HeldSubRow>, Refusal> {
        let sub = Self::sub_table(served, use_id, name)?.clone();
        let conds = match id {
            Some(id) => vec![(sub.row.as_str(), Self::key(served, id)?)],
            None => Vec::new(),
        };
        let rows = select_rows(&mut self.db, &sub.table, &conds)?;
        let mut out = Vec::with_capacity(rows.len());
        for row in &rows {
            let held = row.get(&sub.row).unwrap_or(&Value::Null);
            let id = key_of(
                &served.model.key.kind,
                held,
                &format!("{}.{}", sub.table, sub.row),
            )?;
            out.push(HeldSubRow {
                id,
                row: self.sub_row(&sub, row)?,
            });
        }
        out.sort_by(|a, b| (&a.id, &a.row.key).cmp(&(&b.id, &b.row.key)));
        Ok(out)
    }

    fn shared_row(
        &mut self,
        served: &Served,
        use_id: &str,
        name: &str,
        key: &str,
    ) -> Result<Option<SharedRow>, Refusal> {
        let shared = Self::shared_table(served, use_id, name)?.clone();
        let rows = select_rows(
            &mut self.db,
            &shared.table,
            &[(&shared.key.column.column, Value::String(key.to_owned()))],
        )?;
        let Some(row) = rows.first() else {
            return Ok(None);
        };
        let dialect = self.dialect();
        let mut values = Map::new();
        for field in &shared.fields {
            let held = row.get(&field.column.column).unwrap_or(&Value::Null);
            let what = format!("{}.{}", shared.table, field.column.column);
            if let Some(value) = core_from_column(&field.column, held, dialect, &what)? {
                values.insert(field.name.clone(), value);
            }
        }
        let version = row
            .get(&shared.version)
            .and_then(Value::as_i64)
            .unwrap_or(1);
        Ok(Some(SharedRow::new(key, values, version)))
    }

    fn armed(
        &mut self,
        served: &Served,
        id: &str,
        use_id: &str,
        name: &str,
    ) -> Result<Option<Armed>, Refusal> {
        let Some(queue) = &served.table.deadlines else {
            return Ok(None);
        };
        let dialect = self.dialect();
        let p = |n| placeholder(dialect, n);
        let rows = self.db.query(
            &format!(
                "SELECT {at} AS at, {payload} AS payload FROM {table} WHERE {row} = {} AND {use_} = {} AND {name_} = {} AND state IN ('ready', 'claimed')",
                p(1),
                p(2),
                p(3),
                at = quote(&queue.at),
                payload = quote(&queue.payload),
                table = quote(&queue.table),
                row = quote(&queue.row_key),
                use_ = quote(&queue.use_id),
                name_ = quote(&queue.name),
            ),
            &[
                Self::key(served, id)?,
                Value::String(use_id.to_owned()),
                Value::String(name.to_owned()),
            ],
        )?;
        let Some(row) = rows.first() else {
            return Ok(None);
        };
        Ok(Some(Armed {
            at: row.get("at").and_then(Value::as_i64).unwrap_or_default(),
            payload: json_value(dialect, row.get("payload")),
        }))
    }

    fn live(&self, types: &Types) -> Vec<Version> {
        types
            .types
            .iter()
            .map(|(type_name, served)| Version {
                type_name: type_name.clone(),
                version: served.version,
                document: served.document.clone(),
            })
            .collect()
    }

    // drop_row clears a row's deadlines and its sub-table rows.
    fn drop_row(&mut self, served: &Served, id: &str) -> Result<(), Refusal> {
        let key = Self::key(served, id)?;
        for used in &served.table.uses {
            for sub in &used.tables {
                delete_rows(&mut self.db, &sub.table, &[(&sub.row, key.clone())])?;
            }
        }
        if let Some(queue) = &served.table.deadlines {
            delete_rows(&mut self.db, &queue.table, &[(&queue.row_key, key)])?;
        }
        Ok(())
    }

    fn one(changed: u64) -> Result<(), Stop> {
        if changed == 1 {
            Ok(())
        } else {
            Err(Stop::Moved)
        }
    }

    #[allow(clippy::too_many_lines)]
    fn apply_one(&mut self, types: &Types, write: &StoreWrite) -> Result<(), Stop> {
        let dialect = self.dialect();
        match write {
            StoreWrite::PutDraft { type_name, .. } | StoreWrite::PutVersion { type_name, .. } => {
                return Err(Stop::Refused(Refusal::invalid_argument(format!(
                    "type {type_name}: runtime mode defines no type on its own; a publish takes the database's schema documents whole and migrates its tables (D117)"
                ))))
            }
            StoreWrite::Rewrite { type_name, .. } => {
                return Err(Stop::Refused(internal(format!(
                    "type {type_name}: runtime mode rewrites no row; a publish is a migration"
                ))))
            }
            StoreWrite::DropUse { type_name, .. } | StoreWrite::ClearUse { type_name, .. } => {
                return Err(Stop::Refused(internal(format!(
                    "type {type_name}: runtime mode drops no use's rows; a publish's plan drops its tables"
                ))))
            }
            StoreWrite::Insert {
                type_name,
                id,
                data,
                owned,
            } => {
                let served = types.get(type_name)?;
                if self.row(served, id)?.is_some() {
                    return Err(Stop::Refused(Refusal::new(
                        Code::Conflict,
                        format!("{type_name} {id} already exists"),
                    )));
                }
                let mut columns = Map::new();
                columns.insert(served.table.key.column.clone(), Self::key(served, id)?);
                columns.extend(self.row_columns(served, data, owned, false)?);
                insert_row(&mut self.db, &served.table.table, &columns)?;
            }
            StoreWrite::Update {
                type_name,
                id,
                data,
                owned,
                ..
            } => {
                let served = types.get(type_name)?;
                let set = self.row_columns(served, data, owned, true)?;
                let bump = (!served.table.version.is_empty()).then_some(served.table.version.as_str());
                let changed = update_rows(
                    &mut self.db,
                    &served.table.table,
                    &set,
                    bump,
                    &[(&served.table.key.column, Self::key(served, id)?)],
                )?;
                if changed != 1 && self.row(served, id)?.is_none() {
                    return Err(Stop::Moved);
                }
            }
            StoreWrite::Delete { type_name, id, .. } => {
                let served = types.get(type_name)?;
                self.drop_row(served, id)?;
                delete_rows(
                    &mut self.db,
                    &served.table.table,
                    &[(&served.table.key.column, Self::key(served, id)?)],
                )?;
            }
            StoreWrite::Gone { type_name, id } => {
                if let Ok(served) = types.get(type_name) {
                    if self.row(served, id)?.is_none() {
                        self.drop_row(served, id)?;
                    }
                }
            }
            StoreWrite::SubRow {
                type_name,
                id,
                use_id,
                table,
                key,
                change,
                ..
            } => {
                let served = types.get(type_name)?;
                let sub = Self::sub_table(served, use_id, table)?.clone();
                let row_key = Self::key(served, id)?;
                let key_value = Value::String(key.clone());
                let at = [
                    (sub.row.as_str(), row_key.clone()),
                    (sub.key.column.column.as_str(), key_value.clone()),
                ];
                let values_of = |values: &Map<String, Value>, whole: bool| {
                    let mut columns = Map::new();
                    for (name, value) in values {
                        let Some(field) = sub.field(name) else {
                            if value.is_null() {
                                continue;
                            }
                            return Err(internal(format!(
                                "{}'s sub-table {} has no field {name} in its layout",
                                type_name, sub.name
                            )));
                        };
                        if value.is_null() && !whole {
                            continue;
                        }
                        let what = format!("{}.{}", sub.table, field.column.column);
                        columns.insert(
                            field.column.column.clone(),
                            core_to_column(&field.column, value, dialect, &what)?,
                        );
                    }
                    Ok::<_, Refusal>(columns)
                };
                match change {
                    SubRowChange::Insert { values } => {
                        if !select_rows(&mut self.db, &sub.table, &at)?.is_empty() {
                            return Err(Stop::Moved);
                        }
                        let mut columns = Map::new();
                        columns.insert(sub.row.clone(), row_key);
                        columns.insert(sub.key.column.column.clone(), key_value);
                        columns.extend(values_of(values, false)?);
                        insert_row(&mut self.db, &sub.table, &columns)?;
                    }
                    SubRowChange::Update { values } => {
                        let set = values_of(values, true)?;
                        if set.is_empty() {
                            if select_rows(&mut self.db, &sub.table, &at)?.is_empty() {
                                return Err(Stop::Moved);
                            }
                        } else {
                            Self::one(update_rows(&mut self.db, &sub.table, &set, None, &at)?)?;
                        }
                    }
                    SubRowChange::Delete => {
                        delete_rows(&mut self.db, &sub.table, &at)?;
                    }
                    other => {
                        return Err(Stop::Refused(internal(format!(
                            "the runtime store takes no sub-table change of this kind: {other:?}"
                        ))))
                    }
                }
            }
            StoreWrite::SharedRow {
                type_name,
                use_id,
                table,
                key,
                change,
            } => {
                let served = types.get(type_name)?;
                let shared = Self::shared_table(served, use_id, table)?.clone();
                let at = [(shared.key.column.column.as_str(), Value::String(key.clone()))];
                let values_of = |values: &Map<String, Value>, whole: bool| {
                    let mut columns = Map::new();
                    for (name, value) in values {
                        let Some(field) = shared.fields.iter().find(|field| field.name == *name)
                        else {
                            if value.is_null() {
                                continue;
                            }
                            return Err(internal(format!(
                                "{}'s shared table {} has no field {name} in its layout",
                                type_name, shared.name
                            )));
                        };
                        if value.is_null() && !whole {
                            continue;
                        }
                        let what = format!("{}.{}", shared.table, field.column.column);
                        columns.insert(
                            field.column.column.clone(),
                            core_to_column(&field.column, value, dialect, &what)?,
                        );
                    }
                    Ok::<_, Refusal>(columns)
                };
                match change {
                    SharedRowChange::Insert { values } => {
                        if !select_rows(&mut self.db, &shared.table, &at)?.is_empty() {
                            return Err(Stop::Moved);
                        }
                        let mut columns = Map::new();
                        columns.insert(shared.key.column.column.clone(), Value::String(key.clone()));
                        columns.extend(values_of(values, false)?);
                        columns.insert(shared.version.clone(), Value::from(1));
                        insert_row(&mut self.db, &shared.table, &columns)?;
                    }
                    SharedRowChange::Update {
                        expected_version,
                        values,
                    } => {
                        let set = values_of(values, true)?;
                        let mut held = at.to_vec();
                        held.push((shared.version.as_str(), Value::from(*expected_version)));
                        Self::one(update_rows(
                            &mut self.db,
                            &shared.table,
                            &set,
                            Some(&shared.version),
                            &held,
                        )?)?;
                    }
                    other => {
                        return Err(Stop::Refused(internal(format!(
                            "the runtime store takes no shared-table change of this kind: {other:?}"
                        ))))
                    }
                }
            }
            StoreWrite::Arm {
                type_name,
                id,
                use_id,
                name,
                at,
                payload,
            } => {
                let served = types.get(type_name)?;
                let queue = served.table.deadlines.clone().ok_or_else(|| {
                    internal(format!(
                        "{type_name}'s use {use_id} arms the deadline {name}, and its layout gives it no deadline queue"
                    ))
                })?;
                let key = Self::key(served, id)?;
                delete_rows(
                    &mut self.db,
                    &queue.table,
                    &[
                        (&queue.row_key, key.clone()),
                        (&queue.use_id, Value::String(use_id.clone())),
                        (&queue.name, Value::String(name.clone())),
                    ],
                )?;
                let payload = match payload {
                    Value::Null => Value::Null,
                    payload => Value::String(payload.to_string()),
                };
                let p = |n| placeholder(dialect, n);
                self.db.execute(
                    &format!(
                        "INSERT INTO {} ({}, {}, {}, {}, {}, \"due_at\") VALUES ({}, {}, {}, {}, {}, {})",
                        quote(&queue.table),
                        quote(&queue.row_key),
                        quote(&queue.use_id),
                        quote(&queue.name),
                        quote(&queue.at),
                        quote(&queue.payload),
                        p(1),
                        p(2),
                        p(3),
                        p(4),
                        json_param(dialect, 5),
                        timestamp(dialect, 6),
                    ),
                    &[
                        key,
                        Value::String(use_id.clone()),
                        Value::String(name.clone()),
                        Value::from(*at),
                        payload,
                        Value::from(*at),
                    ],
                )?;
            }
            StoreWrite::Clear {
                type_name,
                id,
                use_id,
                name,
            } => {
                let served = types.get(type_name)?;
                if let Some(queue) = served.table.deadlines.clone() {
                    delete_rows(
                        &mut self.db,
                        &queue.table,
                        &[
                            (&queue.row_key, Self::key(served, id)?),
                            (&queue.use_id, Value::String(use_id.clone())),
                            (&queue.name, Value::String(name.clone())),
                        ],
                    )?;
                }
            }
            StoreWrite::Spend {
                type_name,
                id,
                use_id,
                name,
                at,
            } => {
                let served = types.get(type_name)?;
                let Some(queue) = served.table.deadlines.clone() else {
                    return Err(Stop::Moved);
                };
                let p = |n| placeholder(dialect, n);
                Self::one(self.db.execute(
                    &format!(
                        "DELETE FROM {} WHERE {} = {} AND {} = {} AND {} = {} AND {} = {} AND state IN ('ready', 'claimed')",
                        quote(&queue.table),
                        quote(&queue.row_key),
                        p(1),
                        quote(&queue.use_id),
                        p(2),
                        quote(&queue.name),
                        p(3),
                        quote(&queue.at),
                        p(4)
                    ),
                    &[
                        Self::key(served, id)?,
                        Value::String(use_id.clone()),
                        Value::String(name.clone()),
                        Value::from(*at),
                    ],
                )?)?;
            }
            StoreWrite::Release {
                type_name,
                id,
                use_id,
                name,
                due,
            } => {
                let served = types.get(type_name)?;
                let Some(queue) = served.table.deadlines.clone() else {
                    return Err(Stop::Moved);
                };
                let p = |n| placeholder(dialect, n);
                Self::one(self.db.execute(
                    &format!(
                        "UPDATE {} SET \"due_at\" = {} WHERE {} = {} AND {} = {} AND {} = {} AND state IN ('ready', 'claimed')",
                        quote(&queue.table),
                        timestamp(dialect, 1),
                        quote(&queue.row_key),
                        p(2),
                        quote(&queue.use_id),
                        p(3),
                        quote(&queue.name),
                        p(4)
                    ),
                    &[
                        Value::from(*due),
                        Self::key(served, id)?,
                        Value::String(use_id.clone()),
                        Value::String(name.clone()),
                    ],
                )?)?;
            }
            StoreWrite::Event {
                type_name,
                id,
                use_id,
                kind,
                at,
                payload,
            } => {
                let served = types.get(type_name)?;
                let queue = served.table.events.clone().ok_or_else(|| {
                    internal(format!(
                        "{type_name}'s use {use_id} emits the event {kind}, and its layout gives it no event queue"
                    ))
                })?;
                let p = |n| placeholder(dialect, n);
                let payload = match payload {
                    Value::Null => Value::Null,
                    payload => Value::String(payload.to_string()),
                };
                self.db.execute(
                    &format!(
                        "INSERT INTO {} ({}, {}, {}, {}, {}) VALUES ({}, {}, {}, {}, {})",
                        quote(&queue.table),
                        quote(&queue.row_key),
                        quote(&queue.use_id),
                        quote(&queue.kind),
                        quote(&queue.at),
                        quote(&queue.payload),
                        p(1),
                        p(2),
                        p(3),
                        p(4),
                        json_param(dialect, 5)
                    ),
                    &[
                        Self::key(served, id)?,
                        Value::String(use_id.clone()),
                        Value::String(kind.clone()),
                        Value::from(*at),
                        payload,
                    ],
                )?;
            }
            StoreWrite::Change { type_name, .. } => {
                // The table's change trigger, which the plan wrote, has
                // written the change to its queue with the write itself.
                let served = types.get(type_name)?;
                if served.table.changes.is_none() {
                    return Err(Stop::Refused(internal(format!(
                        "the behaviors hear {type_name}'s writes, and its layout gives it no change queue"
                    ))));
                }
            }
            StoreWrite::Dequeue { seq } => {
                let Some((table, id)) = self.queued.remove(seq) else {
                    return Err(Stop::Moved);
                };
                Self::one(delete_rows(&mut self.db, &table, &[("id", id)])?)?;
            }
        }
        Ok(())
    }
}

/// A JSON column's value as the dialect read it: SQLite's text parsed, a
/// NULL null.
fn json_value(dialect: Dialect, value: Option<&Value>) -> Value {
    match (dialect, value) {
        (_, None | Some(Value::Null)) => Value::Null,
        (Dialect::Sqlite, Some(Value::String(text))) => {
            serde_json::from_str(text).unwrap_or_else(|_| Value::String(text.clone()))
        }
        (_, Some(value)) => value.clone(),
    }
}

/// A change queue's image of a row, by column, as the handler's own and
/// owned fields; `None` for none.
fn image_of(
    served: &Served,
    dialect: Dialect,
    value: Option<&Value>,
) -> Result<Option<RowImage>, Refusal> {
    let columns = match json_value(dialect, value) {
        Value::Null => return Ok(None),
        Value::Object(columns) => columns,
        other => {
            return Err(internal(format!(
                "{}'s change holds an image that is no object: {other}",
                served.table.type_name
            )))
        }
    };
    let mut image = RowImage::default();
    for (field, column) in &served.own {
        let held = columns.get(&column.column).unwrap_or(&Value::Null);
        let what = format!("{}.{}", served.table.type_name, field.name);
        if let Some(value) = own_from_column(field, held, dialect, &what)? {
            image.own.insert(field.name.clone(), value);
        }
    }
    for used in &served.table.uses {
        let mut values = Map::new();
        for field in &used.fields {
            let held = columns.get(&field.column.column).unwrap_or(&Value::Null);
            let what = format!("{}.{}", served.table.type_name, field.column.column);
            if let Some(value) = core_from_column(&field.column, held, dialect, &what)? {
                values.insert(field.name.clone(), value);
            }
        }
        if !values.is_empty() {
            image.owned.insert(used.id.clone(), values);
        }
    }
    Ok(Some(image))
}

/// A queue's time column, epoch milliseconds.
fn millis(value: Option<&Value>) -> i64 {
    value
        .and_then(superschematic_rules_handler::values::instant_of)
        .unwrap_or_default()
}

impl Store for RuntimeStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        let types = self.types.clone();
        let dialect = self.dialect();
        let mut out: Vec<(i64, DueDeadline)> = Vec::new();
        for (type_name, served) in &types.types {
            let Some(queue) = &served.table.deadlines else {
                continue;
            };
            let rows = self.db.query(
                &format!(
                    "SELECT {} AS row_key, {} AS use_id, {} AS name, {} AS at, {} AS payload, \"due_at\" AS due_at FROM {} WHERE state = 'ready' AND \"due_at\" <= {}",
                    quote(&queue.row_key),
                    quote(&queue.use_id),
                    quote(&queue.name),
                    quote(&queue.at),
                    quote(&queue.payload),
                    quote(&queue.table),
                    timestamp(dialect, 1)
                ),
                &[Value::from(now)],
            )?;
            for row in rows {
                let key = row.get("row_key").unwrap_or(&Value::Null);
                let id = key_of(&served.model.key.kind, key, &queue.table)?;
                let text = |name: &str| {
                    row.get(name)
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned()
                };
                out.push((
                    millis(row.get("due_at")),
                    DueDeadline {
                        type_name: type_name.clone(),
                        id,
                        use_id: text("use_id"),
                        name: text("name"),
                        at: row.get("at").and_then(Value::as_i64).unwrap_or_default(),
                        payload: json_value(dialect, row.get("payload")),
                    },
                ));
            }
        }
        out.sort_by(|(a, x), (b, y)| {
            (a, &x.type_name, &x.id, &x.use_id, &x.name).cmp(&(
                b,
                &y.type_name,
                &y.id,
                &y.use_id,
                &y.name,
            ))
        });
        Ok(out.into_iter().map(|(_, due)| due).collect())
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        let types = self.types.clone();
        let dialect = self.dialect();
        let mut found: Vec<(i64, i64, String, Value, QueuedChange)> = Vec::new();
        for (type_name, served) in &types.types {
            let Some(queue) = &served.table.changes else {
                continue;
            };
            let rows = self.db.query(
                &format!(
                    "SELECT \"id\" AS id, {} AS row_key, {} AS write, {} AS at, {} AS before, {} AS after, \"created_at\" AS created_at FROM {} WHERE state = 'ready'",
                    quote(&queue.row_key),
                    quote(&queue.write),
                    quote(&queue.at),
                    quote(&queue.before),
                    quote(&queue.after),
                    quote(&queue.table)
                ),
                &[],
            )?;
            for row in rows {
                let key = row.get("row_key").unwrap_or(&Value::Null);
                let id = key_of(&served.model.key.kind, key, &queue.table)?;
                let write = match row.get("write").and_then(Value::as_str) {
                    Some("insert") => WriteKind::Insert,
                    Some("update") => WriteKind::Update,
                    Some("delete") => WriteKind::Delete,
                    other => {
                        return Err(internal(format!(
                            "{}'s change of {id} is a write of {other:?}, which is no insert, update or delete",
                            queue.table
                        )))
                    }
                };
                let at = row.get("at").and_then(Value::as_i64).unwrap_or_default();
                let change = RowChange {
                    write,
                    at,
                    before: image_of(served, dialect, row.get("before"))?,
                    after: image_of(served, dialect, row.get("after"))?,
                };
                found.push((
                    at,
                    millis(row.get("created_at")),
                    queue.table.clone(),
                    row.get("id").cloned().unwrap_or(Value::Null),
                    QueuedChange {
                        seq: 0,
                        type_name: type_name.clone(),
                        id,
                        change,
                    },
                ));
            }
        }
        found.sort_by(|a, b| (a.0, a.1, &a.2).cmp(&(b.0, b.1, &b.2)));
        self.queued.clear();
        let mut out = Vec::with_capacity(found.len());
        for (_, _, table, message, mut change) in found {
            self.next_seq += 1;
            change.seq = self.next_seq;
            self.queued.insert(self.next_seq, (table, message));
            out.push(change);
        }
        Ok(out)
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        let types = self.types.clone();
        let dialect = self.dialect();
        let mut found: Vec<(i64, i64, QueuedEvent)> = Vec::new();
        for (type_name, served) in &types.types {
            let Some(queue) = &served.table.events else {
                continue;
            };
            let rows = self.db.query(
                &format!(
                    "SELECT {} AS row_key, {} AS use_id, {} AS kind, {} AS at, {} AS payload, \"created_at\" AS created_at FROM {}",
                    quote(&queue.row_key),
                    quote(&queue.use_id),
                    quote(&queue.kind),
                    quote(&queue.at),
                    quote(&queue.payload),
                    quote(&queue.table)
                ),
                &[],
            )?;
            for row in rows {
                let key = row.get("row_key").unwrap_or(&Value::Null);
                let text = |name: &str| {
                    row.get(name)
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned()
                };
                let at = row.get("at").and_then(Value::as_i64).unwrap_or_default();
                found.push((
                    at,
                    millis(row.get("created_at")),
                    QueuedEvent {
                        seq: 0,
                        type_name: type_name.clone(),
                        id: key_of(&served.model.key.kind, key, &queue.table)?,
                        use_id: text("use_id"),
                        kind: text("kind"),
                        at,
                        payload: json_value(dialect, row.get("payload")),
                    },
                ));
            }
        }
        // The queues hold no number of their own across tables: each
        // event's is its place in the order written.
        found.sort_by_key(|a| (a.0, a.1));
        Ok(found
            .into_iter()
            .enumerate()
            .map(|(i, (_, _, mut event))| {
                event.seq = i as i64 + 1;
                event
            })
            .filter(|event| event.seq > after)
            .collect())
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.db.begin()?;
        if let Err(refusal) = self.follow() {
            self.db.rollback()?;
            return Err(refusal);
        }
        Ok(())
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.db.commit()
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        self.db.rollback()
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        let types = self.types.clone();
        Ok(match read {
            StoreRead::Live => StoreAnswer::Live(self.live(&types)),
            StoreRead::Draft { .. } => StoreAnswer::Draft(None),
            StoreRead::Row { type_name, id } => match types.types.get(type_name) {
                Some(served) => StoreAnswer::Row(self.row(served, id)?),
                None => StoreAnswer::Row(None),
            },
            StoreRead::Rows { type_name } => match types.types.get(type_name) {
                Some(served) => StoreAnswer::Rows(self.rows(served)?),
                None => StoreAnswer::Rows(Vec::new()),
            },
            StoreRead::SubRows {
                type_name,
                id,
                use_id,
                table,
            } => {
                let served = types.get(type_name)?;
                StoreAnswer::SubRows(
                    self.sub_rows(served, Some(id), use_id, table)?
                        .into_iter()
                        .map(|held| held.row)
                        .collect(),
                )
            }
            StoreRead::TableSubRows {
                type_name,
                use_id,
                table,
            } => {
                let served = types.get(type_name)?;
                StoreAnswer::TableSubRows(self.sub_rows(served, None, use_id, table)?)
            }
            StoreRead::SharedRow {
                type_name,
                use_id,
                table,
                key,
            } => {
                let served = types.get(type_name)?;
                StoreAnswer::SharedRow(self.shared_row(served, use_id, table, key)?)
            }
            StoreRead::Armed {
                type_name,
                id,
                use_id,
                name,
            } => {
                let served = types.get(type_name)?;
                StoreAnswer::Armed(self.armed(served, id, use_id, name)?)
            }
        })
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        let types = self.types.clone();
        self.db.batch("SAVEPOINT runtime_unit")?;
        for write in &unit.writes {
            if let Err(stop) = self.apply_one(&types, write) {
                self.db
                    .batch("ROLLBACK TO SAVEPOINT runtime_unit; RELEASE SAVEPOINT runtime_unit")?;
                return match stop {
                    Stop::Moved => Ok(false),
                    Stop::Refused(refusal) => Err(refusal),
                };
            }
        }
        self.db.batch("RELEASE SAVEPOINT runtime_unit")?;
        Ok(true)
    }
}

/// A value's JSON text, for the record's columns.
fn json_text(value: &impl serde::Serialize) -> Result<String, Refusal> {
    serde_json::to_string(value).map_err(|error| internal(format!("a value is no JSON: {error}")))
}
