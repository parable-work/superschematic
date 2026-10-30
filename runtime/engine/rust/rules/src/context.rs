//! What a hook reads and writes through (D103). A [`View`] is a guard's and
//! a validation's: the use's config, the row as the run has it, the
//! caller, the time, whether the caller holds a permission, and reads. A
//! [`Cx`] is an initializer's, a handler's, a deadline's and an
//! after-change hook's: a view that also sets the use's owned fields,
//! writes its sub-table rows, emits outbox events, arms and clears its
//! deadlines, makes keys, calls an operation of a use of its row or of
//! another row, and creates rows. A [`ConfigCx`] is a config change's,
//! which reads alone.
//!
//! A read answers from the host's answers or stops the run with the
//! request (`ctx.read(...)?`): the host reads, adds the answer and runs the
//! command again, and the same hooks reach the same read and get the
//! answer. A read returns the state the host read; the hook's own row as
//! the run changes it is the context's own and owned fields.
//!
//! A use writes only its own state: its owned fields and sub-tables, by
//! their declared names, and its deadlines, on the row it runs on. It
//! reaches another use's state, on its row or another, through that use's
//! operations ([`Cx::call`], [`Cx::call_on`]).

use std::ops::Deref;

use serde_json::{Map, Value};

use crate::behavior::{Hook, Stop};
use crate::changes::{DeadlineChange, OutboxEvent, SubRowChange, SubRowWrite};
use crate::command::Caller;
use crate::reads::{Answer, LockKey, ReadRequest, Row};
use crate::refusal::{Refusal, Veto};
use crate::run::{Frame, Run};
use crate::spec::{Anchor, TypeSpec};

/// A read-only context: a guard's and a validation's, and what a [`Cx`]
/// reads through.
pub struct View<'r, C> {
    pub(crate) run: &'r Run<'r>,
    pub(crate) frame: Frame<'r>,
    pub(crate) index: usize,
    pub(crate) config: &'r C,
    pub(crate) action: &'r str,
}

