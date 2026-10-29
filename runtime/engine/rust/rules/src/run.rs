//! One run of a command (D103): the type's uses bound to their parsed
//! configs, each row the run reaches as the run has it, the change set it
//! builds, and the reads and locks it asked for that the host has not
//! answered. A run is thrown away when it stops; the next one, with more
//! answers, starts again from the top, so nothing here outlives a call of
//! [`crate::Core::run`].
//!
//! A command composes across the type's uses in list order: validation of
//! own fields (every use's issues together), then the guards (the first
//! veto wins), then the initializers or the operation's handler, then the
//! after-change hooks. Every effect lands in one change set; the first
//! refusal ends the run.
//!
//! A hook runs in a frame: a type, its bound uses and one row. The
//! command's frame is its own type on its own row; a use reaches another
//! row through [`crate::Cx::call_on`] and [`crate::Cx::create_row`], which
//! bind that row's type (read with [`ReadRequest::Spec`] when it is not the
//! command's) and run its parameter checks, guards, handlers and
//! after-change hooks in a frame of its own, so a row of any type is held to
//! its own type's rules. Every row the run writes is in the change set,
//! each held to its own version.

use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;

use serde_json::{Map, Value};

use crate::behavior::{
    Change, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook, OperationDecl, Stop,
    ValidationRequest,
};
use crate::changes::{ChangeSet, DeadlineChange, OutboxEvent, RowRef, RowWrite, SubRowWrite};
use crate::command::{Action, Command};
use crate::context::{listed, ConfigCx};
use crate::core::{Core, Outcome, Registered, Step};
use crate::erased::{AnyConfig, Scope};
use crate::patch::merge_patch;
use crate::reads::{Answer, Answers, LockKey, ReadRequest, Row};
use crate::refusal::{
    Code, ConfigIssue, ConfigIssueKind, Refusal, SchemaChange, SchemaIssue, ValidationIssue, Veto,
};
use crate::spec::{Anchor, TypeSpec, UseSpec};

/// How deep operations, the calls they make and the rows they create nest,
/// the engine's limit.
pub const MAX_CALL_DEPTH: usize = 16;

/// One use of the type with its behavior and its parsed config.
pub(crate) struct Bound<'a> {
    pub spec: &'a UseSpec,
    pub registered: &'a Registered,
    pub config: AnyConfig,
}

/// Where hooks run: a type, its uses bound, and the row they run on.
#[derive(Clone, Copy)]
pub(crate) struct Frame<'a> {
    pub spec: &'a TypeSpec,
    pub uses: &'a [Bound<'a>],
    /// The row's place in the run's rows.
    pub row: usize,
    /// The row's key; `None` for a create the host keys, and for the
    /// define and publish checks, which run on no row.
    pub key: Option<&'a str>,
}

impl Frame<'_> {
    /// The row as a change set names it.
    pub fn row_ref(&self) -> RowRef {
        RowRef::new(&self.spec.name, self.key)
    }
}

/// What the change set writes of a row the run reached.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Write {
    /// Nothing: the run read it.
    Read,
    Create,
    /// An update, with the merge patch of its own fields.
    Update(Map<String, Value>),
    Delete,
}

/// A row the run reached: what the host read (or the create's own fields),
/// with the run's writes, and what the change set writes of it.
#[derive(Clone)]
pub(crate) struct Touched {
    pub type_name: String,
    pub key: Option<String>,
    pub own: Map<String, Value>,
    pub owned: BTreeMap<String, Map<String, Value>>,
    /// The version the host read it at; `None` for a row the run creates.
    pub version: Option<i64>,
    pub write: Write,
    /// The owned fields the run set, by use id.
    pub set: BTreeMap<String, Map<String, Value>>,
}

impl Touched {
    fn read(type_name: &str, key: &str, row: &Row) -> Self {
        Touched {
            type_name: type_name.to_owned(),
            key: Some(key.to_owned()),
            own: row.own.clone(),
            owned: row.owned.clone(),
            version: Some(row.version),
            write: Write::Read,
            set: BTreeMap::new(),
        }
    }

    fn created(type_name: &str, key: Option<&str>, own: &Map<String, Value>) -> Self {
        Touched {
            type_name: type_name.to_owned(),
            key: key.map(str::to_owned),
            own: own.clone(),
            owned: BTreeMap::new(),
            version: None,
            write: Write::Create,
            set: BTreeMap::new(),
        }
    }

    // into_write is the row's write in the change set; `None` for a row the
    // run only read. Only a create's row may have no key, and only a row
    // the run read has a version.
    fn into_write(self) -> Option<RowWrite> {
        let expected_version = self.version.unwrap_or_default();
        match self.write {
            Write::Read => None,
            Write::Create => Some(RowWrite::Create {
                type_name: self.type_name,
                key: self.key,
                own: self.own,
                owned: self.set,
            }),
            Write::Update(patch) => Some(RowWrite::Update {
                type_name: self.type_name,
                key: self.key.unwrap_or_default(),
                expected_version,
                own: patch,
                owned: self.set,
            }),
            Write::Delete => Some(RowWrite::Delete {
                type_name: self.type_name,
                key: self.key.unwrap_or_default(),
                expected_version,
            }),
        }
    }
}

/// The sub-table rows, events and deadlines the run builds, each naming its
/// row.
#[derive(Clone, Default)]
pub(crate) struct Pending {
    pub sub_rows: Vec<SubRowWrite>,
    pub events: Vec<OutboxEvent>,
    pub deadlines: Vec<DeadlineChange>,
}

