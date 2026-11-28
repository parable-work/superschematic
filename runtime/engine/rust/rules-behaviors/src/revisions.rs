//! Revisions, the history of a row's own fields with an optional review
//! step (D16), ported from the engine's `revisions.rs` with its rules and
//! words. A create records revision 1, and every change of the own fields
//! the next, in the change's own command: an update, and an operation that
//! writes them with [`Cx::update`], an approval among them. A revision
//! holds the own fields as the change left them, never a behavior's field,
//! and is never changed or removed while the row exists. `listRevisions`
//! reads them a page at a time, oldest first, and `getRevision` one by its
//! number; neither writes.
//!
//! With `review: { permission }` in its config, a change can go through a
//! reviewer. `propose` keeps a JSON merge patch of the own fields as a
//! pending proposal, after holding it to an update's validation
//! ([`Cx::validate_update`]) and to changing the row; nothing else changes.
//! `approve` applies a pending proposal's patch with [`Cx::update`], so an
//! update's validation and every use's guard run, and records the revision
//! it makes with the proposal's number; `reject` settles it with an
//! optional reason. approve and reject need the review permission, which
//! the guard asks the deployment's matcher about; who may propose is the
//! host's access policy's call. `listProposals` is read-only. A patch
//! applies to the row as it is at approval, not as it was proposed: `base`
//! records the revision it was made against, so a reviewer sees the row
//! moved since. Without review the four refuse (`no_review`), and approve
//! and reject refuse a proposal that is not pending (`not_pending`). With
//! review, `pendingProposals`, computed at a read, counts the proposals
//! still pending; without it the field is absent.
//!
//! A proposal may cite evidence: rows, each at one of its revisions or at
//! none, which propose checks as the run reads them (the type exists, so
//! does the row, and a cited revision is one the row, whose type composes
//! Revisions, has had) and keeps as given. Evidence is no reference: a later
//! change or delete of a row cited leaves the proposal as it was proposed.
//! Whether the proposer may read a type it cites is the host's access
//! policy's, which the core does not hold.
//!
//! The state is the use's: the owned field `revision`, the latest number,
//! absent before the first; and two sub-tables of the row, `revisions` and
//! `proposals`, each keyed by its number in ten digits, zero-padded, so the
//! key's order is theirs. Numbers are per row, 1, 2, 3, .... A delete of
//! the row deletes both, as the host deletes every sub-table row of a
//! deleted row.
//!
//! Revisions is held to D103's test of a behavior. Its review step passes
//! it: proposals are state of its own, and its rules say how that state
//! changes (a proposal is pending until a reviewer who holds the
//! permission approves it, which applies it through every rule an update
//! meets, or rejects it, and a settled one stays settled), and review
//! recurs (a document's, a policy's, a price list's). Its revision log
//! alone is a storage shape: a snapshot of the own fields after each
//! change, with who and when, is what `@versioned`'s history table keeps
//! (on Postgres), and listing it is a query. The core keeps the log here
//! because an approval records the revision it made and a proposal its
//! base, and the build's `@versioned` is not on SQLite or D1; the fold the
//! port's report recommends is Revisions' review over `@versioned`'s
//! history where both exist.
//!
//! configChange: review may be added, removed or given another
//! permission. Revisions can be added to a table that has rows: each one's
//! history starts at its next change. It cannot be removed from one: the
//! revisions and proposals its rows have would stay behind with nothing to
//! delete them.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    merge_patch, Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration,
    GuardRequest, Hook, Refusal, Row, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::declared;
use crate::paging::{page, page_request, Page};
use crate::schema::{self, Issues};
use crate::values;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Revisions";

/// Revisions' declaration, read in place: the copy `make behaviors` writes of
/// the one the compiler registers.
pub const DECLARATION: &str = include_str!("../declarations/Revisions.behavior.json");