impl<'r, C> View<'r, C> {
    /// The use's parsed config.
    pub fn config(&self) -> &'r C {
        self.config
    }

    /// The name of the type of the row the hook runs on.
    pub fn type_name(&self) -> &'r str {
        &self.frame.spec.name
    }

    /// The use's id: the behavior's name on a type anchor, the field's on a
    /// field anchor.
    pub fn use_id(&self) -> &'r str {
        self.frame.uses[self.index].spec.id()
    }

    /// The use's behavior's name.
    pub fn behavior(&self) -> &'r str {
        &self.frame.uses[self.index].spec.behavior
    }

    pub fn anchor(&self) -> &'r Anchor {
        &self.frame.uses[self.index].spec.anchor
    }

    /// The field a field anchor names; `None` on a type anchor.
    pub fn field(&self) -> Option<&'r str> {
        self.frame.uses[self.index].spec.field()
    }

    /// What a veto refuses: `create`, `update`, `delete`, an operation's
    /// name or a deadline's.
    pub fn action(&self) -> &'r str {
        self.action
    }

    /// The key of the row the hook runs on; `None` for a create the host
    /// keys.
    pub fn key(&self) -> Option<&'r str> {
        self.frame.key
    }

    /// The command's time, in epoch milliseconds.
    pub fn now(&self) -> i64 {
        self.run.command.now
    }

    pub fn caller(&self) -> &'r Caller {
        &self.run.command.caller
    }

    /// Whether the caller holds a permission, as the core's matcher
    /// answers; a caller standing in for an end user holds none. An empty
    /// permission is a defect.
    pub fn can(&self, permission: &str) -> Hook<bool> {
        self.run.can(self.frame, self.index, permission)
    }

    /// The row's own fields as the run has them: before the change for a
    /// guard of an update, the new row's for a create, after the patch for
    /// an update's after-change hooks.
    pub fn own(&self) -> Map<String, Value> {
        self.run.rows.borrow()[self.frame.row].own.clone()
    }

    /// One own field; `None` when it has no value.
    pub fn own_field(&self, name: &str) -> Option<Value> {
        self.run.rows.borrow()[self.frame.row]
            .own
            .get(name)
            .filter(|value| !value.is_null())
            .cloned()
    }

    /// One of the use's owned fields as the run has it, its writes so far
    /// included; `None` when it has no value.
    pub fn get(&self, field: &str) -> Option<Value> {
        self.get_of(self.use_id(), field)
    }

    /// An owned field of another use of the row, by its use id; `None`
    /// when it has no value. A use reads another's state, and changes it
    /// only through its operations.
    pub fn get_of(&self, use_id: &str, field: &str) -> Option<Value> {
        self.run.rows.borrow()[self.frame.row]
            .owned
            .get(use_id)
            .and_then(|fields| fields.get(field))
            .filter(|value| !value.is_null())
            .cloned()
    }

    /// The version the host read the row at; `None` for a create.
    pub fn version(&self) -> Option<i64> {
        self.run.rows.borrow()[self.frame.row].version
    }

    /// The host's answer to a request, or a stop that asks for it.
    pub fn read(&self, request: &ReadRequest) -> Hook<&'r Answer> {
        self.run.read(request)
    }

    /// The answers to several requests, in order, or a stop that asks for
    /// every one the host has not answered.
    pub fn read_many(&self, requests: &[ReadRequest]) -> Hook<Vec<&'r Answer>> {
        self.run.read_many(requests)
    }

    /// A row of a type by its key, as the host read it; `None` when the
    /// type has no row with the key.
    pub fn read_row(&self, type_name: &str, key: &str) -> Hook<Option<&'r Row>> {
        self.run.read_row(&ReadRequest::row(type_name, key))
    }

    /// Whether a type has any rows.
    pub fn has_rows(&self, type_name: &str) -> Hook<bool> {
        self.run.has_rows(type_name)
    }

    /// A type's live version, its uses and their configs; `None` when no
    /// type has the name. The command's own type is the version the command
    /// runs on, read from no host.
    pub fn read_spec(&self, type_name: &str) -> Hook<Option<&'r TypeSpec>> {
        self.run.read_spec(type_name)
    }

    /// The config a use of a type gives, as the type holds it (`{}` when it
    /// gives none): what a behavior that reads another type's Workflow
    /// reads its states' outcomes from. `None` when the type has no use
    /// with the id, or no type has the name.
    pub fn config_of(&self, type_name: &str, use_id: &str) -> Hook<Option<Value>> {
        Ok(self.read_spec(type_name)?.and_then(|spec| {
            spec.use_of(use_id)
                .map(|(_, entry)| entry.config_or_empty())
        }))
    }

    /// Holds the run until the host holds a lock: call it before the reads
    /// a rule across rows decides on.
    pub fn lock(&self, key: impl Into<LockKey>) -> Hook<()> {
        self.run.lock(key.into())
    }

    /// A refusal of the action by this use: `vetoed`, with the veto's
    /// reason, code and details. A code the declaration does not list is a
    /// defect instead.
    pub fn veto(&self, veto: impl Into<Veto>) -> Stop {
        self.run
            .veto_refusal(self.frame, self.index, self.action, veto.into())
    }

    /// A `forbidden` refusal: the caller lacks a permission the config
    /// names.
    pub fn forbidden(&self, message: impl Into<String>) -> Stop {
        Refusal::forbidden(message).into()
    }

    /// A defect in the behavior's code: `behavior_error`.
    pub fn defect(&self, message: impl Into<String>) -> Stop {
        self.run.defect_of(self.frame, self.index, message)
    }
}

/// A context that writes: an initializer's, a handler's, a deadline's and
/// an after-change hook's. It reads as a [`View`]. A read-only operation's
/// context refuses every write as a defect.
pub struct Cx<'r, C> {
    pub(crate) view: View<'r, C>,
    pub(crate) writable: bool,
}

impl<'r, C> Deref for Cx<'r, C> {
    type Target = View<'r, C>;

    fn deref(&self) -> &View<'r, C> {
        &self.view
    }
}

