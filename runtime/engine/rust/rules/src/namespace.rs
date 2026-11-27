//! What an operation of the namespace as a whole reads and writes through
//! (D77's primitive 3, [`crate::Action::NamespaceOperation`]): a
//! [`NamespaceCx`]. It runs on no type and no row, keyed by the caller: the
//! caller's mentions and read threads across every type that composes the
//! behavior (Comments' inbox), or the deliveries a notifier claims.
//!
//! It has no config of its own. It reads the types that compose its
//! behavior and that the caller may read ([`NamespaceCx::composing`]), which
//! the host answers with its access policy asked of each, and each use's
//! config as the type holds it; their rows through the reads across a
//! table ([`NamespaceCx::read_rows`]) with the sub-tables of its behavior's
//! uses; and those uses' sub-table rows and shared rows. One that writes
//! writes those alone: a sub-table row of a row of a composing type through
//! a use of its behavior, and a shared row of such a use, as a write beside
//! the row is; never a row, an owned field, an event or a deadline. A write
//! of a type the caller may not read, or through a use of another
//! behavior, is a defect.

use serde_json::{Map, Value};

use crate::behavior::{Hook, Stop};
use crate::changes::{RowRef, SharedRowChange, SharedRowWrite, SubRowChange, SubRowWrite};
use crate::command::Caller;
use crate::context::listed;
use crate::reads::{Answer, LinkedRows, LockKey, ReadRequest, SharedRow, SubRow, TableRef};
use crate::refusal::{Refusal, Veto};
use crate::run::Run;
use crate::spec::{TypeSpec, UseSpec};

/// The namespace a host of the core keeps, which a refusal of an operation
/// of the namespace names: development mode keeps one, `default` (D117),
/// and a table's database is one.
pub const NAMESPACE: &str = "default";

/// The context of an operation of the namespace as a whole: the run, its
/// behavior, its operation's name, and whether it writes.
pub struct NamespaceCx<'r> {
    pub(crate) run: &'r Run<'r>,
    pub(crate) behavior: &'r str,
    pub(crate) operation: &'r str,
    pub(crate) writable: bool,
}

