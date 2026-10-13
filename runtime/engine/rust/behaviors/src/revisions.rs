//! Revisions, the core's immutable revisions of an instance's own fields
//! (D16), the port of `src/behaviors/core/revisions.ts`. A create records
//! revision 1 and every change to the own fields the next, in the change's
//! transaction: an update, and an operation that changes them with
//! `update()`, an approval included. A revision holds the own fields as the
//! change left them, never a behavior's field, and is never changed or
//! removed while the instance exists. `listRevisions` reads them a page at
//! a time, oldest first, and `getRevision` one by its number; neither goes
//! through a write.
//!
//! With `review: { permission }` in its config, a change can go through a
//! reviewer. `propose` stores a JSON merge patch of the own fields as a
//! pending proposal, after checking it changes the instance and leaves it
//! valid; nothing else changes. `approve` applies a pending proposal's
//! patch with `update()`, so the update's validation and every guard run,
//! and records the revision it makes; `reject` settles it with an optional
//! reason. approve and reject need the review permission, which the guard
//! asks the deployment's matcher about (`can`); who may propose is the
//! access policy's call, which it gets as `write` with the operation
//! `propose`. `listProposals` is read-only and never takes the write path.
//! A patch applies to the instance as it is at approval, not as it was
//! proposed: `base` records the revision it was made against, so a
//! reviewer can see the instance moved since. Without review, the four
//! refuse (`no_review`), and approve and reject refuse a proposal that is
//! not pending (`not_pending`): vetoes with the codes the declaration
//! lists. With review, the `pendingProposals` field counts the proposals
//! still pending; without it the field is absent.
//!
//! A proposal may cite evidence: instances, each optionally at one of its
//! revisions, which propose checks as the proposer reads them (the target
//! exists, its schema is readable, and a cited revision is one the target,
//! whose schema composes Revisions, has had) and stores as data. Evidence
//! is no reference: the engine records none, so a target's later change or
//! delete leaves the proposal as it was proposed.
//!
//! Numbers (revisions, proposals) are per instance, 1, 2, 3, .... Deleting
//! the instance deletes its revisions and proposals.
//!
//! A revision's fields and a proposal's patch go through the engine's value
//! store (`values.stow`): a field whose JSON is longer than the engine's
//! threshold is stored once by hash, however many revisions, proposals,
//! events and the instance itself hold it, and the row keeps a ref with the
//! pointers to the refs beside it (`value_refs`). Every read puts the
//! values back. A row written before Revisions' second migration has none.
//!
//! Every statement is the same on SQLite and Postgres but the check that a
//! stored text is JSON (D58): SQLite's `json_valid`, Postgres's cast.
//!
//! configChange: review may be added, removed or given another permission.
//! Revisions can be added to a schema that has instances: each one's
//! history starts at its next change. It cannot be removed from one: the
//! history would stay behind with nothing to delete it.

use serde::Deserialize;
use serde_json::{Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, IndexSpec, InstanceCx, InstanceView, Page,
    TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::instances::patch::merge_patch;
use superschematic_engine::json::{js_equal, number_value, stringify};
use superschematic_engine::storage::{Row, Sql, SqlValue};
use superschematic_engine::{
    json_config, Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, OperationCx, Result,
};

const NAME: &str = "Revisions";

/// The operations of the review step.
const REVIEW_OPERATIONS: [&str; 4] = ["propose", "approve", "reject", "listProposals"];

const PROPOSAL_COLUMNS: &str = "proposal, patch, note, evidence, base, state, created_by, created_at, reviewed_by, reviewed_at, reason, revision, value_refs";

const REVISION_COLUMNS: &str = "revision, data, created_by, created_at, proposal, value_refs";

/// The review step of a Revisions config.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RevisionsReview {
    /// The permission a caller needs to approve or reject a proposal.
    pub permission: String,
}

/// Revisions' config.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RevisionsConfig {
    /// Turns on the review step: propose, approve, reject and
    /// listProposals.
    #[serde(default)]
    pub review: Option<RevisionsReview>,
}

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// key with more values after it.
fn keyed<C>(view: &InstanceView<'_, C>, more: impl IntoIterator<Item = SqlValue>) -> Vec<SqlValue> {
    let mut values = key(view);
    values.extend(more);
    values
}

// integer reads a parameter its paramsSchema holds to an integer, which
// JSON may write as 2 or 2.0.
fn integer(value: Option<&Value>) -> Option<i64> {
    match value {
        Some(Value::Number(number)) => {
            let read = number_value(number);
            (read.fract() == 0.0).then_some(read as i64)
        }
        _ => None,
    }
}