impl<'r, C> Cx<'r, C> {
    /// The context as a view.
    pub fn view(&self) -> &View<'r, C> {
        &self.view
    }

    /// Whether the context writes: false in a read-only operation.
    pub fn writable(&self) -> bool {
        self.writable
    }

    // writing refuses a write from a read-only context, and one that names
    // what the declaration does not.
    fn writing(&self, what: &str) -> Hook<()> {
        if self.writable {
            Ok(())
        } else {
            Err(self.defect(format!("{what}: a read-only operation writes nothing")))
        }
    }

    /// Sets one of the use's owned fields, by its declared name; null
    /// clears it.
    pub fn set(&self, field: &str, value: impl Into<Value>) -> Hook<()> {
        self.writing(&format!("set({field})"))?;
        let view = &self.view;
        let declaration = view.run.declaration(view.frame, view.index);
        if !declaration.has_field(field) {
            return Err(self.defect(format!(
                "set: {field} is not a field it declares ({})",
                listed(&declaration.fields)
            )));
        }
        view.run
            .set_owned(view.frame.row, view.use_id(), field, value.into());
        Ok(())
    }

    /// Inserts a row of one of the use's sub-tables, by a key it makes
    /// ([`Cx::new_key`]) or one the row's data gives.
    pub fn insert_row(&self, table: &str, key: &str, values: Map<String, Value>) -> Hook<()> {
        self.sub_row(table, key, SubRowChange::Insert { values })
    }

    /// Sets values of a row of one of the use's sub-tables.
    pub fn update_row(&self, table: &str, key: &str, values: Map<String, Value>) -> Hook<()> {
        self.sub_row(table, key, SubRowChange::Update { values })
    }

    /// Deletes a row of one of the use's sub-tables.
    pub fn delete_row(&self, table: &str, key: &str) -> Hook<()> {
        self.sub_row(table, key, SubRowChange::Delete)
    }

    fn sub_row(&self, table: &str, key: &str, change: SubRowChange) -> Hook<()> {
        self.writing(&format!("a write of sub-table {table}"))?;
        let view = &self.view;
        let declaration = view.run.declaration(view.frame, view.index);
        if !declaration.has_table(table) {
            return Err(self.defect(format!(
                "{table} is not a sub-table it declares ({})",
                listed(&declaration.tables)
            )));
        }
        view.run.pending.borrow_mut().sub_rows.push(SubRowWrite {
            row: view.frame.row_ref(),
            use_id: view.use_id().to_owned(),
            table: table.to_owned(),
            key: key.to_owned(),
            change,
        });
        Ok(())
    }

    /// Emits an outbox event of the use's, about its row.
    pub fn emit(&self, kind: &str, payload: Value) -> Hook<()> {
        self.writing(&format!("emit({kind})"))?;
        if kind.is_empty() {
            return Err(self.defect("emit takes a kind: a non-empty string"));
        }
        let view = &self.view;
        view.run.pending.borrow_mut().events.push(OutboxEvent {
            row: view.frame.row_ref(),
            kind: kind.to_owned(),
            use_id: view.use_id().to_owned(),
            payload,
        });
        Ok(())
    }

    /// Arms one of the use's deadlines on the row, at a time in epoch
    /// milliseconds, with a payload the deadline's command carries back.
    pub fn arm(&self, name: &str, at: i64, payload: Value) -> Hook<()> {
        self.writing(&format!("arm({name})"))?;
        if name.is_empty() {
            return Err(self.defect("arm takes a deadline's name: a non-empty string"));
        }
        let view = &self.view;
        view.run
            .pending
            .borrow_mut()
            .deadlines
            .push(DeadlineChange::Arm {
                row: view.frame.row_ref(),
                use_id: view.use_id().to_owned(),
                name: name.to_owned(),
                at,
                payload,
            });
        Ok(())
    }

    /// Clears one of the use's deadlines on the row.
    pub fn clear(&self, name: &str) -> Hook<()> {
        self.writing(&format!("clear({name})"))?;
        let view = &self.view;
        view.run
            .pending
            .borrow_mut()
            .deadlines
            .push(DeadlineChange::Clear {
                row: view.frame.row_ref(),
                use_id: view.use_id().to_owned(),
                name: name.to_owned(),
            });
        Ok(())
    }

    /// A new key for a row the use makes (a sub-table row, a row of another
    /// type), by a label of its own (`comment`, `hold`): derived from the
    /// command's request id, the use, the label and a count, so every key a
    /// run makes is new and a run again, after a read or a row that moved,
    /// makes the same ones. A command that carries no request id refuses it
    /// as a defect.
    pub fn new_key(&self, label: &str) -> Hook<String> {
        self.view
            .run
            .new_key(self.view.frame, self.view.index, label)
    }

    /// Calls an operation of a use of the row (its own or another's), by
    /// the use's id: the operation's parameter check, every use's guard in
    /// list order (each told the call is this use's), and its handler. Its
    /// effects join the run's change set; when it is refused they are
    /// undone and the refusal returns, which the caller may pass on or go
    /// on past. A read-only operation calls only operations that do not
    /// write.
    pub fn call(&self, use_id: &str, operation: &str, params: &Value) -> Hook<Value> {
        self.view.run.call(
            self.view.frame,
            self.view.index,
            self.writable,
            use_id,
            operation,
            params,
        )
    }

    /// Calls an operation of a use of another row, of this type or another,
    /// by the row's type and key and the use's id, as a caller's operation
    /// runs there: the row's type is bound (read when it is not the
    /// command's), the row read, and the operation's parameter check, every
    /// guard of that type (each told the call is this use's, of this
    /// type), its handler and, for a writing operation, that row's
    /// after-change hooks run. A writing operation writes the row's update,
    /// held to the version the host read it at. The row the hook runs on is
    /// a [`Cx::call`]. A row the type has none of is `not_found`; a refused
    /// call is undone as a [`Cx::call`] is.
    pub fn call_on(
        &self,
        type_name: &str,
        key: &str,
        use_id: &str,
        operation: &str,
        params: &Value,
    ) -> Hook<Value> {
        self.view.run.call_on(
            self.view.frame,
            self.view.index,
            self.writable,
            type_name,
            key,
            use_id,
            operation,
            params,
        )
    }

    /// Creates a row of a type, this one or another, by its key, its own
    /// fields and its uses' create parameters by use id, as a caller's
    /// create runs: the create parameters, the type's validation, guards,
    /// initializers and after-change hooks. A key the run has reached
    /// already is a `conflict`, and the host refuses one its layout holds.
    /// A refused create is undone as a refused call is.
    pub fn create_row(
        &self,
        type_name: &str,
        key: &str,
        own: &Map<String, Value>,
        params: &Map<String, Value>,
    ) -> Hook<()> {
        self.view.run.create_row(
            self.view.frame,
            self.view.index,
            self.writable,
            type_name,
            key,
            own,
            params,
        )
    }
}

