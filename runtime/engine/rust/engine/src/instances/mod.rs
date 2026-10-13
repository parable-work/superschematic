//! Instances of a schema's instance type, the port of
//! `src/instances/store.ts` (D16, D58), keyed by namespace, schema name and
//! id. The key includes the namespace, so an id in one namespace says
//! nothing about another. An instance is written with the live version of
//! its schema (its own namespace's, or the shared one's) and validated by
//! it; its row records that version. An update is a JSON merge patch
//! (RFC 7386, [`patch`]), validated after the merge. Each write appends its
//! event in the same transaction (`events`). A write in an
//! archived namespace (a create, an update or a delete) is refused
//! (`namespace_archived`) once the policy has allowed it; reads go on.
//!
//! A write takes its namespace's lock first (the name it was given, or
//! `default`), then reads the namespaces and resolves its own inside the
//! transaction, so a namespace another process archived is seen at once
//! (D58), and asks the policy there: the policy is data, so asking it in
//! the transaction calls back into no host.
//!
//! The behaviors of the live version run with every call (milestone 5):
//! the write path reaches them at the points `hooks` names, in the
//! TypeScript engine's order (validate, the create's parameters and the
//! preconditions, guards, initialize, afterChange, the references' guards
//! and hooks, the behaviors' fields on every read). `invoke`, `operate` and
//! `invokeSchema` arrive with the behaviors.
//!
//! `list` pages through a schema's instances in creation order with an
//! opaque cursor, each page one indexed range read of at most the page
//! size; its `where` keeps the instances whose fields hold the values it
//! names (`filters`). `lookup` reads the one instance whose unique fields
//! hold a key's values, through their index. The indexes of the instance
//! type's own fields refuse a create or an update that would repeat a
//! unique field's value in the namespace (`conflict`), and keep each field
//! they cover inline in the row.
//!
//! A row keeps each own field whose JSON is longer than the value store's
//! threshold as a ref, the value stored once by hash (`values`),
//! and so does each event a write appends. Every read puts the values back;
//! a get or a list that asks for `valueRefs` returns the refs instead, with
//! the pointers to them. A write reuses the ref of a member it leaves alone,
//! so it neither hashes nor rewrites a large value it did not change.

pub(crate) mod filters;
mod hooks;
pub mod patch;
pub(crate) mod references;

use std::collections::BTreeSet;

use serde::Serialize;
use serde_json::{Map, Value};

use superschematic_engine_storage::{Lock, Row, RunResult, SqlValue, Tx};

use crate::access::SchemaAction;
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result, ValidationIssue};
use crate::events::{append_event, next_seq, NewEvent};
use crate::json::{self, js_equal};
use crate::namespaces::{NamespaceView, DEFAULT_NAMESPACE};
use crate::paging::page_size;
use crate::principal::{actor_of, check_principal, Principal};
use crate::registry::document::check_schema_name;
use crate::registry::{Catalog, NormalizeMode, SchemaRecord, VersionRuntime, Which};
use crate::values::{refs_of, refs_text, ValueHolder};
use filters::{
    filtered_page, lookup_condition, normalized_filter, parse_lookup_key, parse_where, FilterPlan,
};
use hooks::{check_create_params, check_preconditions, Chain, Execution, InstanceChange};
use patch::{diff_patch, merge_patch};
use references::{moves, ReferenceTable};

pub use filters::{FILTER_SCAN_ROWS, MAX_FILTER_RANGES, MAX_FILTER_VALUES};

/// What an instance id is, as a regular expression: a letter or digit, then
/// letters, digits, `.`, `_`, `:` and `-`, at most 256 characters.
pub const INSTANCE_ID: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$";

/// Whether an id matches [`INSTANCE_ID`].
pub fn is_instance_id(id: &str) -> bool {
    let bytes = id.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 256
        && bytes[0].is_ascii_alphanumeric()
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b':' | b'-'))
}

/// A stored instance, as a read returns it.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InstanceRecord {
    pub namespace: String,
    pub schema: String,
    pub id: String,
    /// The namespace that holds the schema: the instance's own, or the
    /// shared one.
    pub schema_namespace: String,
    /// The schema version the instance was last written with.
    pub version: i64,
    /// The per-instance sequence of its last event: its entity tag.
    pub seq: i64,
    /// The instance's own fields.
    pub data: Map<String, Value>,
    /// Its behaviors' fields, by behavior name (D16, amended): `{}` until a
    /// behavior that declares fields composes (milestone 5).
    pub behaviors: Map<String, Value>,
    pub created_at: i64,
    pub created_by: String,
    pub updated_at: i64,
    pub updated_by: String,
    /// For a read that asked for `valueRefs`: the JSON pointers into data
    /// of the own fields that hold a ref in place of their value. Absent
    /// when none does.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value_refs: Option<Vec<String>>,
}