/// The owned field that holds the row's latest revision.
pub const REVISION: &str = "revision";
/// The computed field that counts the pending proposals.
pub const PENDING_PROPOSALS: &str = "pendingProposals";
/// The sub-table of the row's revisions.
pub const REVISIONS: &str = "revisions";
/// The sub-table of the row's proposals.
pub const PROPOSALS: &str = "proposals";

const LIST_REVISIONS: &str = "listRevisions";
const GET_REVISION: &str = "getRevision";
const PROPOSE: &str = "propose";
const APPROVE: &str = "approve";
const REJECT: &str = "reject";
const LIST_PROPOSALS: &str = "listProposals";

/// The operations of the review step.
const REVIEW_OPERATIONS: [&str; 4] = [PROPOSE, APPROVE, REJECT, LIST_PROPOSALS];

/// The states a proposal is in.
const STATES: [&str; 3] = ["pending", "approved", "rejected"];

// The paramsSchema's bounds.
const MAX_LIMIT: i64 = 500;
const MAX_TEXT: usize = 10_000;
const MAX_EVIDENCE: usize = 64;
const NUMBER_WIDTH: usize = 10;

/// The review step of a Revisions config.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RevisionsReview {
    /// The permission a caller needs to approve or reject a proposal.
    pub permission: String,
}

/// Revisions' config.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RevisionsConfig {
    /// Turns on the review step: propose, approve, reject and
    /// listProposals.
    pub review: Option<RevisionsReview>,
}

impl RevisionsConfig {
    /// Reads a config held to the declaration's configSchema, in its words.
    pub fn parse(raw: &Value) -> Result<RevisionsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let review = raw
            .get("review")
            .and_then(|review| review.get("permission"))
            .and_then(Value::as_str)
            .map(|permission| RevisionsReview {
                permission: permission.to_owned(),
            });
        Ok(RevisionsConfig { review })
    }
}

/// Revisions: the history of a row's own fields, with an optional review
/// step.
#[derive(Clone, Copy, Debug, Default)]
pub struct Revisions;

impl Behavior for Revisions {
    type Config = RevisionsConfig;

    // The declaration file names the owned field revision, the computed
    // pendingProposals and the sub-tables revisions and proposals.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<RevisionsConfig, ConfigError> {
        RevisionsConfig::parse(raw)
    }

    // See the module's header: removed from a table with rows, its history
    // would stay behind.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&RevisionsConfig>,
        after: Option<&RevisionsConfig>,
    ) -> Hook<Option<String>> {
        if before.is_none() || after.is_some() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(
            "the revisions and proposals its instances have would stay behind with nothing to delete them"
                .to_owned(),
        ))
    }

    fn check_params(
        &self,
        _config: &RevisionsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // The review step's operations need review in the config, and approve
    // and reject the review permission, whoever calls them.
    fn guard(
        &self,
        view: &View<'_, RevisionsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let GuardRequest::Operation {
            use_id, operation, ..
        } = request
        else {
            return Ok(None);
        };
        if *use_id != view.use_id() || !REVIEW_OPERATIONS.contains(operation) {
            return Ok(None);
        }
        let Some(review) = &view.config().review else {
            return Ok(Some(
                Veto::new(format!(
                    "{} has no review step: its Revisions config sets no review",
                    view.type_name()
                ))
                .with_code("no_review"),
            ));
        };
        if matches!(*operation, APPROVE | REJECT) && !view.can(&review.permission)? {
            return Err(view.forbidden(format!(
                "{} may not {operation} a proposal on {}: it needs permission {}",
                view.caller().subject,
                row(view),
                review.permission
            )));
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, RevisionsConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            LIST_REVISIONS => list_revisions(cx, params),
            GET_REVISION => get_revision(cx, params),
            PROPOSE => propose(cx, params),
            APPROVE => approve(cx, params),
            REJECT => reject(cx, params),
            LIST_PROPOSALS => list_proposals(cx, params),
            _ => Err(cx.defect(format!("{name} is not an operation of Revisions"))),
        }
    }

    // A create records revision 1, and each change of the own fields the
    // next: an update that changed them, and an operation that wrote them
    // with update(), unless the operation recorded the revision itself (an
    // approval, by its proposal). A change of nothing records none.
    fn after_change(&self, cx: &Cx<'_, RevisionsConfig>, change: &Change<'_>) -> Hook<()> {
        match change {
            Change::Create => {
                record(cx, None)?;
            }
            Change::Update { before, .. } => {
                if cx.own() != **before {
                    record(cx, None)?;
                }
            }
            Change::Operation {
                before: Some(before),
                ..
            } => {
                let own = cx.own();
                if own == **before {
                    return Ok(());
                }
                let rows = cx.sub_rows(REVISIONS)?;
                let recorded = rows
                    .iter()
                    .max_by_key(|row| number_of(row))
                    .is_some_and(|latest| {
                        values::equal(latest.value("data"), Some(&Value::Object(own.clone())))
                    });
                if !recorded {
                    record(cx, None)?;
                }
            }
            _ => {}
        }
        Ok(())
    }

    // pendingProposals counts the proposals still pending, with review in
    // the config; without it the field is absent, as there are none to
    // count.
    fn read_fields(
        &self,
        config: &RevisionsConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let mut fields = Map::new();
        if config.review.is_some() {
            let pending = tables.get(PROPOSALS).map_or(0, |rows| {
                rows.iter()
                    .filter(|row| row.value("state").and_then(Value::as_str) == Some("pending"))
                    .count()
            });
            fields.insert(PENDING_PROPOSALS.into(), Value::from(pending));
        }
        fields
    }
}

