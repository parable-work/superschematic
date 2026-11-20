//! The pure request handler (D103's step 6, D117's development mode): a
//! request and its caller in; the reads it asks of the development layout,
//! a response and the unit that applies its changes out. It opens no
//! connection and no file, reads no clock and makes no id: the host gives
//! it the time, the request's id and a fresh UUID for a create that names
//! no key ([`Context`]), answers the reads it asks ([`StoreRead`]), and
//! applies the unit it returns in the request's transaction ([`Done`]).
//!
//! A request is a define, a publish, a describe, a get, a create, an
//! update, a delete, a list, a behavior's operation of a row or of the
//! table, or what a host's runner hands it: a due deadline and a write its
//! change queue holds.

use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Caller, Code, Command, Core, Outcome, Refusal, RowChange, RowWrite, Step, TypeSpec,
    ValidationIssue,
};

use crate::bridge::{self, row_of, rows_of, Bridge, Ran};
use crate::declarations::Declarations;
use crate::document::{self, document_refusal, Kind, Model, OnDelete};
use crate::filter::{self, Sort};
use crate::store::{
    Draft, StoreAnswer, StoreAnswers, StoreRead, StoreWrite, StoredRow, Unit, Version,
};
use crate::validate::{self, Mode};
use crate::values::{base62, uuid_of};

/// How long a fire a guard vetoed waits before it falls due again (D103's
/// release): a minute.
pub const VETOED_RETRY_MS: i64 = 60_000;

/// A list's page size when it names none, and the largest it may name.
pub const DEFAULT_LIMIT: usize = 50;
pub const MAX_LIMIT: usize = 1000;

/// What a request asks.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum Request {
    /// Defines a type's next version from a schema document: its draft,
    /// which replaces one defined before.
    Define { document: Value },
    /// Publishes a type's draft as its next version. With no compatibility
    /// rule (D117): it may rename, retype, remove and tighten. Stored rows
    /// the version no longer describes are refused, each with what it
    /// fails, unless `discard` says to delete them.
    Publish {
        #[serde(rename = "type")]
        type_name: String,
        #[serde(default)]
        discard: bool,
    },
    /// A type's live version as development mode runs it.
    Describe {
        #[serde(rename = "type")]
        type_name: String,
    },
    Get {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
    },
    /// A new row: its own fields, its key among them or left to the host,
    /// and each use's create parameters by use id.
    Create {
        #[serde(rename = "type")]
        type_name: String,
        #[serde(default)]
        data: Map<String, Value>,
        #[serde(default)]
        params: Map<String, Value>,
    },
    /// A merge patch (RFC 7396) of a row's own fields.
    Update {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        patch: Map<String, Value>,
        #[serde(default)]
        preconditions: Map<String, Value>,
    },
    Delete {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        #[serde(default)]
        preconditions: Map<String, Value>,
    },
    /// A page of a type's rows a filter keeps, in a sort's order.
    List {
        #[serde(rename = "type")]
        type_name: String,
        #[serde(default)]
        filter: Value,
        #[serde(default)]
        sort: Vec<Sort>,
        #[serde(default)]
        limit: Option<usize>,
        #[serde(default)]
        cursor: Option<String>,
    },
    /// A use's operation of a row.
    Operation {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        #[serde(rename = "use")]
        use_id: String,
        operation: String,
        #[serde(default)]
        params: Value,
        #[serde(default)]
        preconditions: Map<String, Value>,
    },
    /// A use's operation of the table as a whole (D104).
    TableOperation {
        #[serde(rename = "type")]
        type_name: String,
        #[serde(rename = "use")]
        use_id: String,
        operation: String,
        #[serde(default)]
        params: Value,
    },
    /// A deadline that fell due, as the host's runner hands it: the time it
    /// was armed to fall due at and its payload.
    Deadline {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        #[serde(rename = "use")]
        use_id: String,
        name: String,
        at: i64,
        #[serde(default)]
        payload: Value,
    },
    /// A write of a row the change queue holds (D103), as the host's runner
    /// hands it, with the number the queue gave it, which the unit spends.
    Change {
        #[serde(rename = "type")]
        type_name: String,
        id: String,
        change: RowChange,
        #[serde(default)]
        seq: Option<i64>,
    },
}

/// What a request runs with beside itself: its caller, the time in epoch
/// milliseconds, the host's id for it (kept for every run of it), and a
/// version 4 UUID the host drew, the key of a create that names none.
#[derive(Clone, Debug, PartialEq)]
pub struct Context {
    pub caller: Caller,
    pub now: i64,
    pub request: String,
    pub id: u128,
}