impl<'r> NamespaceCx<'r> {
    /// The behavior whose operation it is.
    pub fn behavior(&self) -> &'r str {
        self.behavior
    }

    /// The operation's declared name.
    pub fn operation(&self) -> &'r str {
        self.operation
    }

    /// Whether the operation writes, as its declaration says.
    pub fn writable(&self) -> bool {
        self.writable
    }

    /// The command's time, in epoch milliseconds.
    pub fn now(&self) -> i64 {
        self.run.command.now
    }

    pub fn caller(&self) -> &'r Caller {
        &self.run.command.caller
    }

    /// Whether the caller holds a permission, as the core's matcher answers;
    /// a caller standing in for an end user holds none. An empty permission
    /// is a defect.
    pub fn can(&self, permission: &str) -> Hook<bool> {
        if permission.is_empty() {
            return Err(self.defect("can() takes a permission: a non-empty string"));
        }
        let caller = &self.run.command.caller;
        if caller.stands_in {
            return Ok(false);
        }
        Ok(self
            .run
            .core
            .matcher()
            .matches(&caller.permissions, &[permission.to_owned()]))
    }

    /// The live versions of the types that compose the behavior and that the
    /// caller may read, in name order, as the host answers
    /// [`ReadRequest::Composing`].
    pub fn composing(&self) -> Hook<&'r [TypeSpec]> {
        let request = ReadRequest::composing(self.behavior);
        match self.run.read(&request)? {
            Answer::Specs(specs) => Ok(specs.as_slice()),
            _ => Err(Refusal::internal(format!(
                "the host answered {request:?} with an answer of another kind"
            ))
            .into()),
        }
    }

    /// Each use of the behavior on a type it may read: the type and the use,
    /// in the types' order and each type's list order. A use's config is
    /// [`UseSpec::config_or_empty`].
    pub fn uses(&self) -> Hook<Vec<(&'r TypeSpec, &'r UseSpec)>> {
        Ok(self
            .composing()?
            .iter()
            .flat_map(|spec| {
                spec.uses
                    .iter()
                    .filter(|entry| entry.behavior == self.behavior)
                    .map(move |entry| (spec, entry))
            })
            .collect())
    }

    /// The host's answer to a request, or a stop that asks for it.
    pub fn read(&self, request: &ReadRequest) -> Hook<&'r Answer> {
        self.run.read(request)
    }

    /// One page of the live rows of a type, as the host read them, with the
    /// sub-table rows `tables` names ([`ReadRequest::Rows`]); the run's own
    /// writes are not in it.
    pub fn read_rows(
        &self,
        type_name: &str,
        equal: &Map<String, Value>,
        tables: &[TableRef],
        after: Option<&str>,
    ) -> Hook<&'r LinkedRows> {
        self.run.read_linked(&ReadRequest::rows(
            type_name,
            equal.clone(),
            tables.to_vec(),
            after,
        ))
    }

    /// The rows of one sub-table of a use of the behavior on a row, as the
    /// run has them: the host's, with the run's own writes applied in
    /// order, in key order.
    pub fn sub_rows(
        &self,
        type_name: &str,
        key: &str,
        use_id: &str,
        table: &str,
    ) -> Hook<Vec<SubRow>> {
        self.held_table(type_name, use_id, table, false, "sub_rows")?;
        let request = ReadRequest::sub_rows(type_name, key, use_id, table);
        let read = self.run.read_sub_rows(&request)?;
        let rows = read
            .iter()
            .map(|row| (row.key.clone(), row.values.clone()))
            .collect();
        Ok(self
            .run
            .with_pending(RowRef::new(type_name, Some(key)), use_id, table, rows))
    }

    /// The row of a key of a shared table of a use of the behavior, as the
    /// run has it, with the version an update of it is held to; `None` when
    /// the table holds none of the key.
    pub fn shared_row(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Hook<Option<SharedRow>> {
        self.held_table(type_name, use_id, table, true, "shared_row")?;
        self.run.shared_row_of(type_name, use_id, table, key)
    }

    /// Inserts a sub-table row of a row of a composing type, through a use
    /// of the behavior; its values held to the table's fields.
    pub fn insert_row(
        &self,
        at: (&str, &str),
        use_id: &str,
        table: &str,
        key: &str,
        values: Map<String, Value>,
    ) -> Hook<()> {
        self.sub_row(at, use_id, table, key, SubRowChange::Insert { values })
    }

    /// Sets values of a sub-table row of a row of a composing type; null
    /// clears one.
    pub fn update_row(
        &self,
        at: (&str, &str),
        use_id: &str,
        table: &str,
        key: &str,
        values: Map<String, Value>,
    ) -> Hook<()> {
        self.sub_row(at, use_id, table, key, SubRowChange::Update { values })
    }

    /// Deletes a sub-table row of a row of a composing type.
    pub fn delete_row(&self, at: (&str, &str), use_id: &str, table: &str, key: &str) -> Hook<()> {
        self.sub_row(at, use_id, table, key, SubRowChange::Delete)
    }

    /// Sets values of a row of a shared table of a use of the behavior, held
    /// to the version it was read at: a concurrent writer that moved it runs
    /// the command again, so a write across the namespace fences itself on
    /// what it read.
    pub fn update_shared(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
        expected_version: i64,
        values: Map<String, Value>,
    ) -> Hook<()> {
        self.shared(
            type_name,
            use_id,
            table,
            key,
            SharedRowChange::Update {
                expected_version,
                values,
            },
        )
    }

    /// Inserts a row of a shared table of a use of the behavior.
    pub fn insert_shared(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
        values: Map<String, Value>,
    ) -> Hook<()> {
        self.shared(
            type_name,
            use_id,
            table,
            key,
            SharedRowChange::Insert { values },
        )
    }

    /// A new key, derived from the command's request id, the behavior and a
    /// label, as [`crate::Cx::new_key`] makes one: a claim's token.
    pub fn new_key(&self, label: &str) -> Hook<String> {
        self.run.namespace_key(self.behavior, label)
    }

    /// Holds the run until the host holds a lock.
    pub fn lock(&self, key: impl Into<LockKey>) -> Hook<()> {
        self.run.lock(key.into())
    }

    /// A refusal of the operation by the behavior: `vetoed`, as a veto of a
    /// row's operation, naming the namespace in place of a row. A code the
    /// declaration does not list is a defect instead.
    pub fn veto(&self, veto: impl Into<Veto>) -> Stop {
        let veto = veto.into();
        if veto.reason.is_empty() {
            return self.defect("a veto has a reason: a non-empty string");
        }
        if let Some(code) = &veto.code {
            let declared = self
                .run
                .core
                .declaration(self.behavior)
                .map(|declaration| declaration.vetoes.clone())
                .unwrap_or_default();
            if !declared.contains(code) {
                return self.defect(format!(
                    "a veto's code \"{code}\" is not one its declaration lists ({})",
                    listed(&declared)
                ));
            }
        }
        Refusal::veto(
            self.behavior,
            self.behavior,
            self.operation,
            &format!("namespace {NAMESPACE}"),
            None,
            veto,
        )
        .into()
    }

    /// A `forbidden` refusal.
    pub fn forbidden(&self, message: impl Into<String>) -> Stop {
        Refusal::forbidden(message).into()
    }

    /// A defect in the behavior's code: `behavior_error`.
    pub fn defect(&self, message: impl Into<String>) -> Stop {
        Refusal::defect(self.behavior, message).into()
    }

    // held_table holds a read or write to a table of a use of the behavior
    // on a type that composes it and the caller may read: shared or of each
    // row, as `shared` says.
    fn held_table(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        shared: bool,
        call: &str,
    ) -> Hook<crate::behavior::TableDecl> {
        let Some(spec) = self.composing()?.iter().find(|spec| spec.name == type_name) else {
            return Err(self.defect(format!(
                "{call}: {type_name} is no type that composes {} and the caller may read",
                self.behavior
            )));
        };
        let Some((_, entry)) = spec.use_of(use_id) else {
            return Err(self.defect(format!("{call}: {type_name} has no use {use_id}")));
        };
        if entry.behavior != self.behavior {
            return Err(self.defect(format!(
                "{call}: use {use_id} of {type_name} is {}'s, not {}'s",
                entry.behavior, self.behavior
            )));
        }
        let declaration = self
            .run
            .core
            .declaration(self.behavior)
            .ok_or_else(|| self.defect("the core runs no behavior of this name"))?;
        match declaration.find_table(table) {
            Some(declared) if declared.shared == shared => Ok(declared.clone()),
            Some(_) if shared => Err(self.defect(format!(
                "{call}: {table} is a sub-table of each row, not a shared table"
            ))),
            Some(_) => Err(self.defect(format!(
                "{call}: {table} is a shared table, read and written by key"
            ))),
            None => Err(self.defect(format!(
                "{call}: {table} is not a table it declares ({})",
                listed(&declaration.table_names())
            ))),
        }
    }

    // checked holds a write's values to a checked table's fields.
    fn checked(
        &self,
        call: &str,
        declared: &crate::behavior::TableDecl,
        values: &Map<String, Value>,
    ) -> Hook<()> {
        if !declared.is_checked() {
            return Ok(());
        }
        for name in values.keys() {
            if declared.key.as_deref() == Some(name.as_str()) {
                return Err(self.defect(format!(
                    "{call}: {name} is the row's key, which the write names"
                )));
            }
            if !declared.fields.contains(name) {
                return Err(self.defect(format!(
                    "{call}: {} has no field {name} (its fields: {})",
                    declared.name,
                    listed(&declared.values())
                )));
            }
        }
        Ok(())
    }

    fn writing(&self, what: &str) -> Hook<()> {
        if self.writable {
            Ok(())
        } else {
            Err(self.defect(format!(
                "{what}: an operation of the namespace that does not write writes nothing"
            )))
        }
    }

    fn sub_row(
        &self,
        (type_name, row_key): (&str, &str),
        use_id: &str,
        table: &str,
        key: &str,
        change: SubRowChange,
    ) -> Hook<()> {
        self.writing(&format!("a write of sub-table {table}"))?;
        let declared = self.held_table(type_name, use_id, table, false, "a sub-row write")?;
        if let SubRowChange::Insert { values } | SubRowChange::Update { values } = &change {
            self.checked("a sub-row write", &declared, values)?;
        }
        self.run.pending.borrow_mut().sub_rows.push(SubRowWrite {
            row: RowRef::new(type_name, Some(row_key)),
            use_id: use_id.to_owned(),
            table: table.to_owned(),
            key: key.to_owned(),
            change,
        });
        Ok(())
    }

    fn shared(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
        change: SharedRowChange,
    ) -> Hook<()> {
        self.writing(&format!("a write of shared table {table}"))?;
        let declared = self.held_table(type_name, use_id, table, true, "a shared write")?;
        let none = Map::new();
        let values = match &change {
            SharedRowChange::Insert { values } | SharedRowChange::Update { values, .. } => values,
            SharedRowChange::Delete { .. } => &none,
        };
        self.checked("a shared write", &declared, values)?;
        if key.is_empty() {
            return Err(self.defect(format!(
                "a shared write: a row of {table} takes a key: a non-empty string"
            )));
        }
        self.run
            .pending
            .borrow_mut()
            .shared_rows
            .push(SharedRowWrite {
                type_name: type_name.to_owned(),
                use_id: use_id.to_owned(),
                table: table.to_owned(),
                key: key.to_owned(),
                change,
            });
        Ok(())
    }
}