// latest is the row's latest revision number, 0 before its first.
fn latest(view: &View<'_, RevisionsConfig>) -> i64 {
    view.get(REVISION)
        .and_then(|revision| whole(&revision))
        .unwrap_or(0)
}

// record keeps the row's own fields as its next revision, with the
// proposal whose approval made it.
fn record(cx: &Cx<'_, RevisionsConfig>, proposal: Option<i64>) -> Hook<i64> {
    let revision = latest(cx) + 1;
    let mut values = Map::new();
    values.insert("data".into(), Value::Object(cx.own()));
    values.insert(
        "createdBy".into(),
        Value::from(cx.caller().subject.as_str()),
    );
    values.insert("createdAt".into(), Value::from(cx.now()));
    if let Some(proposal) = proposal {
        values.insert("proposal".into(), Value::from(proposal));
    }
    cx.insert_row(REVISIONS, &key_of(revision), values)?;
    cx.set(REVISION, revision)?;
    Ok(revision)
}

// revision_of is a revision as the operations return it: revision, data,
// createdBy, createdAt, and proposal when an approval made it.
fn revision_of(row: &SubRow) -> Value {
    let mut out = Map::new();
    out.insert(
        "revision".into(),
        Value::from(number_of(row).unwrap_or_default()),
    );
    out.insert(
        "data".into(),
        row.value("data")
            .cloned()
            .unwrap_or_else(|| Value::Object(Map::new())),
    );
    for member in ["createdBy", "createdAt", "proposal"] {
        if let Some(value) = row.value(member) {
            out.insert(member.into(), value.clone());
        }
    }
    Value::Object(out)
}

// proposal_of is a proposal as the operations return it, each member with
// no value left out, as the engine dropped an undefined member.
fn proposal_of(row: &SubRow) -> Value {
    let mut out = Map::new();
    out.insert("id".into(), Value::from(number_of(row).unwrap_or_default()));
    for member in [
        "patch",
        "note",
        "evidence",
        "base",
        "state",
        "createdBy",
        "createdAt",
        "reviewedBy",
        "reviewedAt",
        "reason",
        "revision",
    ] {
        if let Some(value) = row.value(member) {
            out.insert(member.into(), value.clone());
        }
    }
    Value::Object(out)
}