/// What a run of a request came to.
#[derive(Clone, Debug, PartialEq)]
pub enum HandlerStep {
    /// Reads the host answers before it runs the request again.
    Read(Vec<StoreRead>),
    /// The request's response and the unit the host applies.
    Done(Done),
}

/// A request's response, and the unit that applies its changes. A refused
/// request's unit is empty but for a deadline's: one whose row or use is
/// gone spends its arming, and one a guard vetoed is held back a minute.
#[derive(Clone, Debug, PartialEq)]
pub struct Done {
    pub response: Result<Value, Refusal>,
    pub unit: Unit,
}

/// The handler: the core it runs the behaviors on, and the behaviors'
/// declarations.
pub struct Handler<'a> {
    core: &'a Core,
    declarations: &'a Declarations,
}

/// The reads a run has and the ones it still needs.
pub(crate) struct Reads<'a> {
    pub answers: &'a StoreAnswers,
    pub need: Vec<StoreRead>,
}

impl<'a> Reads<'a> {
    fn ask(&mut self, read: StoreRead) -> Option<&'a StoreAnswer> {
        let answer = self.answers.get(&read);
        if answer.is_none() && !self.need.contains(&read) {
            self.need.push(read);
        }
        answer
    }

    pub fn live(&mut self) -> Option<&'a [Version]> {
        match self.ask(StoreRead::Live)? {
            StoreAnswer::Live(versions) => Some(versions),
            _ => None,
        }
    }

    pub fn draft(&mut self, type_name: &str) -> Option<Option<&'a Draft>> {
        match self.ask(StoreRead::Draft {
            type_name: type_name.to_owned(),
        })? {
            StoreAnswer::Draft(draft) => Some(draft.as_ref()),
            _ => None,
        }
    }

    pub fn row(&mut self, type_name: &str, id: &str) -> Option<Option<&'a StoredRow>> {
        if let Some(found) = row_of(self.answers, type_name, id) {
            return Some(found);
        }
        self.ask(StoreRead::Row {
            type_name: type_name.to_owned(),
            id: id.to_owned(),
        });
        None
    }

    pub fn rows(&mut self, type_name: &str) -> Option<&'a [StoredRow]> {
        if let Some(rows) = rows_of(self.answers, type_name) {
            return Some(rows);
        }
        self.ask(StoreRead::Rows {
            type_name: type_name.to_owned(),
        });
        None
    }

    pub fn waiting(&self) -> bool {
        !self.need.is_empty()
    }

    pub fn wait(self) -> HandlerStep {
        HandlerStep::Read(self.need)
    }
}

/// Every type's live version, read: its model, its number, its spec, and
/// the types whose writes are heard.
#[derive(Clone, Debug, Default)]
pub(crate) struct Live {
    pub models: BTreeMap<String, Model>,
    pub versions: BTreeMap<String, i64>,
    pub specs: BTreeMap<String, TypeSpec>,
    pub hearing: BTreeSet<String>,
}

impl Live {
    pub fn model(&self, type_name: &str) -> Result<&Model, Refusal> {
        self.models
            .get(type_name)
            .ok_or_else(|| Refusal::not_found(format!("type {type_name} has no published version")))
    }

    /// Puts a model in, its relations' keys resolved and the hearing types
    /// read again.
    pub fn install(&mut self, core: &Core, model: Model) {
        self.models.insert(model.name.clone(), model);
        self.resolve(core);
    }

    // resolve gives each relation's member its target's key type, and reads
    // the specs and the hearing types again.
    fn resolve(&mut self, core: &Core) {
        let keys: BTreeMap<String, (String, Kind)> = self
            .models
            .iter()
            .map(|(name, model)| {
                (
                    name.clone(),
                    (model.key.type_name.clone(), model.key.kind.clone()),
                )
            })
            .collect();
        for model in self.models.values_mut() {
            for field in &mut model.fields {
                let Some(relation) = &field.relation else {
                    continue;
                };
                if let Some((type_name, kind)) = keys.get(&relation.target) {
                    field.type_name.clone_from(type_name);
                    field.kind = kind.clone();
                }
            }
        }
        self.specs = self
            .models
            .iter()
            .map(|(name, model)| (name.clone(), model.spec()))
            .collect();
        self.hearing = bridge::hearing(core, &self.specs);
    }

    /// The types whose own fields hold a relation to a type, with each such
    /// field's member and what its delete does.
    pub fn referrers(&self, target: &str) -> Vec<(String, String, OnDelete)> {
        let mut out = Vec::new();
        for (name, model) in &self.models {
            for field in &model.fields {
                if let Some(relation) = &field.relation {
                    if relation.target == target {
                        out.push((name.clone(), field.name.clone(), relation.on_delete));
                    }
                }
            }
        }
        out
    }
}

