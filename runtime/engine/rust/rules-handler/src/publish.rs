//! A publish in development mode (D117): the type's draft becomes its next
//! version, with no compatibility rule. A version may rename, retype,
//! remove and tighten. The stored rows are held to it as a table's DDL
//! would hold its rows: a removed field's values go with it, as a dropped
//! column's do, and a removed use's owned fields, sub-table rows and
//! deadlines go with the use; a value no longer of its field's type, a
//! required field with no value, a value past a field's bounds, a relation
//! to a row that does not exist, a unique key two rows hold, a key of
//! another kind, and a use's value field a row holds no value of (a use
//! added to a type with rows) fail the version. A publish that leaves any
//! row failing changes nothing and lists each such row with what it fails,
//! unless it says to discard them; then they are deleted in the publish's
//! transaction, with the rows of every type their delete reaches through a
//! relation, as a table's foreign keys answer it.
//!
//! A row a rename leaves holds the old field's values under no field, so
//! they go: a rename is a removal and an addition, and the new field's
//! required check holds the row to it. A behavior's config may change in
//! any way the core's define check takes, as no behavior's config-change
//! rule is asked; a row's owned state stays as it was, and its behaviors
//! answer from it at its next command.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::{json, Map, Value};
use superschematic_rules::{Code, Refusal, ValidationIssue};

use crate::bridge;
use crate::document::{self, Kind};
use crate::handler::{names_row, unpublished_target, Context, Done, Handler, HandlerStep, Reads};
use crate::store::{StoreWrite, StoredRow, Unit};
use crate::validate;
use crate::values::{same, uuid_of};

/// A stored row that fails a version, and what it fails.
struct Failing {
    type_name: String,
    id: String,
    issues: Vec<ValidationIssue>,
}