/// The reads and locks the run asked for that the host has not answered.
/// The run returns them even when a hook went on past the stop.
#[derive(Default)]
pub(crate) struct Missing {
    pub reads: Vec<ReadRequest>,
    pub locks: Vec<LockKey>,
}

/// What a call undoes when it is refused.
struct Saved {
    rows: Vec<Touched>,
    pending: Pending,
}

pub(crate) struct Run<'a> {
    pub core: &'a Core,
    pub command: &'a Command,
    pub answers: &'a Answers,
    /// The command's frame: its type, on its row, which is the first the
    /// run reaches.
    pub top: Frame<'a>,
    pub rows: RefCell<Vec<Touched>>,
    pub pending: RefCell<Pending>,
    pub missing: RefCell<Missing>,
    /// How many keys each use has made, by type, use and label.
    keys: RefCell<BTreeMap<(String, String, String), u64>>,
    depth: Cell<usize>,
}

impl<'a> Run<'a> {
    /// Binds each use of a type to a behavior the core runs and its parsed
    /// config: the define check. Every use it refuses is an issue: the uses
    /// themselves and the configs' shapes first, and the configs' rules
    /// only when every use and every shape is sound, as a schema
    /// document's validation runs before its behaviors' checks.
    pub fn bind(core: &'a Core, spec: &'a TypeSpec) -> Result<Vec<Bound<'a>>, Refusal> {
        let mut shapes = Vec::new();
        let mut rules = Vec::new();
        let mut bound = Vec::new();
        for (index, entry) in spec.uses.iter().enumerate() {
            let id = entry.id();
            let issue = |kind: ConfigIssueKind, path: String, message: String| ConfigIssue {
                use_id: id.to_owned(),
                index,
                behavior: entry.behavior.clone(),
                kind,
                path,
                message,
            };
            if id.is_empty() {
                shapes.push(issue(
                    ConfigIssueKind::Use,
                    String::new(),
                    format!(
                        "type {}: the use of behavior {} on a field names no field",
                        spec.name, entry.behavior
                    ),
                ));
                continue;
            }
            if spec.uses[..index].iter().any(|earlier| earlier.id() == id) {
                shapes.push(issue(
                    ConfigIssueKind::Use,
                    String::new(),
                    format!(
                        "type {}: two uses have the id {id}; a behavior is anchored on the type once, and each field carries one",
                        spec.name
                    ),
                ));
                continue;
            }
            let Some(registered) = core.find(&entry.behavior) else {
                shapes.push(issue(
                    ConfigIssueKind::Use,
                    String::new(),
                    format!(
                        "type {}: behavior {} is not one this core runs ({})",
                        spec.name,
                        entry.behavior,
                        listed(&core.names())
                    ),
                ));
                continue;
            };
            // A field anchor's field is the behavior's value, which the
            // layout maps to the field's own column: a behavior that names
            // no value field has nothing to put there.
            if let Some(field) = entry.field() {
                let declaration = &registered.declaration;
                let value = declaration
                    .value
                    .as_deref()
                    .filter(|value| declaration.has_field(value));
                if value.is_none() {
                    shapes.push(issue(
                        ConfigIssueKind::Use,
                        String::new(),
                        format!(
                            "type {}: behavior {} declares no value field, so it cannot be anchored on field {field}",
                            spec.name, entry.behavior
                        ),
                    ));
                    continue;
                }
            }
            match parse_use(registered, spec, index) {
                Parsed::Config(config) => bound.push(Bound {
                    spec: entry,
                    registered,
                    config,
                }),
                Parsed::Missing => shapes.push(issue(
                    ConfigIssueKind::Schema,
                    String::new(),
                    MISSING_CONFIG.to_owned(),
                )),
                Parsed::Refused(ConfigError::Schema(found)) if found.is_empty() => {
                    shapes.push(issue(
                        ConfigIssueKind::Schema,
                        "/config".to_owned(),
                        "must match its behavior's config schema".to_owned(),
                    ));
                }
                Parsed::Refused(ConfigError::Schema(found)) => {
                    shapes.extend(found.into_iter().map(|found| {
                        issue(
                            ConfigIssueKind::Schema,
                            format!("/config{}", found.path),
                            found.message,
                        )
                    }));
                }
                Parsed::Refused(ConfigError::Rule(reason)) => rules.push(issue(
                    ConfigIssueKind::Rule,
                    "/config".to_owned(),
                    format!(
                        "{}behavior {} config: {reason}",
                        subject_prefix(spec, entry),
                        entry.behavior
                    ),
                )),
            }
        }
        if !shapes.is_empty() {
            Err(Refusal::invalid_config(&spec.name, shapes))
        } else if !rules.is_empty() {
            Err(Refusal::invalid_config(&spec.name, rules))
        } else {
            Ok(bound)
        }
    }

    pub fn new(
        core: &'a Core,
        spec: &'a TypeSpec,
        command: &'a Command,
        answers: &'a Answers,
        uses: &'a [Bound<'a>],
    ) -> Self {
        Run {
            core,
            command,
            answers,
            top: Frame {
                spec,
                uses,
                row: 0,
                key: command.action.key(),
            },
            rows: RefCell::new(Vec::new()),
            pending: RefCell::new(Pending::default()),
            missing: RefCell::new(Missing::default()),
            keys: RefCell::new(BTreeMap::new()),
            depth: Cell::new(0),
        }
    }

    /// The run's step: the locks it still needs, else the reads, else its
    /// outcome.
    pub fn finish(self, result: Hook<ChangeSet>) -> Step {
        let missing = self.missing.into_inner();
        if !missing.locks.is_empty() {
            return Step::Lock(missing.locks);
        }
        if !missing.reads.is_empty() {
            return Step::Read(missing.reads);
        }
        match result {
            Ok(changes) => Step::Done(Outcome::Changed(Box::new(changes))),
            Err(Stop::Refuse(refusal)) => Step::Done(Outcome::Refused(*refusal)),
            Err(Stop::Read(reads)) => Step::Read(reads),
            Err(Stop::Lock(locks)) => Step::Lock(locks),
        }
    }

    // ---- what the contexts reach ----

    pub fn declaration<'f>(&self, frame: Frame<'f>, index: usize) -> &'f Declaration {
        &frame.uses[index].registered.declaration
    }

    pub fn read(&self, request: &ReadRequest) -> Hook<&'a Answer> {
        if let Some(answer) = self.answers.get(request) {
            return Ok(answer);
        }
        let mut missing = self.missing.borrow_mut();
        if !missing.reads.contains(request) {
            missing.reads.push(request.clone());
        }
        Err(Stop::Read(vec![request.clone()]))
    }

    pub fn read_many(&self, requests: &[ReadRequest]) -> Hook<Vec<&'a Answer>> {
        let mut found = Vec::new();
        let mut lacking = Vec::new();
        for request in requests {
            match self.answers.get(request) {
                Some(answer) => found.push(answer),
                None => lacking.push(request.clone()),
            }
        }
        if lacking.is_empty() {
            return Ok(found);
        }
        let mut missing = self.missing.borrow_mut();
        for request in &lacking {
            if !missing.reads.contains(request) {
                missing.reads.push(request.clone());
            }
        }
        Err(Stop::Read(lacking))
    }

    pub fn read_row(&self, request: &ReadRequest) -> Hook<Option<&'a Row>> {
        match self.read(request)? {
            Answer::Row(row) => Ok(row.as_ref()),
            _ => Err(mismatched(request)),
        }
    }

    pub fn has_rows(&self, type_name: &str) -> Hook<bool> {
        let request = ReadRequest::has_rows(type_name);
        match self.read(&request)? {
            Answer::HasRows(found) => Ok(*found),
            _ => Err(mismatched(&request)),
        }
    }

    /// A type's live version: the command's own spec for its own type, so a
    /// run reads one version of it, and otherwise the host's answer.
    pub fn read_spec(&self, type_name: &str) -> Hook<Option<&'a TypeSpec>> {
        if type_name == self.top.spec.name {
            return Ok(Some(self.top.spec));
        }
        let request = ReadRequest::spec(type_name);
        match self.read(&request)? {
            Answer::Spec(spec) => Ok(spec.as_ref()),
            _ => Err(mismatched(&request)),
        }
    }

    pub fn lock(&self, key: LockKey) -> Hook<()> {
        if self.answers.holds(&key) {
            return Ok(());
        }
        let mut missing = self.missing.borrow_mut();
        if !missing.locks.contains(&key) {
            missing.locks.push(key.clone());
        }
        Err(Stop::Lock(vec![key]))
    }

    pub fn can(&self, frame: Frame<'_>, index: usize, permission: &str) -> Hook<bool> {
        if permission.is_empty() {
            return Err(self.defect_of(
                frame,
                index,
                "can() takes a permission: a non-empty string",
            ));
        }
        let caller = &self.command.caller;
        if caller.stands_in {
            return Ok(false);
        }
        Ok(self
            .core
            .matcher()
            .matches(&caller.permissions, &[permission.to_owned()]))
    }

    pub fn set_owned(&self, row: usize, use_id: &str, field: &str, value: Value) {
        let mut rows = self.rows.borrow_mut();
        let touched = &mut rows[row];
        touched
            .owned
            .entry(use_id.to_owned())
            .or_default()
            .insert(field.to_owned(), value.clone());
        touched
            .set
            .entry(use_id.to_owned())
            .or_default()
            .insert(field.to_owned(), value);
    }

    /// A key for a row a use makes: derived from the command's request id,
    /// the use and a label, and counted, so each key of a run is new and a
    /// run again makes the same ones.
    pub fn new_key(&self, frame: Frame<'_>, index: usize, label: &str) -> Hook<String> {
        if label.is_empty() {
            return Err(self.defect_of(frame, index, "new_key takes a label: a non-empty string"));
        }
        let request = &self.command.request;
        if request.is_empty() {
            return Err(self.defect_of(
                frame,
                index,
                "new_key: the command carries no request id to derive a key from, which the host gives with each command",
            ));
        }
        let use_id = frame.uses[index].spec.id();
        let mut keys = self.keys.borrow_mut();
        let count = keys
            .entry((frame.spec.name.clone(), use_id.to_owned(), label.to_owned()))
            .or_default();
        *count += 1;
        Ok(format!("{request}:{use_id}:{label}:{count}"))
    }

    pub fn defect_of(&self, frame: Frame<'_>, index: usize, message: impl Into<String>) -> Stop {
        Refusal::defect(&frame.uses[index].spec.behavior, message).into()
    }

    /// A use's veto of an action, its reason and code held to its
    /// declaration: one that breaks it is a defect instead.
    pub fn veto_refusal(&self, frame: Frame<'_>, index: usize, action: &str, veto: Veto) -> Stop {
        if let Err(stop) = self.check_veto(frame, index, "a veto", &veto) {
            return stop;
        }
        let entry = frame.uses[index].spec;
        Refusal::veto(
            &entry.behavior,
            entry.id(),
            action,
            &frame.spec.name,
            frame.key,
            veto,
        )
        .into()
    }

    fn check_veto(&self, frame: Frame<'_>, index: usize, what: &str, veto: &Veto) -> Hook<()> {
        if veto.reason.is_empty() {
            return Err(self.defect_of(
                frame,
                index,
                format!("{what} has a reason: a non-empty string"),
            ));
        }
        let Some(code) = &veto.code else {
            return Ok(());
        };
        let declared = &self.declaration(frame, index).vetoes;
        if declared.contains(code) {
            return Ok(());
        }
        Err(self.defect_of(
            frame,
            index,
            format!(
                "a veto's code {} is not one its declaration lists ({})",
                quoted(code),
                listed(declared)
            ),
        ))
    }

    // save is what a refused call undoes; restore undoes it.
    fn save(&self) -> Saved {
        Saved {
            rows: self.rows.borrow().clone(),
            pending: self.pending.borrow().clone(),
        }
    }

    fn restore(&self, saved: Saved) {
        *self.rows.borrow_mut() = saved.rows;
        *self.pending.borrow_mut() = saved.pending;
    }

    // undoing runs a call and undoes its effects when it is refused.
    fn undoing<T>(&self, call: impl FnOnce() -> Hook<T>) -> Hook<T> {
        let saved = self.save();
        let result = call();
        if matches!(result, Err(Stop::Refuse(_))) {
            self.restore(saved);
        }
        result
    }

    // nested runs a call one level deeper, refusing one past the limit.
    fn nested<T>(
        &self,
        frame: Frame<'_>,
        index: usize,
        what: &str,
        call: impl FnOnce() -> Hook<T>,
    ) -> Hook<T> {
        let depth = self.depth.get();
        if depth >= MAX_CALL_DEPTH {
            return Err(self.defect_of(
                frame,
                index,
                format!("{what}: calls nest more than {MAX_CALL_DEPTH} deep"),
            ));
        }
        self.depth.set(depth + 1);
        let result = call();
        self.depth.set(depth);
        result
    }

    /// A call of an operation of a use of the calling use's row.
    pub fn call(
        &self,
        from: Frame<'_>,
        from_index: usize,
        writable: bool,
        use_id: &str,
        operation: &str,
        params: &Value,
    ) -> Hook<Value> {
        let (index, declared) = self.find_operation(from, use_id, operation)?;
        if declared.writes && !writable {
            return Err(self.defect_of(
                from,
                from_index,
                format!("a read-only operation cannot call {use_id}.{operation}, which writes"),
            ));
        }
        let caller = from.uses[from_index].spec.id();
        self.undoing(|| self.invoke(from, index, declared, params, Some(caller), None, writable))
    }

    /// A call of an operation of a use of another row, of any type: its
    /// parameter check, every guard of that row's type, its handler, and
    /// for a writing operation that row's after-change hooks, the row's
    /// update held to the version the host read it at.
    #[allow(clippy::too_many_arguments)]
    pub fn call_on(
        &self,
        from: Frame<'_>,
        from_index: usize,
        writable: bool,
        type_name: &str,
        key: &str,
        use_id: &str,
        operation: &str,
        params: &Value,
    ) -> Hook<Value> {
        if type_name.is_empty() || key.is_empty() {
            return Err(self.defect_of(
                from,
                from_index,
                "call_on takes a type's name and a row's key: non-empty strings",
            ));
        }
        if from.spec.name == type_name && from.key == Some(key) {
            return self.call(from, from_index, writable, use_id, operation, params);
        }
        let caller = from.uses[from_index].spec.id();
        let caller_type = from.spec.name.as_str();
        self.undoing(|| {
            self.on_type(type_name, |spec, uses| {
                let row = self.reach(type_name, key)?;
                let frame = Frame {
                    spec,
                    uses,
                    row,
                    key: Some(key),
                };
                let (index, declared) = self.find_operation(frame, use_id, operation)?;
                if declared.writes && !writable {
                    return Err(self.defect_of(
                        from,
                        from_index,
                        format!(
                            "a read-only operation cannot call {use_id}.{operation} of {type_name} {key}, which writes"
                        ),
                    ));
                }
                let result = self.invoke(
                    frame,
                    index,
                    declared,
                    params,
                    Some(caller),
                    Some(caller_type),
                    writable,
                )?;
                if declared.writes {
                    self.mark_updated(row);
                    self.after_change(
                        frame,
                        &Change::Operation {
                            use_id,
                            operation,
                            params,
                        },
                        operation,
                    )?;
                }
                Ok(result)
            })
        })
    }

    /// A create of a row of any type, by its key: its create parameters,
    /// its type's validation, guards, initializers and after-change hooks,
    /// as a caller's create runs them.
    #[allow(clippy::too_many_arguments)]
    pub fn create_row(
        &self,
        from: Frame<'_>,
        from_index: usize,
        writable: bool,
        type_name: &str,
        key: &str,
        own: &Map<String, Value>,
        params: &Map<String, Value>,
    ) -> Hook<()> {
        if !writable {
            return Err(self.defect_of(
                from,
                from_index,
                format!("create_row({type_name} {key}): a read-only operation writes nothing"),
            ));
        }
        if type_name.is_empty() || key.is_empty() {
            return Err(self.defect_of(
                from,
                from_index,
                "create_row takes a type's name and a row's key: non-empty strings",
            ));
        }
        if self.find_row(type_name, key).is_some() {
            return Err(
                Refusal::new(Code::Conflict, format!("{type_name} {key} already exists")).into(),
            );
        }
        self.nested(from, from_index, &format!("create of {type_name}"), || {
            self.undoing(|| {
                self.on_type(type_name, |spec, uses| {
                    let row = self.touch(Touched::created(type_name, Some(key), own));
                    let frame = Frame {
                        spec,
                        uses,
                        row,
                        key: Some(key),
                    };
                    self.create_on(frame, own, params)
                })
            })
        })
    }

    // on_type runs a call with a type's spec and its uses bound: the
    // command's own, or another type's, read and bound for the call.
    fn on_type<T>(
        &self,
        type_name: &str,
        call: impl FnOnce(&TypeSpec, &[Bound<'_>]) -> Hook<T>,
    ) -> Hook<T> {
        if type_name == self.top.spec.name {
            return call(self.top.spec, self.top.uses);
        }
        let Some(spec) = self.read_spec(type_name)? else {
            return Err(Refusal::not_found(format!("type {type_name} does not exist")).into());
        };
        let uses = Run::bind(self.core, spec)?;
        call(spec, &uses)
    }

    // touch adds a row the run reached and returns its place.
    fn touch(&self, touched: Touched) -> usize {
        let mut rows = self.rows.borrow_mut();
        rows.push(touched);
        rows.len() - 1
    }

    fn find_row(&self, type_name: &str, key: &str) -> Option<usize> {
        self.rows
            .borrow()
            .iter()
            .position(|row| row.type_name == type_name && row.key.as_deref() == Some(key))
    }

    // reach is the place of a row of any type: one the run reached, or one
    // it reads now. A row the type has none of, or the run deleted, is not
    // found.
    fn reach(&self, type_name: &str, key: &str) -> Hook<usize> {
        let gone =
            || -> Stop { Refusal::not_found(format!("{type_name} {key} does not exist")).into() };
        if let Some(at) = self.find_row(type_name, key) {
            if self.rows.borrow()[at].write == Write::Delete {
                return Err(gone());
            }
            return Ok(at);
        }
        let Some(row) = self.read_row(&ReadRequest::row(type_name, key))? else {
            return Err(gone());
        };
        Ok(self.touch(Touched::read(type_name, key, row)))
    }

    // mark_updated makes a row the run read an update with no own patch;
    // one it creates or updates already stays so.
    fn mark_updated(&self, row: usize) {
        let mut rows = self.rows.borrow_mut();
        if rows[row].write == Write::Read {
            rows[row].write = Write::Update(Map::new());
        }
    }

    // ---- the command's flow ----

    pub fn execute(&self) -> Hook<ChangeSet> {
        match &self.command.action {
            Action::Define => Ok(ChangeSet::default()),
            Action::Publish { from } => {
                self.publish(from.as_ref())?;
                Ok(ChangeSet::default())
            }
            Action::Create { id, own, params } => {
                self.touch(Touched::created(&self.top.spec.name, id.as_deref(), own));
                self.create_on(self.top, own, params)?;
                Ok(self.changes(Value::Null))
            }
            Action::Update { key, patch } => self.update(key, patch),
            Action::Delete { key } => self.delete(key),
            Action::Operation {
                key,
                use_id,
                operation,
                params,
            } => self.operation(key, use_id, operation, params),
            Action::Deadline {
                key,
                use_id,
                name,
                payload,
                ..
            } => self.deadline(key, use_id, name, payload),
        }
    }

    // create_on runs a create on a frame whose row the run just added: the
    // create parameters, validation, guards, initializers and after-change
    // hooks.
    fn create_on(
        &self,
        frame: Frame<'_>,
        own: &Map<String, Value>,
        params: &Map<String, Value>,
    ) -> Hook<()> {
        self.check_create_params(frame, params)?;
        self.validation(
            frame,
            &ValidationRequest::Create { own },
            anchored_writes(frame, own),
        )?;
        self.guards(frame, &GuardRequest::Create { own, params })?;
        let empty = Value::Object(Map::new());
        for (index, bound) in frame.uses.iter().enumerate() {
            let entry = params.get(bound.spec.id()).unwrap_or(&empty);
            bound
                .registered
                .behavior
                .initialize(self.scope(frame, index, "create", true), entry)?;
        }
        self.after_change(frame, &Change::Create, "create")
    }

    fn update(&self, key: &str, patch: &Map<String, Value>) -> Hook<ChangeSet> {
        let row = self.load(key)?;
        let after = merge_patch(&row.own, patch);
        self.validation(
            self.top,
            &ValidationRequest::Update {
                before: &row.own,
                after: &after,
            },
            anchored_writes(self.top, patch),
        )?;
        self.guards(
            self.top,
            &GuardRequest::Update {
                patch,
                after: &after,
            },
        )?;
        {
            let mut rows = self.rows.borrow_mut();
            rows[self.top.row].own = after;
            rows[self.top.row].write = Write::Update(patch.clone());
        }
        self.after_change(
            self.top,
            &Change::Update {
                patch,
                before: &row.own,
            },
            "update",
        )?;
        Ok(self.changes(Value::Null))
    }

    fn delete(&self, key: &str) -> Hook<ChangeSet> {
        self.load(key)?;
        self.guards(self.top, &GuardRequest::Delete)?;
        self.rows.borrow_mut()[self.top.row].write = Write::Delete;
        self.after_change(self.top, &Change::Delete, "delete")?;
        Ok(self.changes(Value::Null))
    }

    fn operation(
        &self,
        key: &str,
        use_id: &str,
        operation: &str,
        params: &Value,
    ) -> Hook<ChangeSet> {
        self.load(key)?;
        let (index, declared) = self.find_operation(self.top, use_id, operation)?;
        let result = self.invoke(self.top, index, declared, params, None, None, true)?;
        if declared.writes {
            self.mark_updated(self.top.row);
            self.after_change(
                self.top,
                &Change::Operation {
                    use_id,
                    operation,
                    params,
                },
                operation,
            )?;
        }
        Ok(self.changes(result))
    }

    fn deadline(&self, key: &str, use_id: &str, name: &str, payload: &Value) -> Hook<ChangeSet> {
        self.load(key)?;
        let Some((index, _)) = self.top.spec.use_of(use_id) else {
            return Err(self.no_use(self.top, use_id));
        };
        self.top.uses[index].registered.behavior.deadline(
            self.scope(self.top, index, name, true),
            name,
            payload,
        )?;
        self.mark_updated(self.top.row);
        self.after_change(self.top, &Change::Deadline { use_id, name }, name)?;
        Ok(self.changes(Value::Null))
    }

    // changes is the run's change set: each row it writes, in the order it
    // reached them, the command's own first, and the rest it built.
    fn changes(&self, result: Value) -> ChangeSet {
        let rows = self.rows.take();
        let pending = self.pending.take();
        ChangeSet {
            rows: rows.into_iter().filter_map(Touched::into_write).collect(),
            sub_rows: pending.sub_rows,
            events: pending.events,
            deadlines: pending.deadlines,
            result,
        }
    }

    /// The publish check: each use's config change from the published
    /// version, by use id. A use whose id the published version gives
    /// another behavior is a removal and an addition.
    fn publish(&self, from: Option<&TypeSpec>) -> Hook<()> {
        let Some(before) = from else {
            return Ok(());
        };
        let type_name = &self.top.spec.name;
        let same = |a: &UseSpec, b: &UseSpec| a.id() == b.id() && a.behavior == b.behavior;
        let mut changes = Vec::new();
        for (index, entry) in self.top.spec.uses.iter().enumerate() {
            let path = format!("{type_name}.behaviors.{}", entry.id());
            let bound = &self.top.uses[index];
            let cx = ConfigCx {
                run: self,
                use_id: entry.id(),
                behavior: &entry.behavior,
            };
            match before
                .uses
                .iter()
                .enumerate()
                .find(|(_, earlier)| same(earlier, entry))
            {
                Some((at, earlier)) => {
                    let (was, now) = (earlier.config_or_empty(), entry.config_or_empty());
                    if was == now {
                        continue;
                    }
                    let subject = match &entry.anchor {
                        Anchor::Type => format!("behavior {} on type {type_name}", entry.behavior),
                        Anchor::Field(field) => format!(
                            "behavior {} on field {field} of type {type_name}",
                            entry.behavior
                        ),
                    };
                    let reason = match parse_use(bound.registered, before, at) {
                        Parsed::Config(old) => self.reason(
                            index,
                            bound.registered.behavior.config_change(
                                &cx,
                                Some(&*old),
                                Some(&*bound.config),
                            )?,
                        )?,
                        refused => Some(refused.problem(&entry.behavior)),
                    };
                    if let Some(reason) = reason {
                        changes.push(SchemaChange::new(
                            path,
                            format!(
                                "{subject} cannot change its config from {} to {}: {reason}",
                                stringify(&canonical(&was)),
                                stringify(&canonical(&now))
                            ),
                        ));
                    }
                }
                None => {
                    if !self.has_rows(type_name)? {
                        continue;
                    }
                    let reason = self.reason(
                        index,
                        bound
                            .registered
                            .behavior
                            .config_change(&cx, None, Some(&*bound.config))?,
                    )?;
                    if let Some(reason) = reason {
                        changes.push(SchemaChange::new(
                            path,
                            format!(
                                "{} cannot be added to type {type_name}, which has instances: {reason}",
                                named(entry)
                            ),
                        ));
                    }
                }
            }
        }
        for (at, earlier) in before.uses.iter().enumerate() {
            if self.top.spec.uses.iter().any(|entry| same(earlier, entry)) {
                continue;
            }
            if !self.has_rows(type_name)? {
                continue;
            }
            let path = format!("{type_name}.behaviors.{}", earlier.id());
            let reason = match self.core.find(&earlier.behavior) {
                None => Some("no implementation is registered to allow it".to_owned()),
                Some(registered) => match parse_use(registered, before, at) {
                    Parsed::Config(old) => {
                        let cx = ConfigCx {
                            run: self,
                            use_id: earlier.id(),
                            behavior: &earlier.behavior,
                        };
                        let answer = registered.behavior.config_change(&cx, Some(&*old), None)?;
                        match answer {
                            Some(reason) if reason.is_empty() => {
                                return Err(Refusal::defect(&earlier.behavior, EMPTY_REASON).into())
                            }
                            other => other,
                        }
                    }
                    refused => Some(refused.problem(&earlier.behavior)),
                },
            };
            if let Some(reason) = reason {
                changes.push(SchemaChange::new(
                    path,
                    format!(
                        "{} cannot be removed from type {type_name}, which has instances: {reason}",
                        named(earlier)
                    ),
                ));
            }
        }
        if changes.is_empty() {
            Ok(())
        } else {
            Err(Refusal::incompatible_change(type_name, changes).into())
        }
    }

    // reason holds a config change's answer to a non-empty reason.
    fn reason(&self, index: usize, answer: Option<String>) -> Hook<Option<String>> {
        match answer {
            Some(reason) if reason.is_empty() => Err(self.defect_of(self.top, index, EMPTY_REASON)),
            other => Ok(other),
        }
    }

    // ---- the parts every flow composes ----

    fn scope<'s>(
        &'s self,
        frame: Frame<'s>,
        index: usize,
        action: &'s str,
        writable: bool,
    ) -> Scope<'s> {
        Scope {
            run: self,
            frame,
            index,
            action,
            writable,
        }
    }

    // load reads the command's row, refusing a key the type has none of,
    // and makes it the first row the run reached.
    fn load(&self, key: &str) -> Hook<Row> {
        let type_name = &self.top.spec.name;
        let Some(row) = self.read_row(&ReadRequest::row(type_name, key))? else {
            return Err(Refusal::not_found(format!("{type_name} {key} does not exist")).into());
        };
        self.touch(Touched::read(type_name, key, row));
        Ok(row.clone())
    }

    fn no_use(&self, frame: Frame<'_>, use_id: &str) -> Stop {
        let ids: Vec<String> = frame.spec.uses.iter().map(|u| u.id().to_owned()).collect();
        Refusal::not_found(format!(
            "{} has no behavior use {use_id} (its uses: {})",
            frame.spec.name,
            listed(&ids)
        ))
        .into()
    }

    fn find_operation<'f>(
        &self,
        frame: Frame<'f>,
        use_id: &str,
        operation: &str,
    ) -> Hook<(usize, &'f OperationDecl)> {
        let Some((index, _)) = frame.spec.use_of(use_id) else {
            return Err(self.no_use(frame, use_id));
        };
        let declaration = self.declaration(frame, index);
        let Some(declared) = declaration.find_operation(operation) else {
            let names: Vec<String> = declaration
                .operations
                .iter()
                .map(|op| op.name.clone())
                .collect();
            return Err(Refusal::not_found(format!(
                "{} has no operation {operation} of {use_id} (its operations: {})",
                frame.spec.name,
                listed(&names)
            ))
            .into());
        };
        Ok((index, declared))
    }

    // check_create_params holds a create's parameters to the type's uses:
    // an entry names a use whose behavior takes create parameters, and each
    // such use's entry, `{}` when the create gives none, passes its check.
    fn check_create_params(&self, frame: Frame<'_>, params: &Map<String, Value>) -> Hook<()> {
        let mut issues = Vec::new();
        let taking: Vec<String> = frame
            .uses
            .iter()
            .filter(|bound| bound.registered.declaration.create_params)
            .map(|bound| bound.spec.id().to_owned())
            .collect();
        for id in params.keys() {
            if frame.spec.use_of(id).is_none() {
                issues.push(SchemaIssue::new(
                    pointer(id),
                    format!(
                        "{} has no behavior use {id} (its uses that take create parameters: {})",
                        frame.spec.name,
                        listed(&taking)
                    ),
                ));
            }
        }
        let empty = Value::Object(Map::new());
        for (index, bound) in frame.uses.iter().enumerate() {
            let id = bound.spec.id();
            if !bound.registered.declaration.create_params {
                if params.contains_key(id) {
                    issues.push(SchemaIssue::new(
                        pointer(id),
                        format!(
                            "behavior {} takes no create parameters",
                            bound.spec.behavior
                        ),
                    ));
                }
                continue;
            }
            let entry = params.get(id).unwrap_or(&empty);
            let found = bound
                .registered
                .behavior
                .check_create_params(&*bound.config, entry)
                .ok_or_else(|| self.defect_of(frame, index, CONFIG_OF_ANOTHER_TYPE))?;
            issues.extend(found.into_iter().map(|issue| {
                SchemaIssue::new(format!("{}{}", pointer(id), issue.path), issue.message)
            }));
        }
        if issues.is_empty() {
            Ok(())
        } else {
            Err(Refusal::create_params(&frame.spec.name, issues).into())
        }
    }

    // validation asks every use to judge the own fields a write would
    // store, after the issues the core finds itself, and refuses with
    // them all together.
    fn validation(
        &self,
        frame: Frame<'_>,
        request: &ValidationRequest<'_>,
        mut issues: Vec<ValidationIssue>,
    ) -> Hook<()> {
        let action = match request {
            ValidationRequest::Create { .. } => "create",
            ValidationRequest::Update { .. } => "update",
        };
        for (index, bound) in frame.uses.iter().enumerate() {
            issues.extend(
                bound
                    .registered
                    .behavior
                    .validate(self.scope(frame, index, action, false), request)?,
            );
        }
        if issues.is_empty() {
            Ok(())
        } else {
            Err(Refusal::invalid_instance(&frame.spec.name, issues).into())
        }
    }

    /// Asks every use's guard in list order; the first veto refuses.
    fn guards(&self, frame: Frame<'_>, request: &GuardRequest<'_>) -> Hook<()> {
        let action = request.action();
        for (index, bound) in frame.uses.iter().enumerate() {
            let answer = bound
                .registered
                .behavior
                .guard(self.scope(frame, index, action, false), request)?;
            if let Some(veto) = answer {
                return Err(self.veto_refusal(frame, index, action, veto));
            }
        }
        Ok(())
    }

    fn after_change(&self, frame: Frame<'_>, change: &Change<'_>, action: &str) -> Hook<()> {
        for (index, bound) in frame.uses.iter().enumerate() {
            bound
                .registered
                .behavior
                .after_change(self.scope(frame, index, action, true), change)?;
        }
        Ok(())
    }

    /// Runs an operation: its parameter check, every guard, its handler.
    /// `caller` names the use of a `call()` or a `call_on()`, and
    /// `caller_type` the calling use's type for a `call_on()`.
    #[allow(clippy::too_many_arguments)]
    fn invoke(
        &self,
        frame: Frame<'_>,
        index: usize,
        declared: &OperationDecl,
        params: &Value,
        caller: Option<&str>,
        caller_type: Option<&str>,
        writable: bool,
    ) -> Hook<Value> {
        self.nested(
            frame,
            index,
            &format!("operation {}", declared.name),
            || {
                let bound = &frame.uses[index];
                let behavior = &bound.spec.behavior;
                let issues = bound
                    .registered
                    .behavior
                    .check_params(&*bound.config, &declared.name, params)
                    .ok_or_else(|| self.defect_of(frame, index, CONFIG_OF_ANOTHER_TYPE))?;
                if !issues.is_empty() {
                    return Err(Refusal::operation_params(
                        behavior,
                        bound.spec.id(),
                        &declared.name,
                        issues,
                    )
                    .into());
                }
                self.guards(
                    frame,
                    &GuardRequest::Operation {
                        use_id: bound.spec.id(),
                        behavior,
                        operation: &declared.name,
                        params,
                        writes: declared.writes,
                        caller,
                        caller_type,
                    },
                )?;
                bound.registered.behavior.operation(
                    self.scope(frame, index, &declared.name, declared.writes && writable),
                    &declared.name,
                    params,
                )
            },
        )
    }
}