/// Whether a key a relation's member holds names a row's id: a UUID by its
/// value in either form, anything else as text.
pub(crate) fn names_row(held: &Value, id: &str) -> bool {
    let Some(held) = held.as_str() else {
        return false;
    };
    if held == id {
        return true;
    }
    matches!((uuid_of(held), uuid_of(id)), (Some(a), Some(b)) if a == b)
}

fn refused(refusal: Refusal) -> HandlerStep {
    HandlerStep::Done(Done {
        response: Err(refusal),
        unit: Unit::default(),
    })
}

fn done(response: Value, writes: Vec<StoreWrite>) -> HandlerStep {
    HandlerStep::Done(Done {
        response: Ok(response),
        unit: Unit { writes },
    })
}

/// What a delete reaches through the relations that point at it, as a
/// table's foreign keys answer it: the rows CASCADE deletes and the rows
/// SET NULL clears, each as it stands after; a RESTRICT refuses the delete.
#[derive(Clone, Debug, Default)]
pub(crate) struct Cascade {
    pub deleted: Vec<(String, StoredRow)>,
    pub nulled: Vec<(String, StoredRow, StoredRow)>,
}

impl Handler<'_> {
    pub(crate) fn declarations(&self) -> &Declarations {
        self.declarations
    }

    pub(crate) fn core(&self) -> &Core {
        self.core
    }
}

impl<'a> Handler<'a> {
    /// A handler over a core and the declarations of the behaviors it runs.
    pub fn new(core: &'a Core, declarations: &'a Declarations) -> Self {
        Handler { core, declarations }
    }

    /// Runs a request over the store's answers so far: the reads it needs,
    /// or its response and the unit to apply. A pure function of its
    /// inputs, so a host resumes a request by running it again with more
    /// answers.
    pub fn run(&self, request: &Request, context: &Context, answers: &StoreAnswers) -> HandlerStep {
        let mut reads = Reads {
            answers,
            need: Vec::new(),
        };
        let outcome = match request {
            Request::Define { document } => self.define(document, context),
            Request::Publish { type_name, discard } => {
                crate::publish::publish(self, context, type_name, *discard, &mut reads)
            }
            Request::Describe { type_name } => self.describe(type_name, &mut reads),
            Request::Get { type_name, id } => self.get(type_name, id, &mut reads),
            Request::Create {
                type_name,
                data,
                params,
            } => self.create(type_name, data, params, context, &mut reads),
            Request::Update {
                type_name,
                id,
                patch,
                preconditions,
            } => self.update(type_name, id, patch, preconditions, context, &mut reads),
            Request::Delete {
                type_name,
                id,
                preconditions,
            } => self.delete(type_name, id, preconditions, context, &mut reads),
            Request::List {
                type_name,
                filter,
                sort,
                limit,
                cursor,
            } => self.list(
                type_name,
                filter,
                sort,
                *limit,
                cursor.as_deref(),
                &mut reads,
            ),
            Request::Operation {
                type_name,
                id,
                use_id,
                operation,
                params,
                preconditions,
            } => self.command(
                type_name,
                Action::Operation {
                    key: id.clone(),
                    use_id: use_id.clone(),
                    operation: operation.clone(),
                    params: object_or_empty(params),
                },
                preconditions,
                context,
                &mut reads,
            ),
            Request::TableOperation {
                type_name,
                use_id,
                operation,
                params,
            } => self.command(
                type_name,
                Action::TableOperation {
                    use_id: use_id.clone(),
                    operation: operation.clone(),
                    params: object_or_empty(params),
                },
                &Map::new(),
                context,
                &mut reads,
            ),
            Request::Deadline {
                type_name,
                id,
                use_id,
                name,
                at,
                payload,
            } => self.deadline(
                type_name, id, use_id, name, *at, payload, context, &mut reads,
            ),
            Request::Change {
                type_name,
                id,
                change,
                seq,
            } => self.change(type_name, id, change, *seq, context, &mut reads),
        };
        outcome.unwrap_or_else(refused)
    }

    /// Every type's live version, read; `Ok(None)` while the store has not
    /// answered.
    pub(crate) fn live(&self, reads: &mut Reads<'_>) -> Result<Option<Live>, Refusal> {
        let Some(versions) = reads.live() else {
            return Ok(None);
        };
        let mut live = Live::default();
        for version in versions {
            let model =
                document::read(&version.document, self.declarations).map_err(|refusal| {
                    Refusal::internal(format!(
                        "the stored version {} of {} no longer reads: {}",
                        version.version, version.type_name, refusal.message
                    ))
                })?;
            live.versions
                .insert(version.type_name.clone(), version.version);
            live.models.insert(version.type_name.clone(), model);
        }
        live.resolve(self.core);
        Ok(Some(live))
    }

