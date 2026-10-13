//! A link's target moving on (D16, amended), the port of
//! `src/behaviors/core/revised.ts`, as Reactions' `revised` rule (wave 3)
//! and Lease's `directOn` (the work queue) hear it. An event of the
//! target's schema moves it on when it is a new revision of Revisions (an
//! update or an operation whose change carries `Revisions.revision`), or a
//! release of Branches (its `releaseCommit`, whose patch carries the
//! release pointer's new version, Branches' `release` field); a target
//! whose schema composes neither gains neither, which the rule that names
//! the link takes for a defect of its config. The instances it moves on are
//! the ones of the rule's own schema whose link points at the target, found
//! with Links' `listLinked`: where the link pins what the event made, a
//! revision or a release, only the ones the target has moved past
//! (`stale`), so an instance linked to the new revision or release since is
//! left alone; any other link moves every one on.

use serde_json::{Map, Value};

use superschematic_engine::behaviors::Scope;
use superschematic_engine::json::number_value;
use superschematic_engine::{EngineEvent, Error, Result};

use crate::links::LinkPin;

/// Links' largest page of `listLinked`.
const PAGE: i64 = 500;

/// How an event moved a link's target on: a revision, or a release, by the
/// release pointer's version it made (1 at the first) and the commit it
/// names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TargetMove {
    Revision { revision: i64 },
    Release { release: i64, commit: String },
}

impl TargetMove {
    /// What the move made: a revision or a release.
    pub fn kind(&self) -> LinkPin {
        match self {
            TargetMove::Revision { .. } => LinkPin::Revision,
            TargetMove::Release { .. } => LinkPin::Release,
        }
    }
}

// field reads one behavior field of a patch of `{ data, behaviors }`.
fn field<'p>(patch: Option<&'p Value>, behavior: &str, name: &str) -> Option<&'p Value> {
    patch?
        .get("behaviors")?
        .get(behavior)?
        .as_object()?
        .get(name)
}

// number reads a value as JavaScript's Number() does for the values a
// patch and a call's parameters hold: a number as itself, null as 0.
fn number(value: Option<&Value>) -> Option<f64> {
    match value? {
        Value::Number(number) => Some(number_value(number)),
        Value::Null => Some(0.0),
        Value::String(text) => text.trim().parse().ok(),
        Value::Bool(flag) => Some(f64::from(u8::from(*flag))),
        _ => None,
    }
}

/// How an event of a link's target schema moved its instance on
/// (`targetMove`), or `None` when it did not: a create, a delete, and any
/// change that is neither a revision nor a release. A releaseCommit is a
/// release whatever else its patch carries. `form` names the rule, for
/// the BehaviorError of `behavior` a schema that composes neither Revisions
/// nor Branches gets.
pub fn target_move<C>(
    scope: &Scope<'_, C>,
    behavior: &str,
    form: &str,
    link: &str,
    schema: &str,
    event: &EngineEvent,
) -> Result<Option<TargetMove>> {
    if event.schema != schema || event.kind == "create" || event.kind == "delete" {
        return Ok(None);
    }
    let revisions = scope.schemas().config(schema, "Revisions")?.is_some();
    let branches = scope.schemas().config(schema, "Branches")?.is_some();
    if !revisions && !branches {
        return Err(Error::behavior(
            behavior,
            format!("{form} names link {link} to {schema}, which composes neither Revisions nor Branches, so it gains no revision or release"),
        ));
    }
    let operation = (event.kind == "operation")
        .then_some(&event.change)
        .filter(|change| change.is_object());
    // The patch of { data, behaviors } the event records.
    let patch = match operation {
        Some(operation) => operation.get("patch"),
        None if event.kind == "update" => Some(&event.change),
        None => None,
    };
    if let Some(operation) = operation.filter(|operation| {
        branches
            && operation.get("behavior").and_then(Value::as_str) == Some("Branches")
            && operation.get("operation").and_then(Value::as_str) == Some("releaseCommit")
    }) {
        let empty = Value::Object(Map::new());
        let params = operation.get("params").unwrap_or(&empty);
        let release = match field(patch, "Branches", "release") {
            Some(Value::Number(moved)) => number_value(moved),
            _ => number(params.get("version")).unwrap_or(f64::NAN) + 1.0,
        };
        let commit = match params.get("commit") {
            Some(Value::String(commit)) => commit.clone(),
            Some(other) => superschematic_engine::json::stringify(other),
            None => "undefined".to_owned(),
        };
        return Ok(Some(TargetMove::Release {
            release: release as i64,
            commit,
        }));
    }
    let revision = field(patch, "Revisions", "revision");
    if revisions {
        if let Some(revision) = revision {
            return Ok(Some(TargetMove::Revision {
                revision: number(Some(revision)).unwrap_or(f64::NAN) as i64,
            }));
        }
    }
    Ok(None)
}

/// The instances of the scope's schema whose link points at the target id
/// (`linkedTo`), in Links' order, through Links' `listLinked` as the
/// scope's principal: with `stale`, only the ones whose pin the target has
/// moved past.
pub fn linked_to<C>(
    scope: &Scope<'_, C>,
    link: &str,
    id: &str,
    stale: bool,
) -> Result<Vec<String>> {
    let mut found = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let mut params = Map::new();
        params.insert("name".into(), Value::from(link));
        params.insert("id".into(), Value::from(id));
        params.insert("limit".into(), Value::from(PAGE));
        if stale {
            params.insert("stale".into(), Value::Bool(true));
        }
        if let Some(cursor) = &cursor {
            params.insert("cursor".into(), Value::from(cursor.as_str()));
        }
        let page = scope.instances().invoke_schema(
            scope.schema(),
            "listLinked",
            &Value::Object(params),
        )?;
        found.extend(
            page.get("items")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(|item| item.get("id").and_then(Value::as_str))
                .map(str::to_owned),
        );
        cursor = page.get("next").and_then(Value::as_str).map(str::to_owned);
        if cursor.is_none() {
            return Ok(found);
        }
    }
}

/// The instances a move of a link's target moves on (`movedOn`): where the
/// link pins what the move made (`pin`, as [`crate::link_pin`] reads the
/// link), the stale ones, and every one linked to it otherwise.
pub fn moved_on<C>(
    scope: &Scope<'_, C>,
    link: &str,
    pin: Option<LinkPin>,
    id: &str,
    moved: &TargetMove,
) -> Result<Vec<String>> {
    linked_to(scope, link, id, pin == Some(moved.kind()))
}