pub(crate) fn publish(
    handler: &Handler<'_>,
    context: &Context,
    type_name: &str,
    discard: bool,
    reads: &mut Reads<'_>,
) -> Result<HandlerStep, Refusal> {
    let live = handler.live(reads)?;
    let draft = reads.draft(type_name);
    let (Some(mut live), Some(draft)) = (live, draft) else {
        return Ok(waiting(reads));
    };
    let draft = draft.ok_or_else(|| {
        Refusal::not_found(format!(
            "type {type_name} has no version defined to publish"
        ))
    })?;
    let model = document::read(&draft.document, handler.declarations())?;
    handler.define_check(&model, context)?;
    for field in &model.fields {
        if let Some(relation) = &field.relation {
            if relation.target != model.name && !live.models.contains_key(&relation.target) {
                return Err(unpublished_target(&model, &relation.name, &relation.target));
            }
        }
    }
    let old = live.models.get(type_name).cloned();
    let version = live.versions.get(type_name).copied().unwrap_or(0) + 1;
    live.install(handler.core(), model);
    let model = live.model(type_name)?.clone();

    // The rows the version is held to, and the rows its relations and the
    // relations into it reach.
    let rows = reads.rows(type_name);
    for field in &model.fields {
        if let Some(relation) = &field.relation {
            reads.rows(&relation.target);
        }
    }
    for (referrer, _, _) in live.referrers(type_name) {
        reads.rows(&referrer);
    }
    let Some(rows) = rows.filter(|_| !reads.waiting()) else {
        return Ok(waiting(reads));
    };

    // A use the version removes, or gives another behavior, goes with its
    // owned fields, sub-table rows and deadlines.
    let kept_uses: BTreeMap<&str, &str> = model
        .uses
        .iter()
        .map(|used| (used.id(), used.spec.behavior.as_str()))
        .collect();
    let removed_uses: Vec<(String, String)> = old
        .as_ref()
        .map(|old| {
            old.uses
                .iter()
                .filter(|used| kept_uses.get(used.id()) != Some(&used.spec.behavior.as_str()))
                .map(|used| (used.id().to_owned(), used.spec.behavior.clone()))
                .collect()
        })
        .unwrap_or_default();
    let owned_names: BTreeMap<&str, BTreeSet<&str>> =
        model
            .owned
            .iter()
            .fold(BTreeMap::new(), |mut names, owned| {
                names
                    .entry(owned.use_id.as_str())
                    .or_default()
                    .insert(owned.name.as_str());
                names
            });

    let mut migrated: Vec<StoredRow> = Vec::with_capacity(rows.len());
    let mut failing: Vec<Failing> = Vec::new();
    for row in rows {
        let data: Map<String, Value> = row
            .data
            .iter()
            .filter(|(name, _)| model.field(name).is_some())
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect();
        let owned: BTreeMap<String, Map<String, Value>> = row
            .owned
            .iter()
            .filter_map(|(use_id, fields)| {
                let names = owned_names.get(use_id.as_str())?;
                // A use the version gives another behavior keeps none of
                // the old behavior's state.
                if removed_uses.iter().any(|(removed, _)| removed == use_id) {
                    return None;
                }
                let fields: Map<String, Value> = fields
                    .iter()
                    .filter(|(name, _)| names.contains(name.as_str()))
                    .map(|(name, value)| (name.clone(), value.clone()))
                    .collect();
                (!fields.is_empty()).then(|| (use_id.clone(), fields))
            })
            .collect();
        let mut issues = validate::stored_fields(&model, &data);
        if model.key.kind == Kind::Uuid && uuid_of(&row.id).is_none() {
            issues.push(ValidationIssue::new(
                format!("/{}", model.key.name),
                "key",
                format!(
                    "the row's key {} is no UUID, which {}'s key now is",
                    row.id, model.key.name
                ),
            ));
        }
        for owned_field in model.owned.iter().filter(|owned| !owned.optional) {
            let held = owned
                .get(&owned_field.use_id)
                .and_then(|fields| fields.get(&owned_field.name))
                .is_some_and(|value| !value.is_null());
            if !held {
                let behavior = model
                    .use_of(&owned_field.use_id)
                    .map_or("", |used| used.spec.behavior.as_str());
                issues.push(ValidationIssue::new(
                    format!("/{}", owned_field.field),
                    "required",
                    format!(
                        "{} is {behavior}'s value, which a create sets, and the row holds none",
                        owned_field.field
                    ),
                ));
            }
        }
        for field in &model.fields {
            let (Some(relation), Some(held)) = (&field.relation, data.get(&field.name)) else {
                continue;
            };
            let targets = if relation.target == model.name {
                rows
            } else {
                crate::bridge::rows_of(reads.answers, &relation.target).unwrap_or_default()
            };
            if !targets.iter().any(|target| names_row(held, &target.id)) {
                issues.push(ValidationIssue::new(
                    format!("/{}", field.name),
                    "relation",
                    format!(
                        "{} points at {} {}, which does not exist",
                        relation.name,
                        relation.target,
                        held.as_str().unwrap_or_default()
                    ),
                ));
            }
        }
        if !issues.is_empty() {
            failing.push(Failing {
                type_name: type_name.to_owned(),
                id: row.id.clone(),
                issues,
            });
        }
        migrated.push(StoredRow {
            id: row.id.clone(),
            version: row.version,
            data,
            owned,
        });
    }
    // A unique key two rows hold fails each holder after the first, in id
    // order, as a unique index the version adds would refuse it.
    for unique in &model.uniques {
        let mut holders: Vec<(Vec<&Value>, &str)> = Vec::new();
        for row in &migrated {
            let values: Option<Vec<&Value>> = unique
                .iter()
                .map(|name| row.data.get(name).filter(|value| !value.is_null()))
                .collect();
            let Some(values) = values else {
                continue;
            };
            let first = holders
                .iter()
                .find(|(held, _)| held.iter().zip(&values).all(|(a, b)| same(a, b)));
            match first {
                Some((_, first)) => {
                    let issue = ValidationIssue::new(
                        format!("/{}", unique.join(",")),
                        "unique",
                        format!(
                            "{} {first} holds the same {}, which is unique",
                            model.name,
                            unique.join(" and ")
                        ),
                    );
                    match failing.iter_mut().find(|failing| failing.id == row.id) {
                        Some(failing) => failing.issues.push(issue),
                        None => failing.push(Failing {
                            type_name: type_name.to_owned(),
                            id: row.id.clone(),
                            issues: vec![issue],
                        }),
                    }
                }
                None => holders.push((values, row.id.as_str())),
            }
        }
    }

    if !failing.is_empty() && !discard {
        return Err(failing_refusal(type_name, version, &failing));
    }

    // The rows a discard deletes, and those their delete reaches.
    let start: Vec<(String, String)> = failing
        .iter()
        .map(|failing| (failing.type_name.clone(), failing.id.clone()))
        .collect();
    let cascade = if start.is_empty() {
        crate::handler::Cascade::default()
    } else {
        let Some(cascade) = Handler::cascade(&live, &start, &BTreeSet::new(), reads)? else {
            return Ok(waiting(reads));
        };
        cascade
    };
    let discarded: BTreeSet<(String, String)> = start
        .iter()
        .cloned()
        .chain(
            cascade
                .deleted
                .iter()
                .map(|(type_name, row)| (type_name.clone(), row.id.clone())),
        )
        .collect();

    let mut writes = vec![StoreWrite::PutVersion {
        type_name: type_name.to_owned(),
        version,
        document: draft.document.clone(),
    }];
    for (use_id, behavior) in &removed_uses {
        writes.push(StoreWrite::DropUse {
            type_name: type_name.to_owned(),
            behavior: behavior.clone(),
            use_id: use_id.clone(),
        });
        writes.push(StoreWrite::ClearUse {
            type_name: type_name.to_owned(),
            use_id: use_id.clone(),
        });
    }
    let mut rewritten = 0;
    let mut heard = Vec::new();
    // A row of the type a SET NULL self relation clears is cleared on its
    // migrated fields, an update that moves its version.
    let nulled_here: BTreeMap<&str, &StoredRow> = cascade
        .nulled
        .iter()
        .filter(|(other, _, _)| other == type_name)
        .map(|(_, before, after)| (before.id.as_str(), after))
        .collect();
    for (row, before) in migrated.iter().zip(rows) {
        if let Some(after) = nulled_here.get(row.id.as_str()) {
            let data: Map<String, Value> = row
                .data
                .iter()
                .filter(|(name, _)| after.data.contains_key(name.as_str()))
                .map(|(name, value)| (name.clone(), value.clone()))
                .collect();
            writes.push(StoreWrite::Update {
                type_name: type_name.to_owned(),
                id: row.id.clone(),
                expected: before.version,
                data,
                owned: row.owned.clone(),
            });
            rewritten += 1;
            continue;
        }
        if discarded.contains(&(type_name.to_owned(), row.id.clone())) {
            writes.push(StoreWrite::Delete {
                type_name: type_name.to_owned(),
                id: row.id.clone(),
                expected: None,
            });
            if live.hearing.contains(type_name) {
                heard.push(bridge::change_of(
                    type_name,
                    &row.id,
                    context.now,
                    Some(before),
                    None,
                ));
            }
            continue;
        }
        if row.data != before.data || row.owned != before.owned {
            rewritten += 1;
            writes.push(StoreWrite::Rewrite {
                type_name: type_name.to_owned(),
                id: row.id.clone(),
                data: row.data.clone(),
                owned: row.owned.clone(),
            });
        }
    }
    let others = crate::handler::Cascade {
        deleted: cascade
            .deleted
            .iter()
            .filter(|(other, _)| other != type_name)
            .cloned()
            .collect(),
        nulled: cascade
            .nulled
            .iter()
            .filter(|(other, _, _)| other != type_name)
            .cloned()
            .collect(),
    };
    // A row of the type a self relation cascades to is deleted above, as a
    // failing one is.
    writes.extend(Handler::cascade_writes(&live, &others, context.now));
    writes.extend(heard);
    let discarded: Vec<Value> = discarded
        .iter()
        .map(|(type_name, id)| json!({"type": type_name, "id": id}))
        .collect();
    Ok(HandlerStep::Done(Done {
        response: Ok(json!({
            "type": type_name,
            "version": version,
            "rewritten": rewritten,
            "discarded": discarded,
        })),
        unit: Unit { writes },
    }))
}