    /// The core's define check of a type's uses and configs.
    pub(crate) fn define_check(&self, model: &Model, context: &Context) -> Result<(), Refusal> {
        let command = Command::new(context.caller.clone(), context.now, Action::Define);
        match self.core.run(
            &model.spec(),
            &command,
            &superschematic_rules::Answers::new(),
        ) {
            Step::Done(Outcome::Refused(refusal)) => Err(refusal),
            Step::Done(Outcome::Changed(_)) => Ok(()),
            other => Err(Refusal::internal(format!(
                "the define check of {} asked for {other:?}",
                model.name
            ))),
        }
    }

    fn define(&self, document: &Value, context: &Context) -> Result<HandlerStep, Refusal> {
        let model = document::read(document, self.declarations)?;
        self.define_check(&model, context)?;
        Ok(done(
            json!({"type": model.name, "defined": true}),
            vec![StoreWrite::PutDraft {
                type_name: model.name.clone(),
                document: document.clone(),
            }],
        ))
    }

    fn describe(&self, type_name: &str, reads: &mut Reads<'_>) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        let version = live.versions.get(type_name).copied().unwrap_or_default();
        Ok(done(
            crate::describe::describe(model, version, self.declarations),
            Vec::new(),
        ))
    }

    fn get(
        &self,
        type_name: &str,
        id: &str,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let live = self.live(reads)?;
        let row = reads.row(type_name, id);
        let (Some(live), Some(row)) = (live, row) else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        let row = row.ok_or_else(|| not_found(type_name, id))?;
        Ok(done(filter::render(model, row), Vec::new()))
    }

    /// The key a create writes: the one its data names, in its stored form,
    /// or one the host drew when the key is the host's to make.
    fn key_of(
        model: &Model,
        data: &Map<String, Value>,
        context: &Context,
    ) -> Result<String, ValidationIssue> {
        let path = format!("/{}", model.key.name);
        match data.get(&model.key.name).filter(|value| !value.is_null()) {
            Some(Value::String(given)) if model.key.kind == Kind::Uuid => {
                uuid_of(given).map(base62).ok_or_else(|| {
                    ValidationIssue::new(
                        &path,
                        "format",
                        format!(
                            "{} must be a UUID, hyphenated or in its base62 form",
                            model.key.name
                        ),
                    )
                })
            }
            Some(Value::String(given)) if !given.is_empty() => Ok(given.clone()),
            Some(_) => Err(ValidationIssue::new(
                &path,
                "type",
                format!("{} must be a string", model.key.name),
            )),
            None if model.key.auto => Ok(base62(context.id)),
            None if model.key.kind == Kind::Text => Ok(hyphenated(context.id)),
            None => Err(ValidationIssue::new(
                &path,
                "required",
                format!("{} is required", model.key.name),
            )),
        }
    }

    /// The issues a write's relations have: each member that names a row of
    /// its target that does not exist, as a foreign key refuses it. Asks
    /// for each target row it needs.
    fn relation_issues(
        model: &Model,
        own: &Map<String, Value>,
        reads: &mut Reads<'_>,
    ) -> Vec<ValidationIssue> {
        let mut issues = Vec::new();
        for field in &model.fields {
            let (Some(relation), Some(Value::String(key))) =
                (&field.relation, own.get(&field.name))
            else {
                continue;
            };
            if let Some(None) = reads.row(&relation.target, key) {
                issues.push(ValidationIssue::new(
                    format!("/{}", field.name),
                    "relation",
                    format!(
                        "{} points at {} {key}, which does not exist",
                        relation.name, relation.target
                    ),
                ));
            }
        }
        issues
    }

    /// The refusal of a write whose unique fields another row holds, as a
    /// unique index refuses it: `conflict`.
    fn unique_conflict(
        model: &Model,
        id: &str,
        data: &Map<String, Value>,
        rows: &[StoredRow],
    ) -> Option<Refusal> {
        for unique in &model.uniques {
            let values: Option<Vec<&Value>> = unique
                .iter()
                .map(|name| data.get(name).filter(|value| !value.is_null()))
                .collect();
            // A unique key with a field of no value holds no row's, as
            // NULLs are distinct to a unique index.
            let Some(values) = values else {
                continue;
            };
            let holder = rows.iter().find(|row| {
                row.id != id
                    && unique.iter().zip(&values).all(|(name, value)| {
                        row.data
                            .get(name)
                            .is_some_and(|held| crate::values::same(held, value))
                    })
            });
            if let Some(holder) = holder {
                let shown: Vec<String> = unique
                    .iter()
                    .zip(&values)
                    .map(|(name, value)| format!("{name} {value}"))
                    .collect();
                let mut details = Map::new();
                details.insert("fields".into(), json!(unique));
                details.insert("holder".into(), Value::String(holder.id.clone()));
                return Some(Refusal {
                    details: Some(Box::new(details)),
                    ..Refusal::new(
                        Code::Conflict,
                        format!(
                            "{} {} holds {}, which is unique",
                            model.name,
                            holder.id,
                            shown.join(" and ")
                        ),
                    )
                });
            }
        }
        None
    }

