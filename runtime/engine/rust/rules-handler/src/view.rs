//! The fields a read of a row computes (D103, D104's step 4), beyond the
//! row's own fields and its uses' owned ones: what a get and a list show
//! for each use whose declaration names a field it gives no type, a field
//! no row holds. Two kinds, as the core has them:
//!
//! - the fields a use computes from the row and its own sub-tables
//!   ([`superschematic_rules::Core::read_fields_at`]): Timers' `deadlines`,
//!   Revisions' `pendingProposals`, Lease's `active` at the read's time;
//! - the fields a use computes through reads of other rows, the core's read
//!   command ([`superschematic_rules::Action::Read`]): Rollups' `values`
//!   over the rows that point at the row, Dependencies' `blocked` over its
//!   blockers' statuses.
//!
//! Each is named as the use's other fields are ([`owned_field_name`]): a
//! field anchor's name then the field's (`blockersBlocked`), a type
//! anchor's the behavior's storage key then the field's
//! (`rollupsValues`). A read computes them as the caller, at the
//! request's time, and writes nothing.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{Action, Command, Refusal, SubRow};

use crate::bridge::{self, Bridge, Ran};
use crate::document::{owned_field_name, Model, Use};
use crate::handler::{Context, Handler, Live, Reads};
use crate::store::{HeldSubRow, StoreAnswer, StoreRead, StoredRow};

/// Which rows a read computes for: one row (a get), whose sub-tables it
/// reads by the row, or a page of a list, whose sub-tables it reads for
/// the whole type at once.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Scope {
    Row,
    Table,
}

impl Handler<'_> {
    /// The uses of a type whose declarations name a field computed at a
    /// read, in list order.
    fn computing<'m>(&self, model: &'m Model) -> Vec<&'m Use> {
        model
            .uses
            .iter()
            .filter(|used| {
                self.declarations()
                    .get(&used.spec.behavior)
                    .is_some_and(|declared| !declared.computed.is_empty())
            })
            .collect()
    }

    /// Each row's computed fields, by the name a response gives each, in
    /// the rows' order; `Ok(None)` while the store has not answered the
    /// reads they need. A type whose uses compute nothing asks for nothing.
    pub(crate) fn views(
        &self,
        live: &Live,
        model: &Model,
        rows: &[&StoredRow],
        scope: Scope,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<Option<Vec<Map<String, Value>>>, Refusal> {
        let computing = self.computing(model);
        if computing.is_empty() || rows.is_empty() {
            return Ok(Some(vec![Map::new(); rows.len()]));
        }
        let tables = self.sub_tables(model, &computing, rows, scope, reads);
        let spec = live
            .specs
            .get(&model.name)
            .cloned()
            .unwrap_or_else(|| model.spec());
        let bridge = Bridge {
            core: self.core(),
            specs: &live.specs,
            hearing: &live.hearing,
            own: None,
        };
        let mut out = Vec::with_capacity(rows.len());
        for row in rows {
            let command = Command::new(
                context.caller.clone(),
                context.now,
                Action::Read {
                    key: row.id.clone(),
                },
            )
            .with_request(context.request.clone());
            let through_reads = match bridge.run(&spec, &command, reads.answers) {
                Ran::Read(need) => {
                    for read in need {
                        reads.ask(read);
                    }
                    None
                }
                Ran::Refused(refusal) => return Err(refusal),
                Ran::Changed(changed) => Some(changed.changes.result.clone()),
            };
            let Some(tables) = &tables else {
                continue;
            };
            let Some(through_reads) = through_reads else {
                continue;
            };
            let mut fields = Map::new();
            for used in &computing {
                let empty = BTreeMap::new();
                let held = tables
                    .get(&(row.id.clone(), used.id().to_owned()))
                    .unwrap_or(&empty);
                let mut computed = self.core().read_fields_at(
                    &spec,
                    used.id(),
                    &bridge::core_row(row),
                    held,
                    context.now,
                )?;
                if let Some(Value::Object(viewed)) = through_reads.get(used.id()) {
                    computed.extend(viewed.clone());
                }
                for (name, value) in computed {
                    if value.is_null() {
                        continue;
                    }
                    let field =
                        owned_field_name(used.spec.field(), &used.spec.behavior, &name, false);
                    fields.insert(field, value);
                }
            }
            out.push(fields);
        }
        if reads.waiting() || out.len() != rows.len() {
            return Ok(None);
        }
        Ok(Some(out))
    }

    // sub_tables is each row's sub-table rows of each use that computes
    // fields, by row id and use id, then by table name, each table in key
    // order; `None` while the store has not answered.
    #[allow(clippy::type_complexity)]
    fn sub_tables(
        &self,
        model: &Model,
        computing: &[&Use],
        rows: &[&StoredRow],
        scope: Scope,
        reads: &mut Reads<'_>,
    ) -> Option<BTreeMap<(String, String), BTreeMap<String, Vec<SubRow>>>> {
        let mut out: BTreeMap<(String, String), BTreeMap<String, Vec<SubRow>>> = BTreeMap::new();
        let mut waiting = false;
        for used in computing {
            let Some(declared) = self.declarations().get(&used.spec.behavior) else {
                continue;
            };
            for table in declared.tables.iter().filter(|table| !table.shared) {
                match scope {
                    Scope::Row => {
                        for row in rows {
                            let read = StoreRead::SubRows {
                                type_name: model.name.clone(),
                                id: row.id.clone(),
                                use_id: used.id().to_owned(),
                                table: table.name.clone(),
                            };
                            match reads.ask(read) {
                                Some(StoreAnswer::SubRows(held)) => {
                                    out.entry((row.id.clone(), used.id().to_owned()))
                                        .or_default()
                                        .insert(table.name.clone(), held.clone());
                                }
                                _ => waiting = true,
                            }
                        }
                    }
                    Scope::Table => {
                        let read = StoreRead::TableSubRows {
                            type_name: model.name.clone(),
                            use_id: used.id().to_owned(),
                            table: table.name.clone(),
                        };
                        match reads.ask(read) {
                            Some(StoreAnswer::TableSubRows(held)) => {
                                for HeldSubRow { id, row } in held {
                                    out.entry((id.clone(), used.id().to_owned()))
                                        .or_default()
                                        .entry(table.name.clone())
                                        .or_default()
                                        .push(row.clone());
                                }
                            }
                            _ => waiting = true,
                        }
                    }
                }
            }
        }
        (!waiting).then_some(out)
    }
}

/// A rendered row with its computed fields merged in.
pub(crate) fn with_view(mut rendered: Value, view: Map<String, Value>) -> Value {
    if let Value::Object(fields) = &mut rendered {
        // The version stays last, as a response writes it.
        let version = fields.shift_remove("_version");
        fields.extend(view);
        if let Some(version) = version {
            fields.insert("_version".to_owned(), version);
        }
    }
    rendered
}