/// One page of a list.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct InstancePage {
    pub items: Vec<InstanceRecord>,
    /// The cursor of the next page; `None` after the last.
    pub next: Option<String>,
}

/// What a get or a lookup takes beside the schema and the id or key.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct GetOptions<'a> {
    /// `default` when absent.
    pub namespace: Option<&'a str>,
    /// Return each own field the value store holds as its ref, with the
    /// pointers to them in `value_refs`, rather than its value.
    pub value_refs: bool,
}

/// What a create takes beside the schema and the data.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct CreateOptions<'a> {
    pub namespace: Option<&'a str>,
    /// The id; the engine's id source makes one when absent.
    pub id: Option<&'a str>,
    /// The parameters the create gives the type's behaviors, by behavior
    /// name (milestone 5).
    pub behaviors: Option<&'a Value>,
}

/// What an update or a delete takes beside the schema and the id.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct WriteOptions<'a> {
    pub namespace: Option<&'a str>,
    /// The sequence the caller last read: the write is refused with
    /// `seq_mismatch` unless the instance is still at it, checked inside
    /// the write's transaction.
    pub expected_seq: Option<i64>,
    /// Preconditions by behavior, each handed to that behavior's guard
    /// (milestone 5).
    pub preconditions: Option<&'a Value>,
}

/// What a list takes beside the schema.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct ListOptions<'a> {
    pub namespace: Option<&'a str>,
    /// At most this many instances, 50 by default and at most 500.
    pub limit: Option<i64>,
    /// The `next` of the previous page.
    pub cursor: Option<&'a str>,
    /// As [`GetOptions::value_refs`].
    pub value_refs: bool,
    /// Field values the instances hold, by field (`where`): a value, null
    /// for none, or a list of 1 to 100 of which the instance holds one.
    pub filter: Option<&'a Value>,
}

const COLUMNS: &str = "position, namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, value_refs";

/// The instance store (`engine.instances`).
pub struct Instances<'e> {
    pub(crate) engine: &'e Engine,
}