    fn create(
        &self,
        type_name: &str,
        data: &Map<String, Value>,
        params: &Map<String, Value>,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        let mut own_data = data.clone();
        let key = Self::key_of(model, data, context);
        own_data.shift_remove(&model.key.name);
        let (own, mut issues) = validate::own_fields(model, &own_data, Mode::Create);
        let id = match key {
            Ok(id) => id,
            Err(issue) => {
                issues.insert(0, issue);
                String::new()
            }
        };
        if !issues.is_empty() {
            return Err(Refusal::invalid_instance(type_name, issues));
        }
        let relation = Self::relation_issues(model, &own, reads);
        let held = reads.row(type_name, &id);
        let rows = if model.uniques.is_empty() {
            Some(&[][..])
        } else {
            reads.rows(type_name)
        };
        if reads.waiting() {
            return Ok(take(reads).wait());
        }
        if !relation.is_empty() {
            return Err(Refusal::invalid_instance(type_name, relation));
        }
        if let Some(Some(_)) = held {
            return Err(Refusal::new(
                Code::Conflict,
                format!("{type_name} {id} already exists"),
            ));
        }
        if let Some(conflict) = Self::unique_conflict(model, &id, &own, rows.unwrap_or_default()) {
            return Err(conflict);
        }
        let action = Action::Create {
            id: Some(id.clone()),
            own,
            params: params.clone(),
        };
        self.run_core(
            &live,
            model,
            action,
            &Map::new(),
            context,
            reads,
            |changed| render_written(model, &changed.rows, &id),
        )
    }

    fn update(
        &self,
        type_name: &str,
        id: &str,
        patch: &Map<String, Value>,
        preconditions: &Map<String, Value>,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let live = self.live(reads)?;
        let row = reads.row(type_name, id);
        let (Some(live), Some(row)) = (live, row) else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        let row = row.ok_or_else(|| not_found(type_name, id))?;
        let validate::Patched {
            patch,
            fields: merged,
        } = validate::patched(model, &row.data, patch)
            .map_err(|issues| Refusal::invalid_instance(type_name, issues))?;
        let relation = Self::relation_issues(model, &patch, reads);
        let touches_unique = model
            .uniques
            .iter()
            .any(|unique| unique.iter().any(|name| patch.contains_key(name)));
        let rows = if touches_unique {
            reads.rows(type_name)
        } else {
            Some(&[][..])
        };
        if reads.waiting() {
            return Ok(take(reads).wait());
        }
        if !relation.is_empty() {
            return Err(Refusal::invalid_instance(type_name, relation));
        }
        if let Some(conflict) = Self::unique_conflict(model, id, &merged, rows.unwrap_or_default())
        {
            return Err(conflict);
        }
        let action = Action::Update {
            key: id.to_owned(),
            patch,
        };
        self.run_core(
            &live,
            model,
            action,
            preconditions,
            context,
            reads,
            |changed| render_written(model, &changed.rows, id),
        )
    }