// The keys of a revision's and a proposal's rows in the value store.
fn revision_key(revision: i64) -> String {
    format!("revision {revision}")
}

fn proposal_key(proposal: i64) -> String {
    format!("proposal {proposal}")
}

// What reads a row's stowed object back: the context's value store.
type Load<'a> = &'a dyn Fn(&str, Option<&str>) -> Result<Map<String, Value>>;

// revision_of reads a revision row: revision, data, createdBy, createdAt,
// and proposal when an approval made it.
fn revision_of(load: Load<'_>, row: &Row) -> Result<Value> {
    let mut out = Map::new();
    out.insert("revision".into(), Value::from(row.i64("revision")?));
    out.insert(
        "data".into(),
        Value::Object(load(row.text("data")?, row.opt_text("value_refs")?)?),
    );
    out.insert("createdBy".into(), Value::from(row.text("created_by")?));
    out.insert("createdAt".into(), Value::from(row.i64("created_at")?));
    if let Some(proposal) = row.opt_i64("proposal")? {
        out.insert("proposal".into(), Value::from(proposal));
    }
    Ok(Value::Object(out))
}

// proposal_of reads a proposal row, leaving out each member with no value,
// as the TypeScript engine drops an undefined member from an operation's
// result.
fn proposal_of(load: Load<'_>, row: &Row) -> Result<Value> {
    let mut out = Map::new();
    out.insert("id".into(), Value::from(row.i64("proposal")?));
    out.insert(
        "patch".into(),
        Value::Object(load(row.text("patch")?, row.opt_text("value_refs")?)?),
    );
    if let Some(note) = row.opt_text("note")? {
        out.insert("note".into(), Value::from(note));
    }
    if let Some(evidence) = row.opt_text("evidence")? {
        let parsed: Value = serde_json::from_str(evidence).map_err(|error| {
            Error::behavior(NAME, format!("a proposal's evidence is not JSON: {error}"))
        })?;
        out.insert("evidence".into(), parsed);
    }
    if let Some(base) = row.opt_i64("base")? {
        out.insert("base".into(), Value::from(base));
    }
    out.insert("state".into(), Value::from(row.text("state")?));
    out.insert("createdBy".into(), Value::from(row.text("created_by")?));
    out.insert("createdAt".into(), Value::from(row.i64("created_at")?));
    if let Some(reviewed_by) = row.opt_text("reviewed_by")? {
        out.insert("reviewedBy".into(), Value::from(reviewed_by));
    }
    if let Some(reviewed_at) = row.opt_i64("reviewed_at")? {
        out.insert("reviewedAt".into(), Value::from(reviewed_at));
    }
    if let Some(reason) = row.opt_text("reason")? {
        out.insert("reason".into(), Value::from(reason));
    }
    if let Some(revision) = row.opt_i64("revision")? {
        out.insert("revision".into(), Value::from(revision));
    }
    Ok(Value::Object(out))
}

// latest is the instance's latest revision number, 0 before its first.
fn latest<C>(view: &InstanceView<'_, C>) -> Result<i64> {
    Ok(view.columns().get()?.i64("current")?)
}

// is_not_found is whether a read refused a schema the namespace lacks.
fn is_not_found(error: &Error) -> bool {
    error.code() == Some(EngineErrorCode::NotFound)
}