impl Instances<'_> {
    /// Validates `data` against the schema's live version, and the
    /// behaviors' parameters, and stores the instance under a new id.
    pub fn create(
        &self,
        principal: &Principal,
        schema: &str,
        data: &Value,
        options: CreateOptions<'_>,
    ) -> Result<InstanceRecord> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.write(lock_of(options.namespace), |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Write,
                schema,
                options.namespace,
            )?;
            let id = match options.id {
                Some(id) => id.to_owned(),
                None => engine.new_id()?,
            };
            check_id(&id)?;
            let chain = Chain::new(principal, &namespace, engine.clock.now());
            insert(
                engine,
                tx,
                &view,
                &chain,
                schema,
                &id,
                data,
                options.behaviors,
            )
        })
    }

    /// The instance, or `None` when the namespace has none with the id.
    pub fn get(
        &self,
        principal: &Principal,
        schema: &str,
        id: &str,
        options: GetOptions<'_>,
    ) -> Result<Option<InstanceRecord>> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Read,
                schema,
                options.namespace,
            )?;
            let record = live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(&record)?;
            let Some(row) = row(tx, &namespace, schema, id)? else {
                return Ok(None);
            };
            // The read's chain, which milestone 5's field readers run in:
            // its time is read where the TypeScript engine reads it, so a
            // replayed clock reads the same times.
            let _chain = Chain::new(principal, &namespace, engine.clock.now());
            Ok(Some(read(engine, tx, &runtime, &row, options.value_refs)?))
        })
    }

    /// A page of a schema's instances in creation order; with a filter
    /// (`where`), of the ones whose fields hold the values it names.
    pub fn list(
        &self,
        principal: &Principal,
        schema: &str,
        options: ListOptions<'_>,
    ) -> Result<InstancePage> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Read,
                schema,
                options.namespace,
            )?;
            let limit = page_size(options.limit)?;
            let after = match options.cursor {
                None => 0,
                Some(cursor) => decode_cursor(cursor)?,
            };
            let record = live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(&record)?;
            // The read's chain, which milestone 5's field readers run in:
            // its time is read where the TypeScript engine reads it, so a
            // replayed clock reads the same times.
            let _chain = Chain::new(principal, &namespace, engine.clock.now());
            let filter = normalized_filter(options.filter, |key, value| {
                runtime.validator.normalize_field(key, value)
            });
            let terms = parse_where(filter.as_ref(), &runtime.filters, schema)?;
            let (rows, next) = if terms.is_empty() {
                let mut rows = tx.all(
                    format!(
                        "SELECT {COLUMNS} FROM engine_instances
         WHERE namespace = ? AND schema = ? AND position > ?
         ORDER BY position LIMIT ?"
                    ),
                    &[
                        SqlValue::from(namespace.as_str()),
                        SqlValue::from(schema),
                        SqlValue::Integer(after),
                        SqlValue::Integer(limit as i64 + 1),
                    ],
                )?;
                let more = rows.len() > limit;
                rows.truncate(limit);
                let next = match (more, rows.last()) {
                    (true, Some(last)) => Some(encode_cursor(last.i64("position")?)),
                    _ => None,
                };
                (rows, next)
            } else {
                let (positions, next) = filtered_page(
                    tx,
                    &FilterPlan {
                        namespace: &namespace,
                        schema,
                        holder: &record.namespace,
                        indexes: &runtime.indexes,
                        terms: &terms,
                    },
                    after,
                    limit,
                )?;
                let rows = if positions.is_empty() {
                    Vec::new()
                } else {
                    let mut params =
                        vec![SqlValue::from(namespace.as_str()), SqlValue::from(schema)];
                    params.extend(
                        positions
                            .iter()
                            .map(|position| SqlValue::Integer(*position)),
                    );
                    tx.all(
                        format!(
                            "SELECT {COLUMNS} FROM engine_instances
               WHERE namespace = ? AND schema = ? AND position IN ({})
               ORDER BY position",
                            vec!["?"; positions.len()].join(", ")
                        ),
                        &params,
                    )?
                };
                (rows, next.map(encode_cursor))
            };
            let items = rows
                .iter()
                .map(|row| read(engine, tx, &runtime, row, options.value_refs))
                .collect::<Result<_>>()?;
            Ok(InstancePage { items, next })
        })
    }

    /// The instance whose unique fields hold a key's values, or `None` when
    /// the namespace has none. The key names the fields of one unique index
    /// of the instance type, each with one value of its type.
    pub fn lookup(
        &self,
        principal: &Principal,
        schema: &str,
        key: &Value,
        options: GetOptions<'_>,
    ) -> Result<Option<InstanceRecord>> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Read,
                schema,
                options.namespace,
            )?;
            let record = live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(&record)?;
            let key = normalized_filter(Some(key), |name, value| {
                runtime.validator.normalize_field(name, value)
            })
            .unwrap_or(Value::Null);
            let (index, values) =
                parse_lookup_key(&key, &runtime.indexes, &runtime.filters, schema)?;
            let mut params = vec![SqlValue::from(namespace.as_str())];
            params.extend(values.iter().map(|value| value.bind()));
            let found = tx.get(
                format!(
                    "SELECT {COLUMNS} FROM engine_instances WHERE {} LIMIT 1",
                    lookup_condition(tx.dialect(), &record.namespace, schema, index)
                ),
                &params,
            )?;
            let Some(found) = found else {
                return Ok(None);
            };
            // The read's chain, which milestone 5's field readers run in:
            // its time is read where the TypeScript engine reads it, so a
            // replayed clock reads the same times.
            let _chain = Chain::new(principal, &namespace, engine.clock.now());
            Ok(Some(read(
                engine,
                tx,
                &runtime,
                &found,
                options.value_refs,
            )?))
        })
    }

    /// Applies a JSON merge patch to an instance and validates the result
    /// against the live version. A patch that changes nothing writes
    /// nothing. With an expected sequence, the instance must still be at
    /// it.
    pub fn update(
        &self,
        principal: &Principal,
        schema: &str,
        id: &str,
        patch: &Value,
        options: WriteOptions<'_>,
    ) -> Result<InstanceRecord> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.write(lock_of(options.namespace), |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Write,
                schema,
                options.namespace,
            )?;
            check_expected_seq(options.expected_seq)?;
            let Value::Object(_) = patch else {
                return Err(EngineError::invalid_argument(
                    "a merge patch of an instance is a JSON object",
                )
                .into());
            };
            let chain = Chain::new(principal, &namespace, engine.clock.now());
            let record = live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(&record)?;
            let preconditions =
                check_preconditions(&runtime.composition, schema, options.preconditions)?;
            let row = existing(tx, &namespace, schema, id)?;
            match_seq(&row, options.expected_seq)?;
            let current = own_of(engine, tx, &row)?;
            // The patch's values as the version stores them; an update
            // fills no default. The event and the guards get the patch as
            // stored.
            let normalized = runtime.validator.normalize(patch, NormalizeMode::Patch);
            let given = object_of(json::js_copy(&normalized.value));
            let merged = object_of(merge_patch(
                &Value::Object(current.clone()),
                &Value::Object(given.clone()),
            ));
            validate(
                &namespace,
                &record,
                &runtime,
                &Value::Object(merged.clone()),
                normalized.issues,
            )?;
            let execution = Execution::new(&runtime.composition);
            let change = InstanceChange::Update {
                patch: &given,
                before: &current,
                after: &merged,
            };
            validate_behaviors(&chain, &record, &execution, &change)?;
            if js_equal(
                &Value::Object(merged.clone()),
                &Value::Object(current.clone()),
            ) {
                return read(engine, tx, &runtime, &row, false);
            }
            execution.guard(&change, true, preconditions.as_ref())?;
            let before = execution.fields();
            let seq = row.i64("seq")? + 1;
            write_own(
                engine,
                tx,
                &runtime,
                &namespace,
                schema,
                id,
                &merged,
                &given.keys().cloned().collect(),
            )?;
            tx.run(
                "UPDATE engine_instances
           SET version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
           WHERE namespace = ? AND schema = ? AND id = ?",
                &[
                    SqlValue::from(record.version),
                    SqlValue::from(record.namespace.as_str()),
                    SqlValue::Integer(seq),
                    SqlValue::Integer(chain.now),
                    SqlValue::from(principal.subject.as_str()),
                    SqlValue::from(namespace.as_str()),
                    SqlValue::from(schema),
                    SqlValue::from(id),
                ],
            )?;
            execution.after_change(&change)?;
            let after = execution.fields();
            let logged = engine.values.stow_change(
                tx,
                "",
                Some(&given),
                Some(&diff_patch(&before, &after)),
                false,
                &BTreeSet::new(),
            )?;
            append_event(
                tx,
                &NewEvent {
                    kind: "update",
                    namespace: &namespace,
                    schema,
                    instance_id: Some(id),
                    seq: Some(seq),
                    version: record.version,
                    actor: &actor_of(principal),
                    at: chain.now,
                    change: Some(json::stringify(&Value::Object(logged.value.clone()))),
                    cause: chain.cause.as_ref(),
                    values: Some((&engine.values, &logged)),
                },
            )?;
            let updated = to_instance(
                &existing(tx, &namespace, schema, id)?,
                merged.clone(),
                after.clone(),
            )?;
            let mut was = Map::new();
            was.insert("data".into(), Value::Object(current.clone()));
            was.insert("behaviors".into(), Value::Object(before));
            let mut now = Map::new();
            now.insert("data".into(), Value::Object(merged.clone()));
            now.insert("behaviors".into(), Value::Object(after));
            execution.referenced(&change, &moves(&Value::Object(was), &Value::Object(now)))?;
            Ok(updated)
        })
    }

    /// Removes an instance and appends its delete event; false when there
    /// is none. With an expected sequence, the instance must still be at
    /// it. The references its behaviors recorded go with it.
    pub fn delete(
        &self,
        principal: &Principal,
        schema: &str,
        id: &str,
        options: WriteOptions<'_>,
    ) -> Result<bool> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.write(lock_of(options.namespace), |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = target(
                engine,
                &view,
                principal,
                SchemaAction::Write,
                schema,
                options.namespace,
            )?;
            check_expected_seq(options.expected_seq)?;
            let chain = Chain::new(principal, &namespace, engine.clock.now());
            let record = live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(&record)?;
            let preconditions =
                check_preconditions(&runtime.composition, schema, options.preconditions)?;
            let Some(row) = row(tx, &namespace, schema, id)? else {
                return Ok(false);
            };
            match_seq(&row, options.expected_seq)?;
            let execution = Execution::new(&runtime.composition);
            let change = InstanceChange::Delete;
            execution.guard(&change, true, preconditions.as_ref())?;
            execution.deleting()?;
            tx.run(
                "DELETE FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?",
                &[
                    SqlValue::from(namespace.as_str()),
                    SqlValue::from(schema),
                    SqlValue::from(id),
                ],
            )?;
            execution.after_change(&change)?;
            ReferenceTable::drop_source(tx, &namespace, schema, id)?;
            engine
                .values
                .release(tx, &namespace, schema, "instance", id, Some(""))?;
            append_event(
                tx,
                &NewEvent {
                    kind: "delete",
                    namespace: &namespace,
                    schema,
                    instance_id: Some(id),
                    seq: Some(row.i64("seq")? + 1),
                    version: Some(row.i64("version")?),
                    actor: &actor_of(principal),
                    at: chain.now,
                    change: None,
                    cause: chain.cause.as_ref(),
                    values: None,
                },
            )?;
            execution.referenced(&change, &[])?;
            Ok(true)
        })
    }
}