    /// The rows a delete of some rows reaches through the relations that
    /// point at them, as a table's foreign keys answer it; `Ok(None)` while
    /// the store has not answered every referring type's rows.
    pub(crate) fn cascade(
        live: &Live,
        start: &[(String, String)],
        skip: &BTreeSet<(String, String)>,
        reads: &mut Reads<'_>,
    ) -> Result<Option<Cascade>, Refusal> {
        let mut cascade = Cascade::default();
        let mut seen: BTreeSet<(String, String)> = start.iter().cloned().collect();
        seen.extend(skip.iter().cloned());
        let mut queue: Vec<(String, String)> = start.to_vec();
        let mut nulled: BTreeMap<(String, String), (StoredRow, StoredRow)> = BTreeMap::new();
        while let Some((target, id)) = queue.pop() {
            for (referrer, member, on_delete) in live.referrers(&target) {
                let Some(rows) = reads.rows(&referrer) else {
                    continue;
                };
                for row in rows {
                    let at = (referrer.clone(), row.id.clone());
                    if seen.contains(&at)
                        || !row
                            .data
                            .get(&member)
                            .is_some_and(|held| names_row(held, &id))
                    {
                        continue;
                    }
                    match on_delete {
                        OnDelete::Cascade => {
                            nulled.remove(&at);
                            seen.insert(at.clone());
                            queue.push(at);
                            cascade.deleted.push((referrer.clone(), row.clone()));
                        }
                        OnDelete::SetNull => {
                            let entry = nulled
                                .entry(at)
                                .or_insert_with(|| (row.clone(), row.clone()));
                            entry.1.data.shift_remove(&member);
                        }
                        OnDelete::Restrict => {
                            let mut details = Map::new();
                            details.insert("type".into(), Value::String(referrer.clone()));
                            details.insert("id".into(), Value::String(row.id.clone()));
                            return Err(Refusal {
                                details: Some(Box::new(details)),
                                ..Refusal::new(
                                    Code::Conflict,
                                    format!(
                                        "{referrer} {} refers to {target} {id} through {member}, whose relation restricts its delete",
                                        row.id
                                    ),
                                )
                            });
                        }
                    }
                }
            }
        }
        if reads.waiting() {
            return Ok(None);
        }
        cascade.nulled = nulled
            .into_iter()
            .map(|((type_name, _), (before, after))| (type_name, before, after))
            .collect();
        Ok(Some(cascade))
    }

    /// The writes of a cascade: each row it deletes and each it clears, and
    /// the changes their types' change queues hear.
    pub(crate) fn cascade_writes(live: &Live, cascade: &Cascade, at: i64) -> Vec<StoreWrite> {
        let mut writes = Vec::new();
        let mut heard = Vec::new();
        for (type_name, row) in &cascade.deleted {
            writes.push(StoreWrite::Delete {
                type_name: type_name.clone(),
                id: row.id.clone(),
                expected: Some(row.version),
            });
            if live.hearing.contains(type_name) {
                heard.push(bridge::change_of(type_name, &row.id, at, Some(row), None));
            }
        }
        for (type_name, before, after) in &cascade.nulled {
            writes.push(StoreWrite::Update {
                type_name: type_name.clone(),
                id: before.id.clone(),
                expected: before.version,
                data: after.data.clone(),
                owned: after.owned.clone(),
            });
            if live.hearing.contains(type_name) {
                let mut moved = after.clone();
                moved.version += 1;
                heard.push(bridge::change_of(
                    type_name,
                    &before.id,
                    at,
                    Some(before),
                    Some(&moved),
                ));
            }
        }
        writes.extend(heard);
        writes
    }

    fn delete(
        &self,
        type_name: &str,
        id: &str,
        preconditions: &Map<String, Value>,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let live = self.live(reads)?;
        let row = reads.row(type_name, id);
        let (Some(live), Some(row)) = (live, row) else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        row.ok_or_else(|| not_found(type_name, id))?;
        let start = [(type_name.to_owned(), id.to_owned())];
        let Some(cascade) = Self::cascade(&live, &start, &BTreeSet::new(), reads)? else {
            return Ok(take(reads).wait());
        };
        let action = Action::Delete { key: id.to_owned() };
        let step = self.run_core(&live, model, action, preconditions, context, reads, |_| {
            let mut deleted = vec![json!({"type": type_name, "id": id})];
            deleted.extend(
                cascade
                    .deleted
                    .iter()
                    .map(|(type_name, row)| json!({"type": type_name, "id": row.id})),
            );
            json!({"deleted": deleted})
        })?;
        Ok(match step {
            HandlerStep::Done(Done {
                response: Ok(response),
                mut unit,
            }) => {
                unit.writes
                    .extend(Self::cascade_writes(&live, &cascade, context.now));
                HandlerStep::Done(Done {
                    response: Ok(response),
                    unit,
                })
            }
            other => other,
        })
    }