// anchored_writes is the issues of own fields a write names that are a field
// anchor's: the field is the behavior's value, which only its operations
// change, and in a host's layout it is the column the behavior writes, so
// an own member of the name would write it past them. A null in a patch is
// refused too, since it would clear the column.
fn anchored_writes(frame: Frame<'_>, fields: &Map<String, Value>) -> Vec<ValidationIssue> {
    frame
        .spec
        .uses
        .iter()
        .filter_map(|entry| Some((entry.field()?, &entry.behavior)))
        .filter(|(field, _)| fields.contains_key(*field))
        .map(|(field, behavior)| {
            ValidationIssue::new(
                field,
                "owned",
                format!("{field} is behavior {behavior}'s field: only its operations change it"),
            )
        })
        .collect()
}

const EMPTY_REASON: &str =
    "config_change returns a reason (a non-empty string) to refuse, or None to allow";

/// The issue of a use that gives no config to a behavior that needs one,
/// in the schema validator's words, at the use's entry.
const MISSING_CONFIG: &str = "must have required property 'config'";

/// A parameter check handed a config its behavior did not parse: a defect
/// of the core's, refused as the behavior's, as every other hook does.
const CONFIG_OF_ANOTHER_TYPE: &str = "the core holds a config of another type for this use";

/// What a use's config came to.
pub(crate) enum Parsed {
    Config(AnyConfig),
    /// The use gives no config, and its behavior refuses `{}` for its
    /// shape: it needs one.
    Missing,
    Refused(ConfigError),
}