/// A config change's context: what it reads, and the run it reads in.
pub struct ConfigCx<'r> {
    pub(crate) run: &'r Run<'r>,
    pub(crate) use_id: &'r str,
    pub(crate) behavior: &'r str,
}

impl<'r> ConfigCx<'r> {
    pub fn type_name(&self) -> &'r str {
        &self.run.top.spec.name
    }

    /// The id of the use whose config changes.
    pub fn use_id(&self) -> &'r str {
        self.use_id
    }

    pub fn behavior(&self) -> &'r str {
        self.behavior
    }

    pub fn now(&self) -> i64 {
        self.run.command.now
    }

    pub fn caller(&self) -> &'r Caller {
        &self.run.command.caller
    }

    /// Whether the type has any rows: a row may be in any state the old
    /// config allowed.
    pub fn has_rows(&self) -> Hook<bool> {
        self.run.has_rows(&self.run.top.spec.name)
    }

    /// The host's answer to a request, or a stop that asks for it.
    pub fn read(&self, request: &ReadRequest) -> Hook<&'r Answer> {
        self.run.read(request)
    }

    /// A defect in the behavior's code.
    pub fn defect(&self, message: impl Into<String>) -> Stop {
        Refusal::defect(self.behavior, message).into()
    }
}

// listed names declared names for a defect's message, `none` for none.
pub(crate) fn listed(names: &[String]) -> String {
    if names.is_empty() {
        "none".to_owned()
    } else {
        names.join(", ")
    }
}