    fn list(
        &self,
        type_name: &str,
        filter_given: &Value,
        sort: &[Sort],
        limit: Option<usize>,
        cursor: Option<&str>,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        let conds = filter::compile(model, &live.models, filter_given)?;
        let order = filter::ordering(model, sort)?;
        let limit = limit.unwrap_or(DEFAULT_LIMIT);
        if limit == 0 || limit > MAX_LIMIT {
            return Err(Refusal::invalid_argument(format!(
                "list of {type_name}: limit must be 1 to {MAX_LIMIT}"
            )));
        }
        let mut targets = Vec::new();
        filter::targets(&conds, &mut targets);
        let rows = reads.rows(type_name);
        for target in &targets {
            reads.rows(target);
        }
        let Some(rows) = rows.filter(|_| !reads.waiting()) else {
            return Ok(take(reads).wait());
        };
        let reach: BTreeMap<String, &[StoredRow]> = targets
            .iter()
            .map(|target| {
                (
                    target.clone(),
                    rows_of(reads.answers, target).unwrap_or_default(),
                )
            })
            .collect();
        let mut kept: Vec<&StoredRow> = rows
            .iter()
            .filter(|row| filter::keeps(&conds, row, &reach))
            .collect();
        order.sort(&mut kept);
        if let Some(cursor) = cursor {
            kept = order.after(kept, cursor, type_name)?;
        }
        let more = kept.len() > limit;
        kept.truncate(limit);
        let next = if more {
            kept.last().map(|row| Value::String(order.cursor(row)))
        } else {
            None
        };
        let items: Vec<Value> = kept.iter().map(|row| filter::render(model, row)).collect();
        Ok(done(
            json!({"items": items, "next": next.unwrap_or(Value::Null)}),
            Vec::new(),
        ))
    }

    /// Runs an operation of a row or of the table through the core and
    /// answers its result.
    fn command(
        &self,
        type_name: &str,
        action: Action,
        preconditions: &Map<String, Value>,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let model = live.model(type_name)?;
        self.run_core(
            &live,
            model,
            action,
            preconditions,
            context,
            reads,
            |changed| changed.changes.result.clone(),
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn deadline(
        &self,
        type_name: &str,
        id: &str,
        use_id: &str,
        name: &str,
        at: i64,
        payload: &Value,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let spend = StoreWrite::Spend {
            type_name: type_name.to_owned(),
            id: id.to_owned(),
            use_id: use_id.to_owned(),
            name: name.to_owned(),
            at,
        };
        // A type gone since the arming spends it: nothing is left to run.
        let Ok(model) = live.model(type_name) else {
            return Ok(HandlerStep::Done(Done {
                response: Err(not_found(type_name, id)),
                unit: Unit {
                    writes: vec![spend],
                },
            }));
        };
        let action = Action::Deadline {
            key: id.to_owned(),
            use_id: use_id.to_owned(),
            name: name.to_owned(),
            at,
            payload: payload.clone(),
        };
        // Run at the later of the host's clock and the arming, so a fire
        // never runs before it falls due in the core.
        let context = Context {
            now: context.now.max(at),
            ..context.clone()
        };
        let step = self.run_core(
            &live,
            model,
            action,
            &Map::new(),
            &context,
            reads,
            |changed| changed.changes.result.clone(),
        )?;
        Ok(match step {
            HandlerStep::Done(Done {
                response: Err(refusal),
                ..
            }) => {
                let gone = refusal.code == Code::NotFound
                    && (model.use_of(use_id).is_none()
                        || matches!(row_of(reads.answers, type_name, id), Some(None)));
                let writes = if gone {
                    vec![spend]
                } else if refusal.code == Code::Vetoed {
                    vec![StoreWrite::Release {
                        type_name: type_name.to_owned(),
                        id: id.to_owned(),
                        use_id: use_id.to_owned(),
                        name: name.to_owned(),
                        due: context.now + VETOED_RETRY_MS,
                    }]
                } else {
                    Vec::new()
                };
                HandlerStep::Done(Done {
                    response: Err(refusal),
                    unit: Unit { writes },
                })
            }
            other => other,
        })
    }

    fn change(
        &self,
        type_name: &str,
        id: &str,
        change: &RowChange,
        seq: Option<i64>,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let dequeue = seq.map(|seq| StoreWrite::Dequeue { seq });
        let Ok(model) = live.model(type_name) else {
            return Ok(done(Value::Null, dequeue.into_iter().collect()));
        };
        let action = Action::Change {
            key: id.to_owned(),
            change: change.clone(),
        };
        let step = self.run_core(&live, model, action, &Map::new(), context, reads, |_| {
            Value::Null
        })?;
        Ok(match step {
            HandlerStep::Done(Done { response, mut unit }) => {
                // A change refused is spent as a message gone dead, as the
                // reference host takes it off.
                if response.is_err() {
                    unit.writes.clear();
                }
                unit.writes.extend(dequeue);
                HandlerStep::Done(Done { response, unit })
            }
            other => other,
        })
    }

    /// Runs one command of the core over the store and answers what
    /// `respond` makes of its change set.
    #[allow(clippy::too_many_arguments)]
    fn run_core(
        &self,
        live: &Live,
        model: &Model,
        action: Action,
        preconditions: &Map<String, Value>,
        context: &Context,
        reads: &mut Reads<'_>,
        respond: impl FnOnce(&bridge::Changed) -> Value,
    ) -> Result<HandlerStep, Refusal> {
        let spec = live
            .specs
            .get(&model.name)
            .cloned()
            .unwrap_or_else(|| model.spec());
        let command = Command::new(context.caller.clone(), context.now, action)
            .with_request(context.request.clone())
            .with_preconditions(preconditions.clone());
        // A row write's own fields are held to their type as an update's
        // are, an operation's write of them (Cx::update) among them: the
        // columns and their checks a table would hold them to.
        let own = |type_name: &str, before: &Map<String, Value>, patch: &Map<String, Value>| {
            let model = live.model(type_name)?;
            validate::patched(model, before, patch)
                .map(|patched| patched.fields)
                .map_err(|issues| Refusal::invalid_instance(type_name, issues))
        };
        let bridge = Bridge {
            core: self.core,
            specs: &live.specs,
            hearing: &live.hearing,
            own: Some(&own),
        };
        Ok(match bridge.run(&spec, &command, reads.answers) {
            Ran::Read(need) => {
                for read in need {
                    reads.ask(read);
                }
                take(reads).wait()
            }
            Ran::Refused(refusal) => refused(refusal),
            Ran::Changed(changed) => {
                if let Some(refusal) = Self::own_writes_refusal(live, &changed, reads) {
                    return Ok(refused(refusal));
                }
                if reads.waiting() {
                    return Ok(take(reads).wait());
                }
                done(respond(&changed), changed.writes)
            }
        })
    }

    /// The refusal of a change set whose row writes set own fields a
    /// relation or a unique key refuses, as a foreign key and a unique
    /// index refuse a table's write: an update's patch, and an operation's
    /// write of its row's own fields (Cx::update, D103's step 4). Asks for
    /// the rows it needs.
    fn own_writes_refusal(
        live: &Live,
        changed: &bridge::Changed,
        reads: &mut Reads<'_>,
    ) -> Option<Refusal> {
        for write in &changed.changes.rows {
            let RowWrite::Update {
                type_name,
                key,
                own,
                ..
            } = write
            else {
                continue;
            };
            let Ok(model) = live.model(type_name) else {
                continue;
            };
            if own.is_empty() {
                continue;
            }
            let (patch, _) = validate::own_fields(model, own, Mode::Patch);
            let relation = Self::relation_issues(model, &patch, reads);
            if !relation.is_empty() {
                return Some(Refusal::invalid_instance(type_name, relation));
            }
            let touches_unique = model
                .uniques
                .iter()
                .any(|unique| unique.iter().any(|name| patch.contains_key(name)));
            if !touches_unique {
                continue;
            }
            let Some(rows) = reads.rows(type_name) else {
                continue;
            };
            let after = changed
                .rows
                .iter()
                .rev()
                .find(|(t, k, _)| t == type_name && k == key)
                .and_then(|(_, _, row)| row.as_ref());
            if let Some(conflict) =
                after.and_then(|after| Self::unique_conflict(model, key, &after.data, rows))
            {
                return Some(conflict);
            }
        }
        None
    }
}

// take moves the reads a run needs out of it, for its answer.
fn take<'r>(reads: &mut Reads<'r>) -> Reads<'r> {
    Reads {
        answers: reads.answers,
        need: std::mem::take(&mut reads.need),
    }
}