fn list_revisions(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let request =
        page_request(params).map_err(|issue| params_refusal(cx, LIST_REVISIONS, issue))?;
    let rows: Vec<SubRow> = cx
        .sub_rows(REVISIONS)?
        .into_iter()
        .filter(|row| number_of(row).is_some_and(|number| number > request.after))
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(rows, request.limit, |row| {
        number_of(row).unwrap_or_default()
    });
    Ok(Page {
        items: found.items.iter().map(revision_of).collect(),
        next: found.next,
    }
    .into_json())
}

// getRevision reads one revision; one the row has not reached is
// not_found.
fn get_revision(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let revision = params.get("revision").and_then(whole).unwrap_or_default();
    let rows = cx.sub_rows(REVISIONS)?;
    if let Some(found) = rows.iter().find(|row| number_of(row) == Some(revision)) {
        return Ok(revision_of(found));
    }
    let current = latest(cx);
    Err(Refusal::not_found(format!(
        "{} has {}, not revision {revision}",
        row(cx),
        if current == 0 {
            "no revision yet".to_owned()
        } else {
            format!("revisions 1 to {current}")
        }
    ))
    .into())
}

fn propose(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let patch = params
        .get("patch")
        .cloned()
        .unwrap_or_else(|| Value::Object(Map::new()));
    cx.validate_update(&patch)?;
    let own = cx.own();
    let empty = Map::new();
    if merge_patch(&own, patch.as_object().unwrap_or(&empty)) == own {
        return Err(params_refusal(
            cx,
            PROPOSE,
            SchemaIssue::new(
                "/patch",
                format!("changes nothing: {} already has these fields", row(cx)),
            ),
        ));
    }
    let evidence = match params.get("evidence").and_then(Value::as_array) {
        Some(entries) => Some(evidence_of(cx, entries)?),
        None => None,
    };
    let rows = cx.sub_rows(PROPOSALS)?;
    let id = rows.iter().filter_map(number_of).max().unwrap_or(0) + 1;
    let base = latest(cx);
    let mut values = Map::new();
    values.insert("patch".into(), patch);
    if let Some(note) = params.get("note").and_then(Value::as_str) {
        values.insert("note".into(), Value::from(note));
    }
    if let Some(evidence) = evidence {
        values.insert("evidence".into(), Value::Array(evidence));
    }
    if base > 0 {
        values.insert("base".into(), Value::from(base));
    }
    values.insert("state".into(), Value::from("pending"));
    values.insert(
        "createdBy".into(),
        Value::from(cx.caller().subject.as_str()),
    );
    values.insert("createdAt".into(), Value::from(cx.now()));
    cx.insert_row(PROPOSALS, &key_of(id), values.clone())?;
    Ok(proposal_of(&SubRow::new(key_of(id), values)))
}

// evidence_of checks the rows a proposal cites as the run reads them: each
// one's type and the row exist, and a cited revision is one the row, whose
// type composes Revisions, has had. It returns the entries as they are
// kept.
fn evidence_of(cx: &Cx<'_, RevisionsConfig>, entries: &[Value]) -> Hook<Vec<Value>> {
    let mut issues = Vec::new();
    let mut out = Vec::new();
    for (index, entry) in entries.iter().enumerate() {
        let at = format!("/evidence/{index}");
        let type_name = entry
            .get("schema")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let id = entry.get("id").and_then(Value::as_str).unwrap_or_default();
        let revision = entry.get("revision").and_then(whole);
        let Some(spec) = cx.read_spec(type_name)? else {
            issues.push(SchemaIssue::new(
                format!("{at}/schema"),
                format!("{type_name} is not a type of this database"),
            ));
            continue;
        };
        let Some(target) = cx.row_of(type_name, id)? else {
            issues.push(SchemaIssue::new(
                format!("{at}/id"),
                format!("{type_name} {id} does not exist"),
            ));
            continue;
        };
        if let Some(revision) = revision {
            let Some(use_id) = spec
                .uses
                .iter()
                .find(|entry| entry.behavior == NAME)
                .map(|entry| entry.id().to_owned())
            else {
                issues.push(SchemaIssue::new(
                    format!("{at}/revision"),
                    format!("{type_name} does not compose Revisions, so {type_name} {id} has no revision to cite"),
                ));
                continue;
            };
            let current = target
                .owned
                .get(&use_id)
                .and_then(|fields| fields.get(REVISION))
                .and_then(whole)
                .unwrap_or(0);
            if revision > current {
                issues.push(SchemaIssue::new(
                    format!("{at}/revision"),
                    if current == 0 {
                        format!("{type_name} {id} has no revision yet")
                    } else {
                        format!("{type_name} {id} has revisions 1 to {current}, not {revision}")
                    },
                ));
                continue;
            }
        }
        let mut kept = Map::new();
        kept.insert("schema".into(), Value::from(type_name));
        kept.insert("id".into(), Value::from(id));
        if let Some(revision) = revision {
            kept.insert("revision".into(), Value::from(revision));
        }
        out.push(Value::Object(kept));
    }
    if !issues.is_empty() {
        return Err(Refusal::operation_params(cx.behavior(), cx.use_id(), PROPOSE, issues).into());
    }
    Ok(out)
}