impl Parsed {
    // problem is why a config that did not parse is refused, as a publish
    // refusal names it.
    fn problem(&self, behavior: &str) -> String {
        match self {
            Parsed::Config(_) => String::new(),
            Parsed::Missing => format!("behavior {behavior}: {MISSING_CONFIG}"),
            Parsed::Refused(error) => format!("behavior {behavior} config: {error}"),
        }
    }
}

/// Parses the config of the use at a position of a type's list: the one
/// it gives, or `{}` when it gives none, which a behavior that refuses its
/// shape needs a config for.
pub(crate) fn parse_use(registered: &Registered, spec: &TypeSpec, index: usize) -> Parsed {
    let target = ConfigTarget::new(spec, index);
    let given = spec.uses.get(index).and_then(|entry| entry.config.as_ref());
    let parsed = match given {
        Some(raw) => registered.behavior.parse(raw, &target),
        None => registered
            .behavior
            .parse(&Value::Object(Map::new()), &target),
    };
    match parsed {
        Ok(config) => Parsed::Config(config),
        Err(ConfigError::Schema(_)) if given.is_none() => Parsed::Missing,
        Err(error) => Parsed::Refused(error),
    }
}

// canonical is a value with every object's members sorted by name, as a
// publish refusal prints a config: the same config prints the same however
// the host ordered it.
fn canonical(value: &Value) -> Value {
    match value {
        Value::Object(members) => {
            let mut sorted: Vec<(&String, &Value)> = members.iter().collect();
            sorted.sort_by(|a, b| a.0.cmp(b.0));
            Value::Object(
                sorted
                    .into_iter()
                    .map(|(key, member)| (key.clone(), canonical(member)))
                    .collect(),
            )
        }
        Value::Array(items) => Value::Array(items.iter().map(canonical).collect()),
        _ => value.clone(),
    }
}