// lock_of is the lock a write in a namespace takes: the namespace's, by the
// name the call gave, before the transaction resolves it.
fn lock_of(namespace: Option<&str>) -> Lock<'_> {
    Lock::Namespace(namespace.unwrap_or(DEFAULT_NAMESPACE))
}

// target resolves a call's namespace and asks the policy; a write is then
// refused in an archived namespace.
fn target(
    engine: &Engine,
    view: &NamespaceView<'_>,
    principal: &Principal,
    action: SchemaAction,
    schema: &str,
    namespace: Option<&str>,
) -> Result<String> {
    let namespace = view.resolve(namespace)?;
    check_schema_name(schema)?;
    engine
        .access
        .require(principal, action, &namespace, schema)?;
    if action == SchemaAction::Write {
        view.require_writable(&namespace)?;
    }
    Ok(namespace)
}

fn live(
    catalog: &Catalog,
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    namespace: &str,
    schema: &str,
) -> Result<SchemaRecord> {
    catalog
        .find(tx, view, schema, namespace, Which::Live)?
        .ok_or_else(|| {
            EngineError::new(
                EngineErrorCode::NotFound,
                format!("schema {schema} has no live version in namespace {namespace}"),
            )
            .into()
        })
}

// insert creates an instance in the chain's namespace inside the chain's
// transaction: validate the data against the live version, then the
// behaviors' validate, and the behaviors' parameters; insert; every guard,
// every initialize, every afterChange; then the create event. The policy
// has been asked.
#[allow(clippy::too_many_arguments)]
fn insert(
    engine: &Engine,
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    chain: &Chain<'_>,
    schema: &str,
    id: &str,
    data: &Value,
    behaviors: Option<&Value>,
) -> Result<InstanceRecord> {
    let namespace = chain.namespace.as_str();
    let subject = chain.principal.subject.as_str();
    let record = live(&engine.catalog, tx, view, namespace, schema)?;
    let runtime = engine.catalog.runtime_of(&record)?;
    // What the create stores: each value as its scalar's parse makes it,
    // each absent field's default filled.
    let normalized = runtime.validator.normalize(data, NormalizeMode::Create);
    validate(
        namespace,
        &record,
        &runtime,
        &normalized.value,
        normalized.issues,
    )?;
    let own = object_of(json::js_copy(&normalized.value));
    let execution = Execution::new(&runtime.composition);
    validate_behaviors(
        chain,
        &record,
        &execution,
        &InstanceChange::Create {
            data: &own,
            params: &Map::new(),
        },
    )?;
    let params = check_create_params(&runtime.composition, schema, behaviors)?;
    let seq = next_seq(tx, namespace, schema, id)?;
    let stowed = engine
        .values
        .stow(tx, &own, "", &BTreeSet::new(), &runtime.inline)?;
    let inserted = unique(namespace, schema, &runtime, &own, tx.run(
        "INSERT INTO engine_instances
           (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, value_refs)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, id) DO NOTHING",
        &[
            SqlValue::from(namespace),
            SqlValue::from(schema),
            SqlValue::from(id),
            SqlValue::from(record.namespace.as_str()),
            SqlValue::from(record.version),
            SqlValue::Integer(seq),
            SqlValue::from(json::stringify(&Value::Object(stowed.value.clone()))),
            SqlValue::Integer(chain.now),
            SqlValue::from(subject),
            SqlValue::Integer(chain.now),
            SqlValue::from(subject),
            SqlValue::from(refs_text(&stowed.refs)),
        ],
    ))?;
    if inserted.changes == 0 {
        return Err(EngineError::new(
            EngineErrorCode::Conflict,
            format!("{schema} {id} already exists in namespace {namespace}"),
        )
        .into());
    }
    if !stowed.hashes.is_empty() {
        engine
            .values
            .hold(tx, &row_holder(namespace, schema, id), &stowed.hashes)?;
    }
    let change = InstanceChange::Create {
        data: &own,
        params: &params,
    };
    // Nothing refers to a new instance, so no reference's guard is asked.
    execution.guard(&change, false, None)?;
    execution.initialize(&params)?;
    execution.after_change(&change)?;
    let fields = execution.fields();
    let instance = to_instance(&existing(tx, namespace, schema, id)?, own, fields.clone())?;
    // The event's instance is what a read returns, { data, behaviors }: its
    // own fields keep the row's refs, and a large field of a behavior's
    // goes to the store as an own field does.
    let known: BTreeSet<String> = stowed.refs.iter().map(|at| format!("/data{at}")).collect();
    let logged =
        engine
            .values
            .stow_change(tx, "", Some(&stowed.value), Some(&fields), true, &known)?;
    append_event(
        tx,
        &NewEvent {
            kind: "create",
            namespace,
            schema,
            instance_id: Some(id),
            seq: Some(seq),
            version: record.version,
            actor: &actor_of(chain.principal),
            at: chain.now,
            change: Some(json::stringify(&Value::Object(logged.value.clone()))),
            cause: chain.cause.as_ref(),
            values: Some((&engine.values, &logged)),
        },
    )?;
    Ok(instance)
}