fn waiting(reads: &mut Reads<'_>) -> HandlerStep {
    HandlerStep::Read(std::mem::take(&mut reads.need))
}

/// The refusal of a publish that stored rows no longer satisfy:
/// `conflict`, of the class `StoredRowsError`, each row with what it fails.
fn failing_refusal(type_name: &str, version: i64, failing: &[Failing]) -> Refusal {
    let listed: Vec<String> = failing
        .iter()
        .map(|failing| {
            let issues: Vec<String> = failing
                .issues
                .iter()
                .map(|issue| format!("{}: {}", issue.path, issue.message))
                .collect();
            format!("{} ({})", failing.id, issues.join("; "))
        })
        .collect();
    let rows: Vec<Value> = failing
        .iter()
        .map(|failing| json!({"type": failing.type_name, "id": failing.id, "issues": failing.issues}))
        .collect();
    let mut details = Map::new();
    details.insert("version".into(), Value::from(version));
    details.insert("rows".into(), Value::Array(rows));
    Refusal {
        class: "StoredRowsError".to_owned(),
        details: Some(Box::new(details)),
        ..Refusal::new(
            Code::Conflict,
            format!(
                "type {type_name}: version {version} does not describe {} stored row{}: {}; publish it with discard to delete them",
                failing.len(),
                if failing.len() == 1 { "" } else { "s" },
                listed.join(", ")
            ),
        )
    }
}
