//! Provenance, the core's record of who set each own field last and
//! through what (D73). afterChange records, for each field the config
//! records that a change wrote, its source: `caller` for a caller's create
//! or update, or the behavior whose operation wrote it, by the entry its
//! update() attributed it with (D16, amended: an operation's update()
//! attributes what it writes) or else the operation's own behavior; the
//! principal's subject, and the subject it acted for (D77); the time; and
//! the confidence the attribution gave.
//!
//! With review, a write from a source the review names is proposed, or
//! accepted with no reviewer when its confidence reaches `below`. A
//! reviewer holding the review's permission accepts a proposal as it is,
//! or rejects it: reject writes the field through its own update(),
//! clearing it or setting the patch's value, so Derived records the write
//! as an override and does not propose the field again. A write from any
//! other source over a proposal makes it edited. `inReview` is whether any
//! recorded field is proposed, kept in a column of the instance's row, so
//! a list filters on it.
//!
//! A gate refuses a transition of the type's Workflow into its state while
//! a field it lists is proposed: `unreviewed_fields`, with the state and
//! the fields in its details. Like Approvals' gate, it lets through a move
//! Workflow refuses, for Workflow to refuse.
//!
//! configChange: any config may change, and Provenance may be added to and
//! removed from a schema with instances. A field written before it was
//! added has no record until its next write. afterConfigChange drops the
//! records of a field the config no longer records, and every record when
//! the behavior goes. Deleting the instance deletes its records.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, InstanceCx, InstanceView, SqlReader, SqlWriter,
    TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, OperationCx, Result,
};

use crate::staterules::status_of;

/// The review of a Provenance config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct ProvenanceReview {
    /// The behaviors whose writes enter review.
    pub sources: Vec<String>,
    /// The confidence at or above which a write is accepted with no
    /// reviewer; `None` when every write from a source is proposed.
    pub below: Option<f64>,
    /// The permission a reviewer needs.
    pub permission: String,
}

/// Provenance's config, parsed: `fields` and a gate's `all` filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct ProvenanceConfig {
    /// The own top-level fields it records, in the config's order, or the
    /// type's.
    pub fields: Vec<String>,
    /// Which writes enter review; `None` when none does.
    pub review: Option<ProvenanceReview>,
    /// By state of the type's Workflow, the fields a move into it requires
    /// out of review, in the config's order.
    pub gates: Vec<(String, Vec<String>)>,
}

const NAME: &str = "Provenance";

const COLUMNS: &str =
    "record, field, source, by_subject, for_subject, at, confidence, review, reviewed_by, reviewed_at";

const PROPOSED: &str = "proposed";

fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn records_of<C>(view: &InstanceView<'_, C>) -> Result<Vec<Row>> {
    let sql = view.sql();
    sql.all(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY record",
            sql.table("records")?
        ),
        &key(view),
    )
}

fn record_of<C>(view: &InstanceView<'_, C>, field: &str) -> Result<Option<Row>> {
    let sql = view.sql();
    let mut at = key(view);
    at.push(SqlValue::from(field));
    sql.get(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND id = ? AND field = ?",
            sql.table("records")?
        ),
        &at,
    )
}

// The record of one write, as `record` stores it.
struct Written<'a> {
    field: &'a str,
    source: &'a str,
    by: &'a str,
    /// The subject the writer acted for (D77).
    for_subject: Option<&'a str>,
    confidence: Option<f64>,
    review: Option<&'a str>,
    reviewed_by: Option<&'a str>,
}