// evidence_of checks the evidence a proposal cites, as the proposer reads
// it: each target's schema and the target exist, the proposer may read the
// schema (instances.get asks, and a refusal is forbidden), and a cited
// revision is one the target has had, of a schema that composes Revisions.
// It returns the entries as they are stored.
fn evidence_of(cx: &InstanceCx<'_, RevisionsConfig>, entries: &[Value]) -> Result<Vec<Value>> {
    let mut issues = Vec::new();
    let mut out = Vec::new();
    for (index, entry) in entries.iter().enumerate() {
        let at = format!("/evidence/{index}");
        let schema = entry
            .get("schema")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let id = entry.get("id").and_then(Value::as_str).unwrap_or_default();
        let revision = integer(entry.get("revision"));
        let fields: &[&str] = if revision.is_some() {
            &["Revisions.revision"]
        } else {
            &[]
        };
        let target = match cx.instances().get(schema, id, Some(fields)) {
            Ok(target) => target,
            Err(error) if is_not_found(&error) => {
                issues.push(SchemaIssue::new(
                    format!("{at}/schema"),
                    format!("{schema} is not a schema of namespace {}", cx.namespace()),
                ));
                continue;
            }
            Err(error) => return Err(error),
        };
        let Some(target) = target else {
            issues.push(SchemaIssue::new(
                format!("{at}/id"),
                format!("{schema} {id} does not exist"),
            ));
            continue;
        };
        if let Some(revision) = revision {
            if cx.schemas().config(schema, NAME)?.is_none() {
                issues.push(SchemaIssue::new(
                    format!("{at}/revision"),
                    format!("{schema} does not compose Revisions, so {schema} {id} has no revision to cite"),
                ));
                continue;
            }
            let current = target
                .behavior_field(NAME, "revision")
                .and_then(Value::as_number)
                .map_or(0, |number| number_value(number) as i64);
            if revision > current {
                issues.push(SchemaIssue::new(
                    format!("{at}/revision"),
                    if current == 0 {
                        format!("{schema} {id} has no revision yet")
                    } else {
                        format!("{schema} {id} has revisions 1 to {current}, not {revision}")
                    },
                ));
                continue;
            }
        }
        let mut stored = Map::new();
        stored.insert("schema".into(), Value::from(schema));
        stored.insert("id".into(), Value::from(id));
        if let Some(revision) = revision {
            stored.insert("revision".into(), Value::from(revision));
        }
        out.push(Value::Object(stored));
    }
    if !issues.is_empty() {
        return Err(EngineError::operation_params(NAME, "propose", issues).into());
    }
    Ok(out)
}

// record stores the instance's own fields as its next revision.
fn record(cx: &InstanceCx<'_, RevisionsConfig>, proposal: Option<i64>) -> Result<i64> {
    let revision = latest(cx)? + 1;
    let data = cx.data()?;
    let stowed = cx.values().stow(&revision_key(revision), &data)?;
    let table = cx.sql().table("revisions")?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, revision, data, created_by, created_at, proposal, value_refs)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(revision),
                SqlValue::from(stowed.json),
                SqlValue::from(cx.principal().subject.as_str()),
                SqlValue::Integer(cx.now()),
                SqlValue::from(proposal),
                SqlValue::from(stowed.refs),
            ],
        ),
    )?;
    cx.columns()
        .set(&[("current", SqlValue::Integer(revision))])?;
    Ok(revision)
}

// proposal_row reads one of the instance's proposals.
fn proposal_row(view: &InstanceView<'_, RevisionsConfig>, id: i64) -> Result<Option<Row>> {
    let table = view.sql().table("proposals")?;
    view.sql().get(
        format!("SELECT {PROPOSAL_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?"),
        &keyed(view, [SqlValue::Integer(id)]),
    )
}

// pending loads a proposal an approve or reject names, which must be
// pending.
fn pending(cx: &InstanceCx<'_, RevisionsConfig>, operation: &str, id: i64) -> Result<Row> {
    let Some(row) = proposal_row(cx, id)? else {
        return Err(EngineError::operation_params(
            NAME,
            operation,
            vec![SchemaIssue::new(
                "/proposal",
                format!("{} {} has no proposal {id}", cx.schema(), cx.id()),
            )],
        )
        .into());
    };
    let state = row.text("state")?.to_owned();
    if state != "pending" {
        let mut details = Map::new();
        details.insert("proposal".into(), Value::from(id));
        details.insert("state".into(), Value::from(state.as_str()));
        return Err(Error::veto(
            NAME,
            operation,
            cx.schema(),
            cx.id(),
            Veto {
                reason: format!("proposal {id} is {state}, not pending"),
                code: Some("not_pending".to_owned()),
                details: Some(details),
            },
        ));
    }
    Ok(row)
}

// settle records a reviewer's decision on a proposal and returns it.
fn settle(
    cx: &InstanceCx<'_, RevisionsConfig>,
    id: i64,
    state: &str,
    reason: Option<&str>,
    revision: Option<i64>,
) -> Result<Value> {
    let table = cx.sql().table("proposals")?;
    let mut values = vec![
        SqlValue::from(state),
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::Integer(cx.now()),
        SqlValue::from(reason),
        SqlValue::from(revision),
    ];
    values.extend(keyed(cx, [SqlValue::Integer(id)]));
    cx.sql().run(
        format!(
            "UPDATE {table} SET state = ?, reviewed_by = ?, reviewed_at = ?, reason = ?, revision = ?
             WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?"
        ),
        &values,
    )?;
    let row = proposal_row(cx, id)?
        .ok_or_else(|| Error::behavior(NAME, format!("proposal {id} is gone after its update")))?;
    proposal_of(&|json, refs| cx.values().load(json, refs), &row)
}