// validate refuses what the live version refuses in data, or, when it
// refuses nothing, what a scalar's parser refused as the write was
// normalized.
fn validate(
    namespace: &str,
    record: &SchemaRecord,
    runtime: &VersionRuntime,
    data: &Value,
    parsed: Vec<ValidationIssue>,
) -> Result<()> {
    let issues = runtime.validator.validate(data);
    if issues.is_empty() && parsed.is_empty() {
        return Ok(());
    }
    Err(EngineError::instance_validation(
        namespace,
        &record.name,
        record.version.unwrap_or_default(),
        if issues.is_empty() { parsed } else { issues },
    )
    .into())
}

// validate_behaviors asks each behavior's validate about the own fields a
// write would store, which the live version accepts; their issues refuse
// the write as the live version's do.
fn validate_behaviors(
    chain: &Chain<'_>,
    record: &SchemaRecord,
    execution: &Execution<'_>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    let issues = execution.validation_issues(change);
    if issues.is_empty() {
        return Ok(());
    }
    Err(EngineError::instance_validation(
        &chain.namespace,
        &record.name,
        record.version.unwrap_or_default(),
        issues,
    )
    .into())
}

// read returns a stored instance with its behaviors' fields. Its own
// fields have their values back, or with value_refs the refs.
fn read(
    engine: &Engine,
    tx: &Tx<'_>,
    runtime: &VersionRuntime,
    row: &Row,
    value_refs: bool,
) -> Result<InstanceRecord> {
    let own = if value_refs {
        object_of(parse(row.text("data")?)?)
    } else {
        own_of(engine, tx, row)?
    };
    let fields = Execution::new(&runtime.composition).fields();
    let mut instance = to_instance(row, own, fields)?;
    if value_refs {
        let refs = refs_of(row.opt_text("value_refs")?)?;
        if !refs.is_empty() {
            instance.value_refs = Some(refs);
        }
    }
    Ok(instance)
}