// record writes a field's record, replacing what it held.
fn record<C>(cx: &InstanceCx<'_, C>, written: &Written<'_>) -> Result<()> {
    let sql: SqlWriter<'_> = cx.sql();
    let table = sql.table("records")?;
    let mut at = key(cx);
    at.push(SqlValue::from(written.field));
    sql.run(
        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND field = ?"),
        &at,
    )?;
    let now = cx.now();
    at.extend([
        SqlValue::from(written.source),
        SqlValue::from(written.by),
        SqlValue::from(written.for_subject),
        SqlValue::Integer(now),
        written.confidence.map_or(SqlValue::Null, SqlValue::Real),
        written.review.map_or(SqlValue::Null, SqlValue::from),
        written.reviewed_by.map_or(SqlValue::Null, SqlValue::from),
        if written.review.is_some() && written.review != Some(PROPOSED) {
            SqlValue::Integer(now)
        } else {
            SqlValue::Null
        },
    ]);
    sql.run(
        format!("INSERT INTO {table} (namespace, schema, id, field, source, by_subject, for_subject, at, confidence, review, reviewed_by, reviewed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
        &at,
    )?;
    Ok(())
}

// in_review is whether any of an instance's records is proposed.
fn in_review(sql: &SqlReader<'_>, at: &[SqlValue]) -> Result<bool> {
    Ok(sql
        .get(
            format!(
                "SELECT 1 AS found FROM {} WHERE namespace = ? AND schema = ? AND id = ? AND review = '{PROPOSED}'",
                sql.table("records")?
            ),
            at,
        )?
        .is_some())
}

// mark keeps the instance's inReview column in line with its records.
fn mark<C>(cx: &InstanceCx<'_, C>) -> Result<bool> {
    let reviewing = in_review(&cx.sql(), &key(cx))?;
    cx.columns()
        .set(&[("in_review", SqlValue::Integer(i64::from(reviewing)))])?;
    Ok(reviewing)
}

// refuse is a veto of one of the behavior's operations.
fn refuse<C>(view: &InstanceView<'_, C>, operation: &str, reason: String, code: &str) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: None,
        },
    )
}

fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

// review_of is the review a write from a source with a confidence takes,
// over a record whose review was `prior`.
fn review_of<'c>(
    config: &'c ProvenanceConfig,
    source: &str,
    confidence: Option<f64>,
    prior: Option<&str>,
) -> Option<&'c str> {
    match &config.review {
        Some(review) if review.sources.iter().any(|named| named == source) => {
            let sure = review
                .below
                .is_some_and(|below| confidence.is_some_and(|confidence| confidence >= below));
            Some(if sure { "accepted" } else { PROPOSED })
        }
        _ => (prior == Some(PROPOSED)).then_some("edited"),
    }
}

// proposed_of checks fields a review operation names: each one recorded
// and proposed, and the caller a reviewer.
fn proposed_of(
    cx: &OperationCx<'_, ProvenanceConfig>,
    operation: &str,
    fields: &[String],
) -> Result<()> {
    let config = cx.config();
    let (schema, id) = (cx.schema(), cx.id());
    if let Some(review) = &config.review {
        if !cx.can(&review.permission)? {
            return Err(EngineError::new(
                EngineErrorCode::Forbidden,
                format!(
                    "{} may not review {schema} {id}: it needs permission {}",
                    cx.principal().subject,
                    review.permission
                ),
            )
            .into());
        }
    }
    for field in fields {
        if !config.fields.contains(field) {
            return Err(refuse(
                cx,
                operation,
                format!("Provenance does not record {field} of {schema}"),
                "not_recorded",
            ));
        }
        let review = record_of(cx, field)?
            .and_then(|row| row.opt_text("review").ok().flatten().map(str::to_owned));
        if review.as_deref() != Some(PROPOSED) {
            return Err(refuse(
                cx,
                operation,
                format!("{field} of {schema} {id} is not proposed"),
                "not_in_review",
            ));
        }
    }
    Ok(())
}

fn accept(_: &Provenance, cx: &OperationCx<'_, ProvenanceConfig>, params: &Value) -> Result<Value> {
    let fields = strings(params.get("fields"));
    proposed_of(cx, "accept", &fields)?;
    let sql = cx.sql();
    let table = sql.table("records")?;
    for field in &fields {
        let mut at = vec![
            SqlValue::from(cx.principal().subject.as_str()),
            SqlValue::Integer(cx.now()),
        ];
        at.extend(key(cx));
        at.push(SqlValue::from(field.as_str()));
        sql.run(
            format!("UPDATE {table} SET review = 'accepted', reviewed_by = ?, reviewed_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND field = ?"),
            &at,
        )?;
    }
    let reviewing = mark(cx)?;
    Ok(json!({ "accepted": fields, "inReview": reviewing }))
}