// pending is the proposal an approve or reject names, which must be one
// of the row's and pending.
fn pending(cx: &Cx<'_, RevisionsConfig>, operation: &str, id: i64) -> Hook<SubRow> {
    let rows = cx.sub_rows(PROPOSALS)?;
    let Some(found) = rows.into_iter().find(|row| number_of(row) == Some(id)) else {
        return Err(params_refusal(
            cx,
            operation,
            SchemaIssue::new("/proposal", format!("{} has no proposal {id}", row(cx))),
        ));
    };
    let state = found
        .value("state")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    if state != "pending" {
        let mut details = Map::new();
        details.insert("proposal".into(), Value::from(id));
        details.insert("state".into(), Value::from(state.as_str()));
        return Err(cx.veto(
            Veto::new(format!("proposal {id} is {state}, not pending"))
                .with_code("not_pending")
                .with_details(details),
        ));
    }
    Ok(found)
}

// approve applies a pending proposal's patch as an update an operation
// makes, so the update's validation and every guard run, records the
// revision it makes with the proposal, and settles it; a patch the row
// already has makes no revision, and the proposal names the latest.
fn approve(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let id = params.get("proposal").and_then(whole).unwrap_or_default();
    let found = pending(cx, APPROVE, id)?;
    let patch = found
        .value("patch")
        .cloned()
        .unwrap_or_else(|| Value::Object(Map::new()));
    let revision = if cx.update(&patch)? {
        record(cx, Some(id))?
    } else {
        latest(cx)
    };
    settle(
        cx,
        found,
        "approved",
        None,
        (revision > 0).then_some(revision),
    )
}

fn reject(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let id = params.get("proposal").and_then(whole).unwrap_or_default();
    let found = pending(cx, REJECT, id)?;
    let reason = params.get("reason").and_then(Value::as_str);
    settle(cx, found, "rejected", reason, None)
}

// settle records a reviewer's decision on a proposal and returns it.
fn settle(
    cx: &Cx<'_, RevisionsConfig>,
    found: SubRow,
    state: &str,
    reason: Option<&str>,
    revision: Option<i64>,
) -> Hook<Value> {
    let mut values = Map::new();
    values.insert("state".into(), Value::from(state));
    values.insert(
        "reviewedBy".into(),
        Value::from(cx.caller().subject.as_str()),
    );
    values.insert("reviewedAt".into(), Value::from(cx.now()));
    if let Some(reason) = reason {
        values.insert("reason".into(), Value::from(reason));
    }
    if let Some(revision) = revision {
        values.insert("revision".into(), Value::from(revision));
    }
    cx.update_row(PROPOSALS, &found.key, values.clone())?;
    let mut settled = found.values;
    settled.extend(values);
    Ok(proposal_of(&SubRow::new(found.key, settled)))
}