fn object_or_empty(params: &Value) -> Value {
    if params.is_null() {
        Value::Object(Map::new())
    } else {
        params.clone()
    }
}

fn not_found(type_name: &str, id: &str) -> Refusal {
    Refusal::not_found(format!("{type_name} {id} does not exist"))
}

fn render_written(model: &Model, rows: &[(String, String, Option<StoredRow>)], id: &str) -> Value {
    rows.iter()
        .rev()
        .find(|(type_name, key, _)| *type_name == model.name && key == id)
        .and_then(|(_, _, row)| row.as_ref())
        .map_or(Value::Null, |row| filter::render(model, row))
}

/// A UUID's value as its lowercase hyphenated text: a string key's text
/// when the host makes it.
fn hyphenated(n: u128) -> String {
    let hex = format!("{n:032x}");
    format!(
        "{}-{}-{}-{}-{}",
        &hex[..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..]
    )
}

/// The refusal of a document whose relation points at a type with no
/// published version.
pub(crate) fn unpublished_target(model: &Model, field: &str, target: &str) -> Refusal {
    document_refusal(
        &model.name,
        vec![superschematic_rules::SchemaIssue::new(
            format!("{}/fields", model.at),
            format!("the relation {field} points at {target}, which has no published version"),
        )],
    )
}