fn reject(_: &Provenance, cx: &OperationCx<'_, ProvenanceConfig>, params: &Value) -> Result<Value> {
    let fields = strings(params.get("fields"));
    proposed_of(cx, "reject", &fields)?;
    let empty = Map::new();
    let given = params
        .get("patch")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    for name in js_keys(given) {
        if !fields.iter().any(|field| field == name) {
            return Err(refuse(
                cx,
                "reject",
                format!("reject's patch sets {name}, which its fields do not name"),
                "not_recorded",
            ));
        }
    }
    let patch: Map<String, Value> = fields
        .iter()
        .map(|field| {
            (
                field.clone(),
                given.get(field).cloned().unwrap_or(Value::Null),
            )
        })
        .collect();
    cx.update(&Value::Object(patch))?;
    let subject = cx.principal().subject.clone();
    let for_subject = cx.principal().acting_for_subject().map(str::to_owned);
    for field in &fields {
        record(
            cx,
            &Written {
                field,
                source: NAME,
                by: &subject,
                for_subject: for_subject.as_deref(),
                confidence: None,
                review: Some("rejected"),
                reviewed_by: Some(&subject),
            },
        )?;
    }
    let reviewing = mark(cx)?;
    Ok(json!({ "rejected": fields, "inReview": reviewing }))
}

// after_change records the fields a change wrote: who wrote each, through
// what, and the review that takes.
fn after_change(
    _: &Provenance,
    cx: &InstanceCx<'_, ProvenanceConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    let config = cx.config();
    let empty = Map::new();
    let (before, behavior, attribution) = match change {
        InstanceChange::Delete => {
            cx.sql().run(
                format!(
                    "DELETE FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
                    cx.sql().table("records")?
                ),
                &key(cx),
            )?;
            return Ok(());
        }
        InstanceChange::Create => (&empty, None, None),
        InstanceChange::Update { before, .. } => (*before, None, None),
        // reject records its own writes.
        InstanceChange::Operation { behavior: NAME, .. } => return Ok(()),
        InstanceChange::Operation {
            behavior,
            before: Some(before),
            attribution,
            ..
        } => (*before, Some(*behavior), *attribution),
        InstanceChange::Operation { before: None, .. } => return Ok(()),
    };
    let data = cx.data()?;
    let subject = cx.principal().subject.clone();
    let for_subject = cx.principal().acting_for_subject().map(str::to_owned);
    let mut wrote = false;
    for field in &config.fields {
        let after = data.get(field);
        if before.get(field) == after
            || (matches!(change, InstanceChange::Create)
                && matches!(after, None | Some(Value::Null)))
        {
            continue;
        }
        let entry = attribution.and_then(|attribution| attribution.get(field));
        let source = entry
            .and_then(|entry| entry.get("behavior"))
            .and_then(Value::as_str)
            .or(behavior)
            .unwrap_or("caller");
        let confidence = entry
            .and_then(|entry| entry.get("confidence"))
            .and_then(Value::as_f64);
        let prior = record_of(cx, field)?
            .and_then(|row| row.opt_text("review").ok().flatten().map(str::to_owned));
        let review = review_of(config, source, confidence, prior.as_deref());
        record(
            cx,
            &Written {
                field,
                source,
                by: &subject,
                for_subject: for_subject.as_deref(),
                confidence,
                review,
                reviewed_by: (review == Some("edited")).then_some(subject.as_str()),
            },
        )?;
        wrote = true;
    }
    if wrote {
        mark(cx)?;
    }
    Ok(())
}

// guard is the gates: a transition of the type's Workflow into a gated
// state waits until the fields the gate lists are out of review.
fn guard(
    _: &Provenance,
    view: &InstanceView<'_, ProvenanceConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Operation {
        behavior: "Workflow",
        operation: "transition",
        params,
        ..
    } = request
    else {
        return Ok(None);
    };
    let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
    let Some((_, listed)) = view.config().gates.iter().find(|(state, _)| state == to) else {
        return Ok(None);
    };
    let proposed: Vec<String> = records_of(view)?
        .iter()
        .filter(|row| row.opt_text("review").ok().flatten() == Some(PROPOSED))
        .filter_map(|row| row.opt_text("field").ok().flatten().map(str::to_owned))
        .filter(|field| listed.contains(field))
        .collect();
    if proposed.is_empty() {
        return Ok(None);
    }
    // A move Workflow refuses is Workflow's to refuse, whichever of the two
    // the type lists first.
    let Some(from) = status_of(view)? else {
        return Ok(None);
    };
    let flow = view.schemas().config(view.schema(), "Workflow")?;
    let moves = flow
        .as_ref()
        .and_then(|flow| flow.get("transitions"))
        .and_then(Value::as_array)
        .is_some_and(|moves| {
            moves.iter().any(|edge| {
                edge.get("from").and_then(Value::as_str) == Some(from.as_str())
                    && edge.get("to").and_then(Value::as_str) == Some(to)
            })
        });
    if !moves {
        return Ok(None);
    }
    let mut details = Map::new();
    details.insert("state".into(), Value::from(to));
    details.insert("fields".into(), json!(proposed));
    Ok(Some(Veto {
        reason: format!(
            "{} {} cannot move to {to} while {} {} in review",
            view.schema(),
            view.id(),
            proposed.join(", "),
            if proposed.len() == 1 { "is" } else { "are" }
        ),
        code: Some("unreviewed_fields".to_owned()),
        details: Some(details),
    }))
}