// The texts a row keeps as JSON: SQLite checks one with json_valid,
// Postgres by casting it.
const DATA_IS_JSON: Sql<'static> = Sql::Per {
    sqlite: std::borrow::Cow::Borrowed("json_valid(data)"),
    postgres: std::borrow::Cow::Borrowed("data::json IS NOT NULL"),
};
const PATCH_IS_JSON: Sql<'static> = Sql::Per {
    sqlite: std::borrow::Cow::Borrowed("json_valid(patch)"),
    postgres: std::borrow::Cow::Borrowed("patch::json IS NOT NULL"),
};

fn migrations() -> Vec<BehaviorMigration> {
    let text = || ColumnSpec::text().not_null();
    let integer = || ColumnSpec::integer().not_null();
    vec![
        BehaviorMigration::new(1, "revisions and proposals")
            .column("current", ColumnSpec::integer().not_null().default(0))
            .table(
                TableSpec::new("revisions")
                    .column("namespace", text())
                    .column("schema", text())
                    .column("id", text())
                    .column("revision", integer())
                    .column("data", text().check(DATA_IS_JSON))
                    .column("created_by", text())
                    .column("created_at", integer())
                    .column("proposal", ColumnSpec::integer())
                    .primary_key(&["namespace", "schema", "id", "revision"]),
            )
            .table(
                TableSpec::new("proposals")
                    .column("namespace", text())
                    .column("schema", text())
                    .column("id", text())
                    .column("proposal", integer())
                    .column("patch", text().check(PATCH_IS_JSON))
                    .column("note", ColumnSpec::text())
                    .column("base", ColumnSpec::integer())
                    .column(
                        "state",
                        text().check("state IN ('pending', 'approved', 'rejected')"),
                    )
                    .column("created_by", text())
                    .column("created_at", integer())
                    .column("reviewed_by", ColumnSpec::text())
                    .column("reviewed_at", ColumnSpec::integer())
                    .column("reason", ColumnSpec::text())
                    .column("revision", ColumnSpec::integer())
                    .primary_key(&["namespace", "schema", "id", "proposal"])
                    .index(IndexSpec::new(
                        "proposals_by_state",
                        &["namespace", "schema", "id", "state", "proposal"],
                    )),
            ),
        BehaviorMigration::new(2, "large fields by hash").up(|sql| {
            for table in ["revisions", "proposals"] {
                sql.run(
                    format!(
                        "ALTER TABLE {} ADD COLUMN value_refs TEXT",
                        sql.table(table)?
                    ),
                    &[],
                )?;
            }
            Ok(())
        }),
        BehaviorMigration::new(3, "evidence").up(|sql| {
            sql.run(
                format!(
                    "ALTER TABLE {} ADD COLUMN evidence TEXT",
                    sql.table("proposals")?
                ),
                &[],
            )?;
            Ok(())
        }),
    ]
}

/// Revisions: the history of an instance's own fields, with an optional
/// review step.
#[derive(Clone, Copy, Debug, Default)]
pub struct Revisions;