// subject_prefix names where a config sits, for a define issue: the type,
// and the field for a field anchor.
fn subject_prefix(spec: &TypeSpec, entry: &UseSpec) -> String {
    match &entry.anchor {
        Anchor::Type => format!("type {}: ", spec.name),
        Anchor::Field(field) => format!("type {}: field {field}: ", spec.name),
    }
}

// named names a use in a publish refusal: its behavior, and its field.
fn named(entry: &UseSpec) -> String {
    match &entry.anchor {
        Anchor::Type => format!("behavior {}", entry.behavior),
        Anchor::Field(field) => format!("behavior {} on field {field}", entry.behavior),
    }
}

// mismatched refuses an answer of another kind than its request asks for:
// the host's mistake, not a behavior's.
fn mismatched(request: &ReadRequest) -> Stop {
    Refusal::invalid_argument(format!(
        "the host answered {} with an answer of another kind",
        stringify(&serde_json::to_value(request).unwrap_or(Value::Null))
    ))
    .into()
}

fn stringify(value: &Value) -> String {
    serde_json::to_string(value).unwrap_or_default()
}

fn quoted(text: &str) -> String {
    serde_json::to_string(text).unwrap_or_default()
}

// pointer is a JSON pointer to one member (RFC 6901).
fn pointer(member: &str) -> String {
    format!("/{}", member.replace('~', "~0").replace('/', "~1"))
}