// own_of is a row's own fields with their values back.
fn own_of(engine: &Engine, tx: &Tx<'_>, row: &Row) -> Result<Map<String, Value>> {
    let stored = parse(row.text("data")?)?;
    let refs = refs_of(row.opt_text("value_refs")?)?;
    Ok(object_of(engine.values.fill(tx, &stored, &refs)?))
}

// write_own stores an instance's own fields in its row, each large one by
// hash but the ones an index covers. A member the write did not change
// keeps the ref the row holds, so it is neither hashed nor written again.
// A unique index the new values break refuses the write.
#[allow(clippy::too_many_arguments)]
fn write_own(
    engine: &Engine,
    tx: &Tx<'_>,
    runtime: &VersionRuntime,
    namespace: &str,
    schema: &str,
    id: &str,
    data: &Map<String, Value>,
    changed: &BTreeSet<String>,
) -> Result<()> {
    let key = [
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(id),
    ];
    let Some(row) = tx.get(
        "SELECT data, value_refs FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?",
        &key,
    )?
    else {
        return Err(not_found(namespace, schema, id).into());
    };
    let stored = object_of(parse(row.text("data")?)?);
    let mut object = data.clone();
    let mut known = BTreeSet::new();
    let held = refs_of(row.opt_text("value_refs")?)?;
    for at in &held {
        let Some(member) = top_key(at) else {
            continue;
        };
        if !changed.contains(&member)
            && !runtime.inline.contains(&member)
            && data.contains_key(&member)
        {
            if let Some(reference) = stored.get(&member) {
                object.insert(member.clone(), reference.clone());
                known.insert(at.clone());
            }
        }
    }
    let stowed = engine
        .values
        .stow(tx, &object, "", &known, &runtime.inline)?;
    unique(
        namespace,
        schema,
        runtime,
        data,
        tx.run(
            "UPDATE engine_instances SET data = ?, value_refs = ? WHERE namespace = ? AND schema = ? AND id = ?",
            &[
                SqlValue::from(json::stringify(&Value::Object(stowed.value.clone()))),
                SqlValue::from(refs_text(&stowed.refs)),
                SqlValue::from(namespace),
                SqlValue::from(schema),
                SqlValue::from(id),
            ],
        ),
    )?;
    // A row that held no value and holds none has no holds to change.
    if !held.is_empty() || !stowed.hashes.is_empty() {
        engine
            .values
            .hold(tx, &row_holder(namespace, schema, id), &stowed.hashes)?;
    }
    Ok(())
}