impl Behavior for Revisions {
    type Config = RevisionsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Revisions.behavior.json")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<RevisionsConfig> {
        json_config(raw)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the revisions and proposals its instances have would stay behind with nothing to delete them"
                        .to_owned()
                }))
            }),
            guidance: Some(|_, config, target| {
                crate::guidance::revisions::guidance(config, target)
            }),
            migrations: migrations(),
            guard: Some(guard),
            operations: vec![
                ("listRevisions", list_revisions),
                ("getRevision", get_revision),
                ("propose", propose),
                ("approve", |_, cx, params| {
                    let id = integer(params.get("proposal")).unwrap_or_default();
                    let row = pending(cx, "approve", id)?;
                    let before = cx.data()?;
                    let patch = cx
                        .values()
                        .load(row.text("patch")?, row.opt_text("value_refs")?)?;
                    cx.update(&Value::Object(patch))?;
                    // The revision the approval makes; afterChange sees it
                    // recorded.
                    let after = cx.data()?;
                    let revision = if js_equal(
                        &Value::Object((*before).clone()),
                        &Value::Object((*after).clone()),
                    ) {
                        latest(cx)?
                    } else {
                        record(cx, Some(id))?
                    };
                    settle(cx, id, "approved", None, (revision > 0).then_some(revision))
                }),
                ("reject", |_, cx, params| {
                    let id = integer(params.get("proposal")).unwrap_or_default();
                    pending(cx, "reject", id)?;
                    settle(
                        cx,
                        id,
                        "rejected",
                        params.get("reason").and_then(Value::as_str),
                        None,
                    )
                }),
                ("listProposals", list_proposals),
            ],
            fields: vec![
                ("revision", |_, view| {
                    let revision = latest(view)?;
                    Ok((revision > 0).then(|| Value::from(revision)))
                }),
                // The proposals still pending, through the index by state;
                // absent without a review step, where there are none to
                // count.
                ("pendingProposals", |_, view| {
                    if view.config().review.is_none() {
                        return Ok(None);
                    }
                    let table = view.sql().table("proposals")?;
                    let row = view.sql().get(
                        format!("SELECT COUNT(*) AS pending FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state = 'pending'"),
                        &key(view),
                    )?;
                    Ok(Some(Value::from(match row {
                        Some(row) => row.i64("pending")?,
                        None => 0,
                    })))
                }),
            ],
            after_change: Some(after_change),
            ..Hooks::default()
        }
    }
}

// guard is the review step's: its operations need review in the config,
// and approve and reject the review permission, whoever calls them.
fn guard(
    _: &Revisions,
    view: &InstanceView<'_, RevisionsConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Operation {
        behavior: NAME,
        operation,
        ..
    } = request
    else {
        return Ok(None);
    };
    if !REVIEW_OPERATIONS.contains(operation) {
        return Ok(None);
    }
    let Some(review) = &view.config().review else {
        return Ok(Some(Veto {
            reason: format!(
                "{} has no review step: its Revisions config sets no review",
                view.schema()
            ),
            code: Some("no_review".to_owned()),
            details: None,
        }));
    };
    if matches!(*operation, "approve" | "reject") && !view.can(&review.permission)? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {operation} a proposal on {} {}: it needs permission {}",
                view.principal().subject,
                view.schema(),
                view.id(),
                review.permission
            ),
        )
        .into());
    }
    Ok(None)
}

fn list_revisions(
    _: &Revisions,
    cx: &OperationCx<'_, RevisionsConfig>,
    params: &Value,
) -> Result<Value> {
    let request = page_request(NAME, "listRevisions", params)?;
    let table = cx.sql().table("revisions")?;
    let rows = cx.sql().all(
        format!(
            "SELECT {REVISION_COLUMNS} FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND revision > ?
             ORDER BY revision LIMIT ?"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(request.after),
                SqlValue::Integer(request.limit + 1),
            ],
        ),
    )?;
    let load = |json: &str, refs: Option<&str>| cx.values().load(json, refs);
    let records = rows
        .iter()
        .map(|row| Ok((row.i64("revision")?, revision_of(&load, row)?)))
        .collect::<Result<Vec<_>>>()?;
    let found = page(records, request.limit, |(revision, _)| *revision);
    Ok(Page {
        items: found.items.into_iter().map(|(_, record)| record).collect(),
        next: found.next,
    }
    .to_json())
}

// getRevision reads one revision through the value store, as
// listRevisions reads a page of them.
fn get_revision(
    _: &Revisions,
    cx: &OperationCx<'_, RevisionsConfig>,
    params: &Value,
) -> Result<Value> {
    let revision = integer(params.get("revision")).unwrap_or_default();
    let table = cx.sql().table("revisions")?;
    let row = cx.sql().get(
        format!("SELECT {REVISION_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND revision = ?"),
        &keyed(cx, [SqlValue::Integer(revision)]),
    )?;
    let Some(row) = row else {
        let current = latest(cx)?;
        return Err(EngineError::new(
            EngineErrorCode::NotFound,
            format!(
                "{} {} has {}, not revision {revision}",
                cx.schema(),
                cx.id(),
                if current == 0 {
                    "no revision yet".to_owned()
                } else {
                    format!("revisions 1 to {current}")
                }
            ),
        )
        .into());
    };
    revision_of(&|json, refs| cx.values().load(json, refs), &row)
}