fn list_proposals(cx: &Cx<'_, RevisionsConfig>, params: &Value) -> Hook<Value> {
    let request =
        page_request(params).map_err(|issue| params_refusal(cx, LIST_PROPOSALS, issue))?;
    let only = params.get("state").and_then(Value::as_str);
    let rows: Vec<SubRow> = cx
        .sub_rows(PROPOSALS)?
        .into_iter()
        .filter(|row| number_of(row).is_some_and(|number| number > request.after))
        .filter(|row| {
            only.is_none_or(|state| row.value("state").and_then(Value::as_str) == Some(state))
        })
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(rows, request.limit, |row| {
        number_of(row).unwrap_or_default()
    });
    Ok(Page {
        items: found.items.iter().map(proposal_of).collect(),
        next: found.next,
    }
    .into_json())
}

fn params_refusal<C>(view: &View<'_, C>, operation: &str, issue: SchemaIssue) -> Stop {
    Refusal::operation_params(view.behavior(), view.use_id(), operation, vec![issue]).into()
}

// row names the row a message is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// number_of is a revision's or a proposal's number, from its key.
fn number_of(row: &SubRow) -> Option<i64> {
    row.key.parse().ok()
}

// key_of is a revision's or a proposal's key: its number in ten digits,
// zero-padded.
fn key_of(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
}

// whole reads a whole number a schema let through: 2 or 2.0.
fn whole(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    if let Some(whole) = number.as_i64() {
        return Some(whole);
    }
    number
        .as_f64()
        .filter(|n| n.is_finite() && n.fract() == 0.0)
        .map(|n| n as i64)
}

// The configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config.keys().filter(|key| key.as_str() != "review") {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    let Some(review) = config.get("review") else {
        return issues;
    };
    let Some(review) = review.as_object() else {
        issues.push("/review", "type", "must be object");
        return issues;
    };
    for key in review.keys().filter(|key| key.as_str() != "permission") {
        issues.push(
            "/review",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    match review.get("permission") {
        None => issues.push(
            "/review",
            "required",
            "must have required property 'permission'",
        ),
        Some(permission) => schema::text_issues("/review/permission", permission, &mut issues),
    }
    issues
}

// The operations' paramsSchemas, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let (keys, required): (&[&str], Option<&str>) = match operation {
        LIST_REVISIONS => (&["cursor", "limit"], None),
        GET_REVISION => (&["revision"], Some("revision")),
        PROPOSE => (&["evidence", "note", "patch"], Some("patch")),
        APPROVE => (&["proposal"], Some("proposal")),
        REJECT => (&["proposal", "reason"], Some("proposal")),
        LIST_PROPOSALS => (&["cursor", "limit", "state"], None),
        _ => return issues,
    };
    let extra: Vec<&String> = given
        .keys()
        .filter(|key| !keys.contains(&key.as_str()))
        .collect();
    for key in extra {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if let Some(required) = required.filter(|required| !given.contains_key(*required)) {
        issues.push(
            "",
            "required",
            format!("must have required property '{required}'"),
        );
    }
    if let Some(limit) = given.get("limit") {
        schema::integer_issues("/limit", limit, Some(1), Some(MAX_LIMIT), &mut issues);
    }
    if given
        .get("cursor")
        .is_some_and(|cursor| !cursor.is_string())
    {
        issues.push("/cursor", "type", "must be string");
    }
    for member in ["revision", "proposal"] {
        if let Some(value) = given.get(member) {
            schema::integer_issues(&format!("/{member}"), value, Some(1), None, &mut issues);
        }
    }
    for member in ["note", "reason"] {
        match given.get(member) {
            Some(Value::String(text)) if schema::length(text) > MAX_TEXT => issues.push(
                format!("/{member}"),
                "maxLength",
                format!("must NOT have more than {MAX_TEXT} characters"),
            ),
            Some(Value::String(_)) | None => {}
            Some(_) => issues.push(format!("/{member}"), "type", "must be string"),
        }
    }
    if let Some(state) = given.get("state") {
        match state.as_str() {
            Some(state) if STATES.contains(&state) => {}
            Some(_) => issues.push(
                "/state",
                "enum",
                "must be equal to one of the allowed values: \"pending\", \"approved\", \"rejected\"",
            ),
            None => issues.push("/state", "type", "must be string"),
        }
    }
    match given.get("patch") {
        Some(Value::Object(patch)) if patch.is_empty() => {
            issues.push(
                "/patch",
                "minProperties",
                "must NOT have fewer than 1 properties",
            );
        }
        Some(Value::Object(_)) | None => {}
        Some(_) => issues.push("/patch", "type", "must be object"),
    }
    if let Some(evidence) = given.get("evidence") {
        evidence_issues(evidence, &mut issues);
    }
    issues
}