/// Provenance: who set each own field last, and its review.
#[derive(Clone, Copy, Debug, Default)]
pub struct Provenance;

impl Behavior for Provenance {
    type Config = ProvenanceConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Provenance.behavior.json")
    }

    // The configSchema holds the shape; this holds the recorded fields to
    // the type's own top-level fields, the review's sources to behaviors
    // the type composes, and the gates to the type's Workflow and the
    // recorded fields.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ProvenanceConfig> {
        let schemas = target.field_schemas();
        let fields = match raw.get("fields") {
            Some(given) => {
                let fields = strings(Some(given));
                for field in &fields {
                    if !schemas.contains_key(field) {
                        return Err(Error::config(format!(
                            "fields: {} has no field {field}",
                            target.type_name()
                        )));
                    }
                }
                fields
            }
            None => target.fields().to_vec(),
        };
        let review = match raw.get("review").and_then(Value::as_object) {
            Some(review) => {
                let sources = strings(review.get("sources"));
                for source in &sources {
                    if source == NAME || !target.behaviors().iter().any(|named| named == source) {
                        return Err(Error::config(format!(
                            "review: source {source} is not another behavior {} composes",
                            target.type_name()
                        )));
                    }
                }
                Some(ProvenanceReview {
                    sources,
                    below: match review.get("below") {
                        Some(Value::Number(below)) => Some(number_value(below)),
                        _ => None,
                    },
                    permission: review
                        .get("permission")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                })
            }
            None => None,
        };
        let empty = Map::new();
        let raw_gates = raw
            .get("gates")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let states: Option<Vec<String>> = target
            .configs()
            .get("Workflow")
            .and_then(|flow| flow.get("states"))
            .map(|states| strings(Some(states)));
        let mut gates = Vec::new();
        for state in js_keys(raw_gates) {
            let Some(states) = &states else {
                return Err(Error::config(
                    "gates: the type has no Workflow whose moves they gate".to_owned(),
                ));
            };
            if !states.iter().any(|known| known == state) {
                return Err(Error::config(format!(
                    "gate \"{state}\" is not a state of the type's Workflow ({})",
                    states.join(", ")
                )));
            }
            let reviewed = &raw_gates[state]["reviewed"];
            let listed = if reviewed == "all" {
                fields.clone()
            } else {
                let listed = strings(Some(reviewed));
                for field in &listed {
                    if !fields.contains(field) {
                        return Err(Error::config(format!(
                            "gate \"{state}\": {field} is not a field Provenance records"
                        )));
                    }
                }
                listed
            };
            gates.push((state.to_owned(), listed));
        }
        Ok(ProvenanceConfig {
            fields,
            review,
            gates,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            after_config_change: Some(|_, cx| {
                let sql = cx.sql();
                let table = sql.table("records")?;
                let scope = [SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())];
                let Some(config) = cx.config() else {
                    sql.run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
                        &scope,
                    )?;
                    return Ok(());
                };
                let mut params = scope.to_vec();
                params.extend(
                    config
                        .fields
                        .iter()
                        .map(|field| SqlValue::from(field.as_str())),
                );
                let marks = vec!["?"; config.fields.len()].join(", ");
                sql.run(
                    format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND field NOT IN ({marks})"),
                    &params,
                )?;
                Ok(())
            }),
            migrations: vec![
                BehaviorMigration::new(1, "records")
                    .column("in_review", ColumnSpec::integer().not_null().default(0))
                    .index("in_review", &["in_review"])
                    .table(
                        TableSpec::new("records")
                            .row_id("record")
                            .column("namespace", ColumnSpec::text().not_null())
                            .column("schema", ColumnSpec::text().not_null())
                            .column("id", ColumnSpec::text().not_null())
                            .column("field", ColumnSpec::text().not_null())
                            .column("source", ColumnSpec::text().not_null())
                            .column("by_subject", ColumnSpec::text().not_null())
                            .column("at", ColumnSpec::integer().not_null())
                            .column("confidence", ColumnSpec::real())
                            .column("review", ColumnSpec::text())
                            .column("reviewed_by", ColumnSpec::text())
                            .column("reviewed_at", ColumnSpec::integer())
                            .unique(&["namespace", "schema", "id", "field"]),
                    ),
                // Who a write acted for (D77), beside who made it; a record
                // written before it acted for no one.
                BehaviorMigration::new(2, "acting for").up(|sql| {
                    sql.run(
                        format!(
                            "ALTER TABLE {} ADD COLUMN for_subject TEXT",
                            sql.table("records")?
                        ),
                        &[],
                    )?;
                    Ok(())
                }),
            ],
            guard: Some(guard),
            operations: vec![("accept", accept), ("reject", reject)],
            fields: vec![
                // Each recorded field's last write, in the config's order.
                ("fields", |_, view| {
                    let rows = records_of(view)?;
                    let mut out = Map::new();
                    for field in &view.config().fields {
                        let Some(row) = rows.iter().find(|row| {
                            row.opt_text("field").ok().flatten() == Some(field.as_str())
                        }) else {
                            continue;
                        };
                        let mut entry = Map::new();
                        entry.insert("source".into(), Value::from(row.text("source")?));
                        entry.insert("by".into(), Value::from(row.text("by_subject")?));
                        if let Some(for_subject) = row.opt_text("for_subject")? {
                            entry.insert("for".into(), Value::from(for_subject));
                        }
                        entry.insert("at".into(), Value::from(row.i64("at")?));
                        if let Some(confidence) = row.opt_f64("confidence")? {
                            entry.insert("confidence".into(), json!(confidence));
                        }
                        if let Some(state) = row.opt_text("review")? {
                            let mut review = Map::new();
                            review.insert("state".into(), Value::from(state));
                            if let Some(by) = row.opt_text("reviewed_by")? {
                                review.insert("by".into(), Value::from(by));
                            }
                            if let Some(at) = row.opt_i64("reviewed_at")? {
                                review.insert("at".into(), Value::from(at));
                            }
                            entry.insert("review".into(), Value::Object(review));
                        }
                        out.insert(field.clone(), Value::Object(entry));
                    }
                    Ok(Some(Value::Object(out)))
                }),
                ("inReview", |_, view| {
                    Ok(Some(Value::Bool(
                        view.columns().get()?.i64("in_review")? == 1,
                    )))
                }),
            ],
            filters: vec![(
                "inReview",
                BehaviorFilter::new("in_review", "boolean")
                    .described("Whether any recorded field is proposed."),
            )],
            after_change: Some(after_change),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(below: Option<f64>) -> ProvenanceConfig {
        ProvenanceConfig {
            fields: vec!["priority".to_owned()],
            review: Some(ProvenanceReview {
                sources: vec!["Derived".to_owned()],
                below,
                permission: "tickets.review".to_owned(),
            }),
            gates: Vec::new(),
        }
    }

    #[test]
    fn a_write_from_a_source_is_proposed_unless_its_confidence_reaches_below() {
        assert_eq!(
            review_of(&config(None), "Derived", Some(0.99), None),
            Some(PROPOSED)
        );
        assert_eq!(
            review_of(&config(Some(0.8)), "Derived", Some(0.6), None),
            Some(PROPOSED)
        );
        assert_eq!(
            review_of(&config(Some(0.8)), "Derived", None, None),
            Some(PROPOSED)
        );
        assert_eq!(
            review_of(&config(Some(0.8)), "Derived", Some(0.8), None),
            Some("accepted")
        );
    }

    #[test]
    fn another_write_over_a_proposal_edits_it_and_is_otherwise_not_reviewed() {
        assert_eq!(
            review_of(&config(None), "caller", None, Some(PROPOSED)),
            Some("edited")
        );
        assert_eq!(
            review_of(&config(None), "caller", None, Some("accepted")),
            None
        );
        assert_eq!(review_of(&config(None), "Revisions", None, None), None);
    }
}