// unique turns the database's refusal of a write of an instance's row by a
// unique index of the version into the fields whose values another
// instance of the namespace holds; any other failure passes.
fn unique(
    namespace: &str,
    schema: &str,
    runtime: &VersionRuntime,
    data: &Map<String, Value>,
    written: superschematic_engine_storage::Result<RunResult>,
) -> Result<RunResult> {
    match written {
        Err(superschematic_engine_storage::Error::Unique {
            index: Some(name), ..
        }) if runtime.indexes.iter().any(|index| index.name == name) => {
            let index = runtime
                .indexes
                .iter()
                .find(|index| index.name == name)
                .ok_or_else(|| Error::internal("the index just found is gone"))?;
            let values: Vec<Value> = index
                .keys
                .iter()
                .map(|key| data.get(key).cloned().unwrap_or(Value::Null))
                .collect();
            Err(EngineError::unique_conflict(namespace, schema, &index.keys, &values).into())
        }
        other => Ok(other?),
    }
}

fn row(tx: &Tx<'_>, namespace: &str, schema: &str, id: &str) -> Result<Option<Row>> {
    Ok(tx.get(
        format!(
            "SELECT {COLUMNS} FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?"
        ),
        &[
            SqlValue::from(namespace),
            SqlValue::from(schema),
            SqlValue::from(id),
        ],
    )?)
}

fn existing(tx: &Tx<'_>, namespace: &str, schema: &str, id: &str) -> Result<Row> {
    row(tx, namespace, schema, id)?.ok_or_else(|| not_found(namespace, schema, id).into())
}

fn not_found(namespace: &str, schema: &str, id: &str) -> EngineError {
    EngineError::new(
        EngineErrorCode::NotFound,
        format!("{schema} {id} does not exist in namespace {namespace}"),
    )
}

fn to_instance(
    row: &Row,
    own: Map<String, Value>,
    fields: Map<String, Value>,
) -> Result<InstanceRecord> {
    Ok(InstanceRecord {
        namespace: row.text("namespace")?.to_owned(),
        schema: row.text("schema")?.to_owned(),
        id: row.text("id")?.to_owned(),
        schema_namespace: row.text("schema_namespace")?.to_owned(),
        version: row.i64("version")?,
        seq: row.i64("seq")?,
        data: own,
        behaviors: fields,
        created_at: row.i64("created_at")?,
        created_by: row.text("created_by")?.to_owned(),
        updated_at: row.i64("updated_at")?,
        updated_by: row.text("updated_by")?.to_owned(),
        value_refs: None,
    })
}

fn parse(text: &str) -> Result<Value> {
    serde_json::from_str(text)
        .map_err(|error| Error::internal(format!("instance data is not JSON: {error}")))
}

fn object_of(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(members) => members,
        _ => Map::new(),
    }
}

fn row_holder<'a>(namespace: &'a str, schema: &'a str, id: &'a str) -> ValueHolder<'a> {
    ValueHolder {
        namespace,
        schema,
        holder: "instance",
        id,
        key: "",
    }
}