// What the evidence breaks of its schema: a list of 1 to 64 distinct
// entries, each `{ schema, id, revision? }`.
fn evidence_issues(evidence: &Value, issues: &mut Issues) {
    let Some(entries) = evidence.as_array() else {
        issues.push("/evidence", "type", "must be array");
        return;
    };
    if entries.is_empty() {
        issues.push("/evidence", "minItems", "must NOT have fewer than 1 items");
    }
    if entries.len() > MAX_EVIDENCE {
        issues.push(
            "/evidence",
            "maxItems",
            format!("must NOT have more than {MAX_EVIDENCE} items"),
        );
    }
    if let Some((j, i)) = schema::duplicate_value(entries) {
        issues.push(
            "/evidence",
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {i} and {j} are identical)"),
        );
    }
    for (index, entry) in entries.iter().enumerate() {
        let at = format!("/evidence/{index}");
        let Some(entry) = entry.as_object() else {
            issues.push(at, "type", "must be object");
            continue;
        };
        for key in entry
            .keys()
            .filter(|key| !["id", "revision", "schema"].contains(&key.as_str()))
        {
            issues.push(
                at.clone(),
                "additionalProperties",
                format!("must NOT have additional properties: {key}"),
            );
        }
        for (member, max) in [("schema", 128usize), ("id", 256)] {
            match entry.get(member) {
                None => issues.push(
                    at.clone(),
                    "required",
                    format!("must have required property '{member}'"),
                ),
                Some(Value::String(text)) => {
                    if !pattern_holds(member, text, max) {
                        issues.push(
                            format!("{at}/{member}"),
                            "pattern",
                            format!("must match pattern \"{}\"", pattern_of(member)),
                        );
                    }
                }
                Some(_) => issues.push(format!("{at}/{member}"), "type", "must be string"),
            }
        }
        if let Some(revision) = entry.get("revision") {
            schema::integer_issues(&format!("{at}/revision"), revision, Some(1), None, issues);
        }
    }
}

fn pattern_of(member: &str) -> &'static str {
    if member == "schema" {
        "^[A-Za-z][A-Za-z0-9_-]{0,127}$"
    } else {
        "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
    }
}

// pattern_holds is whether an evidence entry's schema or id matches its
// pattern: a letter (a letter or a digit for an id) and then at most
// `max - 1` more of the characters it allows.
fn pattern_holds(member: &str, text: &str, max: usize) -> bool {
    let mut chars = text.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    let (first_ok, rest): (bool, fn(char) -> bool) = if member == "schema" {
        (first.is_ascii_alphabetic(), |c: char| {
            c.is_ascii_alphanumeric() || c == '_' || c == '-'
        })
    } else {
        (first.is_ascii_alphanumeric(), |c: char| {
            c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-')
        })
    };
    first_ok && schema::length(text) <= max && chars.all(rest)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_revisions_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of(1), "0000000001");
        assert_eq!(number_of(&SubRow::new("0000000042", Map::new())), Some(42));
    }

    #[test]
    fn an_evidence_entrys_schema_and_id_are_held_to_their_patterns() {
        assert!(pattern_holds("schema", "Note", 128));
        assert!(!pattern_holds("schema", "1Note", 128));
        assert!(pattern_holds("id", "n1:a.b-c_d", 256));
        assert!(!pattern_holds("id", "-n1", 256));
        assert!(!pattern_holds("id", "", 256));
    }
}