fn propose(_: &Revisions, cx: &OperationCx<'_, RevisionsConfig>, params: &Value) -> Result<Value> {
    let patch = params.get("patch").cloned().unwrap_or(Value::Null);
    let issues = cx.validate_update(&patch)?;
    if !issues.is_empty() {
        return Err(EngineError::instance_validation(
            cx.namespace(),
            cx.schema(),
            cx.version(),
            issues,
        )
        .into());
    }
    let data = Value::Object((*cx.data()?).clone());
    if js_equal(&merge_patch(&data, &patch), &data) {
        return Err(EngineError::operation_params(
            NAME,
            "propose",
            vec![SchemaIssue::new(
                "/patch",
                format!(
                    "changes nothing: {} {} already has these fields",
                    cx.schema(),
                    cx.id()
                ),
            )],
        )
        .into());
    }
    let evidence = match params.get("evidence").and_then(Value::as_array) {
        Some(entries) => Some(evidence_of(cx, entries)?),
        None => None,
    };
    let table = cx.sql().table("proposals")?;
    let last = cx.sql().get(
        format!("SELECT MAX(proposal) AS last FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
        &key(cx),
    )?;
    let id = match last {
        Some(row) => row.opt_i64("last")?.unwrap_or(0) + 1,
        None => 1,
    };
    let base = latest(cx)?;
    let empty = Map::new();
    let stowed = cx
        .values()
        .stow(&proposal_key(id), patch.as_object().unwrap_or(&empty))?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, proposal, patch, note, evidence, base, state, created_by, created_at, value_refs)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::from(stowed.json),
                SqlValue::from(params.get("note").and_then(Value::as_str)),
                SqlValue::from(evidence.map(|entries| stringify(&Value::Array(entries)))),
                SqlValue::from((base > 0).then_some(base)),
                SqlValue::from(cx.principal().subject.as_str()),
                SqlValue::Integer(cx.now()),
                SqlValue::from(stowed.refs),
            ],
        ),
    )?;
    let row = proposal_row(cx, id)?
        .ok_or_else(|| Error::behavior(NAME, format!("proposal {id} is gone after its insert")))?;
    proposal_of(&|json, refs| cx.values().load(json, refs), &row)
}

fn list_proposals(
    _: &Revisions,
    cx: &OperationCx<'_, RevisionsConfig>,
    params: &Value,
) -> Result<Value> {
    let request = page_request(NAME, "listProposals", params)?;
    let state = params.get("state").and_then(Value::as_str);
    let table = cx.sql().table("proposals")?;
    let mut values = keyed(cx, [SqlValue::Integer(request.after)]);
    if let Some(state) = state {
        values.push(SqlValue::from(state));
    }
    values.push(SqlValue::Integer(request.limit + 1));
    let rows = cx.sql().all(
        format!(
            "SELECT {PROPOSAL_COLUMNS} FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND proposal > ?{}
             ORDER BY proposal LIMIT ?",
            if state.is_some() {
                " AND state = ?"
            } else {
                ""
            }
        ),
        &values,
    )?;
    let load = |json: &str, refs: Option<&str>| cx.values().load(json, refs);
    let records = rows
        .iter()
        .map(|row| Ok((row.i64("proposal")?, proposal_of(&load, row)?)))
        .collect::<Result<Vec<_>>>()?;
    let found = page(records, request.limit, |(id, _)| *id);
    Ok(Page {
        items: found.items.into_iter().map(|(_, record)| record).collect(),
        next: found.next,
    }
    .to_json())
}

fn after_change(
    _: &Revisions,
    cx: &InstanceCx<'_, RevisionsConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    match change {
        InstanceChange::Create | InstanceChange::Update { .. } => {
            record(cx, None)?;
        }
        InstanceChange::Operation { before: None, .. } => {}
        InstanceChange::Operation { .. } => {
            // An approval records its own revision, with its proposal.
            let table = cx.sql().table("revisions")?;
            let last = cx.sql().get(
                format!("SELECT data, value_refs FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND revision = ?"),
                &keyed(cx, [SqlValue::Integer(latest(cx)?)]),
            )?;
            let recorded = match last {
                Some(row) => {
                    let data = cx
                        .values()
                        .load(row.text("data")?, row.opt_text("value_refs")?)?;
                    js_equal(&Value::Object(data), &Value::Object((*cx.data()?).clone()))
                }
                None => false,
            };
            if !recorded {
                record(cx, None)?;
            }
        }
        InstanceChange::Delete => {
            for table in ["revisions", "proposals"] {
                let table = cx.sql().table(table)?;
                cx.sql().run(
                    format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
                    &key(cx),
                )?;
            }
            cx.values().release(None)?;
        }
    }
    Ok(())
}