// top_key is the member a top-level JSON pointer names, or None for a
// deeper one.
fn top_key(at: &str) -> Option<String> {
    let rest = at.strip_prefix('/')?;
    if rest.contains('/') {
        return None;
    }
    Some(rest.replace("~1", "/").replace("~0", "~"))
}

/// Refuses an expected sequence that is no non-negative integer.
pub(crate) fn check_expected_seq(expected: Option<i64>) -> Result<(), EngineError> {
    match expected {
        Some(seq) if seq < 0 => Err(expected_seq_refusal(&seq.to_string())),
        _ => Ok(()),
    }
}

/// The refusal of an expected sequence, as the request wrote it.
pub fn expected_seq_refusal(given: &str) -> EngineError {
    EngineError::invalid_argument(format!(
        "an expected sequence is a non-negative integer, got {given}"
    ))
}

// An instance's sequence starts at 1, so an expected sequence of 0 matches
// no instance.
fn match_seq(row: &Row, expected: Option<i64>) -> Result<()> {
    let Some(expected) = expected else {
        return Ok(());
    };
    if row.i64("seq")? != expected {
        return Err(EngineError::new(
            EngineErrorCode::SeqMismatch,
            format!(
                "{} {} is no longer at sequence {expected}",
                row.text("schema")?,
                row.text("id")?
            ),
        )
        .into());
    }
    Ok(())
}

fn check_id(id: &str) -> Result<(), EngineError> {
    if is_instance_id(id) {
        return Ok(());
    }
    Err(EngineError::invalid_argument(format!(
        "instance id \"{id}\" must match {INSTANCE_ID}"
    )))
}

const BASE64URL: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

// A cursor is the position of the last instance of a page, base64url
// encoded (no padding) so callers treat it as opaque.
fn encode_cursor(position: i64) -> String {
    let text = format!("after:{position}");
    let bytes = text.as_bytes();
    let mut out = String::new();
    for chunk in bytes.chunks(3) {
        let n = chunk.iter().enumerate().fold(0u32, |n, (index, byte)| {
            n | (u32::from(*byte) << (16 - 8 * index))
        });
        for index in 0..=chunk.len() {
            out.push(char::from(
                BASE64URL[((n >> (18 - 6 * index)) & 63) as usize],
            ));
        }
    }
    out
}

fn decode_cursor(cursor: &str) -> Result<i64, EngineError> {
    let refused =
        || EngineError::invalid_argument("the list cursor is not one this engine returned");
    let mut bits = 0u32;
    let mut count = 0;
    let mut bytes = Vec::new();
    for c in cursor.trim_end_matches('=').bytes() {
        let value = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'-' | b'+' => 62,
            b'_' | b'/' => 63,
            _ => return Err(refused()),
        };
        bits = (bits << 6) | u32::from(value);
        count += 6;
        if count >= 8 {
            count -= 8;
            bytes.push((bits >> count) as u8);
            bits &= (1 << count) - 1;
        }
    }
    let text = String::from_utf8(bytes).map_err(|_| refused())?;
    let digits = text.strip_prefix("after:").ok_or_else(refused)?;
    if digits.is_empty() || digits.len() > 15 || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return Err(refused());
    }
    digits.parse::<i64>().map_err(|_| refused())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_cursor_is_base64url_of_a_position() {
        // Buffer.from('after:12', 'utf8').toString('base64url')
        assert_eq!(encode_cursor(12), "YWZ0ZXI6MTI");
        assert_eq!(encode_cursor(7), "YWZ0ZXI6Nw");
        for position in [0, 1, 99, 123_456_789_012_345] {
            assert_eq!(decode_cursor(&encode_cursor(position)).unwrap(), position);
        }
        assert_eq!(decode_cursor("YWZ0ZXI6MTI=").unwrap(), 12);
        for bad in ["", "garbage", "YWZ0ZXI6LTE", "!!"] {
            assert!(decode_cursor(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn an_id_is_checked() {
        for id in ["o1", "a.b_c:d-e", &"x".repeat(256), "0"] {
            assert!(is_instance_id(id), "{id}");
        }
        for id in ["", "has space", "-leading", &"x".repeat(257), "é"] {
            assert!(!is_instance_id(id), "{id}");
        }
        assert_eq!(top_key("/a~1b"), Some("a/b".to_owned()));
        assert_eq!(top_key("/a/b"), None);
    }
}
