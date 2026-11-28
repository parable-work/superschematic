//! Provenance, who set each own field last and through what (D73), ported
//! from the engine's `provenance.rs` with its rules and words. Every write
//! of the row records, for each field the config records that the write
//! changed, its source: `caller` for a caller's create or update, or the
//! behavior whose operation wrote it, as the core attributes the write
//! ([`superschematic_rules::Change::Operation`]'s `attribution`); the
//! caller's subject, and the subject it acted for (D77); the time; and the
//! confidence the write's attribution gave.
//!
//! With review, a write from a source the review names is proposed, or
//! accepted with no reviewer when its confidence reaches `below`. A
//! reviewer holding the review's permission accepts a proposal as it is,
//! or rejects it: reject writes the field through [`Cx::update`], clearing
//! it or setting the patch's value, so Derived records the write as an
//! override and does not propose the field again. A write from any other
//! source over a proposal makes it edited. `inReview`, an owned field of
//! the row, is whether any recorded field is proposed, so a list filters on
//! it as on any column.
//!
//! A gate refuses a transition of the type's Workflow into its state while
//! a field it lists is proposed: `unreviewed_fields`, with the state and the
//! fields in its details. Like Approvals' gate, it lets through a move
//! Workflow refuses, for Workflow to refuse.
//!
//! The state is the use's: the owned field `inReview`, false from the
//! create, and the sub-table `records`, one row per recorded field, keyed by
//! the field's name. A field written before the table composed Provenance
//! has no record until its next write, and such a row's `inReview` holds no
//! value until a write records a field. `fields` is computed at a read from
//! the sub-table.
//!
//! Held to D103's test, Provenance passes by its review: a proposal's state
//! is its own, its rules say how it changes (proposed until a reviewer
//! accepts or rejects it, edited by another write, sure answers accepted
//! with no reviewer) and it gates the row's Workflow, and a model's answer
//! a person confirms recurs (a ticket's triage, an invoice's extraction).
//! Its record of who wrote each field alone is close to a storage shape:
//! D87's audit stamps keep who wrote the row and when, per row; what they
//! do not keep is the field, the behavior that wrote it and its confidence,
//! which the core alone knows.
//!
//! configChange: any config may change, and Provenance may be added to a
//! table that has rows. It cannot be removed from one: the records its rows
//! have would stay behind with nothing to delete them. A field the config
//! stops recording keeps its record, which no read lists.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    Row, SchemaIssue, SubRow, Veto, View,
};

use crate::declared;
use crate::derived::{list_issues, params_closed, strings, unknown_keys};
use crate::flow;
use crate::schema::{self, Issues};
use crate::values;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Provenance";

/// Provenance's declaration, read in place: the copy `make behaviors` writes of
/// the one the compiler registers.
pub const DECLARATION: &str = include_str!("../declarations/Provenance.behavior.json");

/// The sub-table of a row's records, keyed by the field's name.
pub const RECORDS: &str = "records";
/// The owned field that says whether any recorded field is proposed.
pub const IN_REVIEW: &str = "inReview";
/// The computed field of each recorded field's last write.
pub const FIELDS: &str = "fields";

const ACCEPT: &str = "accept";
const REJECT: &str = "reject";

/// A caller's create or update, as a record names its source.
const CALLER: &str = "caller";

const PROPOSED: &str = "proposed";
const ACCEPTED: &str = "accepted";
const REJECTED: &str = "rejected";
const EDITED: &str = "edited";

const CONFIG_KEYS: [&str; 3] = ["fields", "gates", "review"];
const REVIEW_KEYS: [&str; 3] = ["below", "permission", "sources"];

/// The review of a Provenance config.
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

/// Provenance's config, with `fields` and a gate's `all` filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct ProvenanceConfig {
    /// The own fields it records, in the config's order, or the type's.
    pub fields: Vec<String>,
    /// Which writes enter review; `None` when none does.
    pub review: Option<ProvenanceReview>,
    /// By state of the type's Workflow, the fields a move into it requires
    /// out of review, in the config's order.
    pub gates: Vec<(String, Vec<String>)>,
    /// The Workflow the gates hold, by its use's id, with its transitions;
    /// `None` without gates.
    pub flow: Option<(String, Vec<(String, String)>)>,
}

impl ProvenanceConfig {
    /// Reads a config held to the declaration's configSchema, in the schema
    /// loader's words, and to the type: the recorded fields its own fields,
    /// the review's sources other behaviors it composes, and the gates the
    /// states of its Workflow and recorded fields.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ProvenanceConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let type_name = target.type_name();
        let own = target.fields();
        let fields = match raw.get("fields") {
            Some(given) => {
                let fields = strings(Some(given));
                for field in &fields {
                    if !own.iter().any(|known| &known.name == field) {
                        return Err(format!("fields: {type_name} has no field {field}").into());
                    }
                }
                fields
            }
            None => own.iter().map(|field| field.name.clone()).collect(),
        };
        let review = match raw.get("review").and_then(Value::as_object) {
            Some(review) => {
                let sources = strings(review.get("sources"));
                for source in &sources {
                    let composed = target.uses().iter().any(|entry| entry.behavior == *source);
                    if source == NAME || !composed {
                        return Err(format!(
                            "review: source {source} is not another behavior {type_name} composes"
                        )
                        .into());
                    }
                }
                Some(ProvenanceReview {
                    sources,
                    below: review.get("below").and_then(Value::as_f64),
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
        let given = raw
            .get("gates")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let mut gates = Vec::new();
        let mut gated_flow = None;
        if !given.is_empty() {
            let flows = target
                .uses()
                .iter()
                .filter(|entry| entry.behavior == workflow::NAME)
                .count();
            if flows == 0 {
                return Err("gates: the type has no Workflow whose moves they gate".into());
            }
            let flow = flow::flow_of(target, None)?;
            for state in crate::derived::js_keys(given) {
                if flow.lacks(state) {
                    return Err(format!(
                        "gate \"{state}\" is not a state of the type's Workflow ({})",
                        flow.listed()
                    )
                    .into());
                }
                let reviewed = &given[state]["reviewed"];
                let listed = if reviewed == "all" {
                    fields.clone()
                } else {
                    let listed = strings(Some(reviewed));
                    for field in &listed {
                        if !fields.contains(field) {
                            return Err(format!(
                                "gate \"{state}\": {field} is not a field Provenance records"
                            )
                            .into());
                        }
                    }
                    listed
                };
                gates.push((state.to_owned(), listed));
            }
            gated_flow = Some((flow.use_id, flow.transitions));
        }
        Ok(ProvenanceConfig {
            fields,
            review,
            gates,
            flow: gated_flow,
        })
    }

    // review_of is the review a write from a source with a confidence takes,
    // over a record whose review was `prior`.
    fn review_of(
        &self,
        source: &str,
        confidence: Option<f64>,
        prior: Option<&str>,
    ) -> Option<&'static str> {
        match &self.review {
            Some(review) if review.sources.iter().any(|named| named == source) => {
                let sure = review
                    .below
                    .is_some_and(|below| confidence.is_some_and(|confidence| confidence >= below));
                Some(if sure { ACCEPTED } else { PROPOSED })
            }
            _ => (prior == Some(PROPOSED)).then_some(EDITED),
        }
    }
}

/// Provenance: who set each own field last, and its review.
#[derive(Clone, Copy, Debug, Default)]
pub struct Provenance;

impl Behavior for Provenance {
    type Config = ProvenanceConfig;

    // The declaration file names the owned field inReview, the computed
    // fields and the sub-table records.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ProvenanceConfig, ConfigError> {
        ProvenanceConfig::parse(raw, target)
    }

    // See the module's header: any config may change, and the behavior may
    // be added to a table with rows, not removed from one.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&ProvenanceConfig>,
        after: Option<&ProvenanceConfig>,
    ) -> Hook<Option<String>> {
        if before.is_none() || after.is_some() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(
            "the records its instances have would stay behind with nothing to delete them"
                .to_owned(),
        ))
    }

    fn check_params(
        &self,
        _config: &ProvenanceConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, ProvenanceConfig>, _params: &Value) -> Hook<()> {
        cx.set(IN_REVIEW, false)
    }

    // The gates: a transition of the type's Workflow into a gated state
    // waits until the fields the gate lists are out of review. A move
    // Workflow refuses is Workflow's to refuse, whichever of the two the
    // type lists first.
    fn guard(
        &self,
        view: &View<'_, ProvenanceConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        let Some((status, transitions)) = &config.flow else {
            return Ok(None);
        };
        let Some(params) = request.operation_of(status, workflow::TRANSITION) else {
            return Ok(None);
        };
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let Some((_, listed)) = config.gates.iter().find(|(state, _)| state == to) else {
            return Ok(None);
        };
        let rows = view.sub_rows(RECORDS)?;
        let proposed: Vec<String> = config
            .fields
            .iter()
            .filter(|field| listed.contains(field))
            .filter(|field| review_state(&rows, field) == Some(PROPOSED))
            .cloned()
            .collect();
        if proposed.is_empty() {
            return Ok(None);
        }
        let Some(from) = view
            .get_of(status, "status")
            .and_then(|status| status.as_str().map(str::to_owned))
        else {
            return Ok(None);
        };
        if !flow::leads(transitions, &from, to) {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert("state".into(), Value::from(to));
        details.insert("fields".into(), json!(proposed));
        Ok(Some(
            Veto::new(format!(
                "{} cannot move to {to} while {} {} in review",
                row_of(view),
                proposed.join(", "),
                if proposed.len() == 1 { "is" } else { "are" }
            ))
            .with_code("unreviewed_fields")
            .with_details(details),
        ))
    }

    fn operation(&self, cx: &Cx<'_, ProvenanceConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            ACCEPT => accept(cx, params),
            REJECT => reject(cx, params),
            _ => Err(cx.defect(format!("{name} is not an operation of Provenance"))),
        }
    }

    // after_change records the fields a write changed: who wrote each,
    // through what, and the review that takes. Its own reject records its
    // writes itself.
    fn after_change(&self, cx: &Cx<'_, ProvenanceConfig>, change: &Change<'_>) -> Hook<()> {
        let empty = Map::new();
        let (before, attribution, create) = match change {
            Change::Create => (&empty, None, true),
            Change::Update { before, .. } => (*before, None, false),
            Change::Operation { use_id, .. } if *use_id == cx.use_id() => return Ok(()),
            Change::Operation {
                before: Some(before),
                attribution,
                use_id,
                ..
            } => (*before, Some((*attribution, *use_id)), false),
            _ => return Ok(()),
        };
        let own = cx.own();
        let rows = cx.sub_rows(RECORDS)?;
        let subject = cx.caller().subject.clone();
        let acting_for = cx.caller().acting_for.clone();
        let config = cx.config();
        let mut wrote = false;
        for field in &config.fields {
            if values::equal(before.get(field), own.get(field))
                || (create && !values::held(&own, field))
            {
                continue;
            }
            let entry = attribution
                .and_then(|(attribution, _)| attribution)
                .and_then(|attribution| attribution.get(field));
            let source = match attribution {
                None => CALLER.to_owned(),
                Some((_, use_id)) => entry
                    .and_then(|entry| entry.get("behavior"))
                    .and_then(Value::as_str)
                    .map_or_else(|| behavior_of(cx, use_id), str::to_owned),
            };
            let confidence = entry.and_then(|entry| entry.get("confidence")).cloned();
            let prior = review_state(&rows, field);
            let review =
                config.review_of(&source, confidence.as_ref().and_then(Value::as_f64), prior);
            record(
                cx,
                &rows,
                field,
                &Written {
                    source: &source,
                    by: &subject,
                    acting_for: acting_for.as_deref(),
                    confidence,
                    review,
                    reviewed_by: (review == Some(EDITED)).then_some(subject.as_str()),
                },
            )?;
            wrote = true;
        }
        if wrote {
            mark(cx)?;
        }
        Ok(())
    }

    // fields, from the sub-table: each recorded field's last write, in the
    // config's order.
    fn read_fields(
        &self,
        config: &ProvenanceConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let rows = tables.get(RECORDS).map_or(&[][..], Vec::as_slice);
        let mut out = Map::new();
        for field in &config.fields {
            let Some(row) = rows.iter().find(|row| &row.key == field) else {
                continue;
            };
            let mut entry = Map::new();
            for (member, from) in [
                ("source", "source"),
                ("by", "bySubject"),
                ("for", "forSubject"),
                ("at", "writtenAt"),
                ("confidence", "confidence"),
            ] {
                if let Some(value) = row.value(from) {
                    entry.insert(member.into(), value.clone());
                }
            }
            if let Some(state) = row.value("review") {
                let mut review = Map::new();
                review.insert("state".into(), state.clone());
                if let Some(by) = row.value("reviewedBy") {
                    review.insert("by".into(), by.clone());
                }
                if let Some(at) = row.value("reviewedAt") {
                    review.insert("at".into(), at.clone());
                }
                entry.insert("review".into(), Value::Object(review));
            }
            out.insert(field.clone(), Value::Object(entry));
        }
        let mut fields = Map::new();
        fields.insert(FIELDS.into(), Value::Object(out));
        fields
    }
}

// The record of one write.
struct Written<'a> {
    source: &'a str,
    by: &'a str,
    /// The subject the writer acted for (D77).
    acting_for: Option<&'a str>,
    confidence: Option<Value>,
    review: Option<&'a str>,
    reviewed_by: Option<&'a str>,
}

// record writes a field's record, replacing what it held.
fn record(
    cx: &Cx<'_, ProvenanceConfig>,
    rows: &[SubRow],
    field: &str,
    written: &Written<'_>,
) -> Hook<()> {
    let now = cx.now();
    let mut values = Map::new();
    values.insert("source".into(), Value::from(written.source));
    values.insert("bySubject".into(), Value::from(written.by));
    values.insert(
        "forSubject".into(),
        written.acting_for.map_or(Value::Null, Value::from),
    );
    values.insert("writtenAt".into(), Value::from(now));
    values.insert(
        "confidence".into(),
        written.confidence.clone().unwrap_or(Value::Null),
    );
    values.insert(
        "review".into(),
        written.review.map_or(Value::Null, Value::from),
    );
    values.insert(
        "reviewedBy".into(),
        written.reviewed_by.map_or(Value::Null, Value::from),
    );
    let settled = written.review.is_some_and(|review| review != PROPOSED);
    values.insert(
        "reviewedAt".into(),
        if settled {
            Value::from(now)
        } else {
            Value::Null
        },
    );
    if rows.iter().any(|row| row.key == field) {
        cx.update_row(RECORDS, field, values)
    } else {
        values.retain(|_, value| !value.is_null());
        cx.insert_row(RECORDS, field, values)
    }
}

// mark keeps the row's inReview in line with its records, as the run has
// them, and answers it.
fn mark(cx: &Cx<'_, ProvenanceConfig>) -> Hook<bool> {
    let rows = cx.sub_rows(RECORDS)?;
    let reviewing = cx
        .config()
        .fields
        .iter()
        .any(|field| review_state(&rows, field) == Some(PROPOSED));
    cx.set(IN_REVIEW, reviewing)?;
    Ok(reviewing)
}

// review_state is a recorded field's review: proposed, accepted, rejected
// or edited; `None` for a field with no record, or whose write took none.
fn review_state<'r>(rows: &'r [SubRow], field: &str) -> Option<&'r str> {
    rows.iter()
        .find(|row| row.key == field)
        .and_then(|row| row.value("review"))
        .and_then(Value::as_str)
}

// behavior_of is the behavior of a use of the row's type, by its id.
fn behavior_of(cx: &Cx<'_, ProvenanceConfig>, use_id: &str) -> String {
    cx.read_spec(cx.type_name())
        .ok()
        .flatten()
        .and_then(|spec| spec.use_of(use_id).map(|(_, entry)| entry.behavior.clone()))
        .unwrap_or_else(|| use_id.to_owned())
}

// proposed_of checks the fields a review operation names: the caller a
// reviewer, and each field recorded and proposed.
fn proposed_of(cx: &Cx<'_, ProvenanceConfig>, fields: &[String]) -> Hook<()> {
    let config = cx.config();
    let row = row_of(cx);
    if let Some(review) = &config.review {
        if !cx.can(&review.permission)? {
            return Err(cx.forbidden(format!(
                "{} may not review {row}: it needs permission {}",
                cx.caller().subject,
                review.permission
            )));
        }
    }
    let rows = cx.sub_rows(RECORDS)?;
    for field in fields {
        if !config.fields.contains(field) {
            return Err(cx.veto(
                Veto::new(format!(
                    "Provenance does not record {field} of {}",
                    cx.type_name()
                ))
                .with_code("not_recorded"),
            ));
        }
        if review_state(&rows, field) != Some(PROPOSED) {
            return Err(cx.veto(
                Veto::new(format!("{field} of {row} is not proposed")).with_code("not_in_review"),
            ));
        }
    }
    Ok(())
}

// accept settles proposals as they are.
fn accept(cx: &Cx<'_, ProvenanceConfig>, params: &Value) -> Hook<Value> {
    let fields = strings(params.get("fields"));
    proposed_of(cx, &fields)?;
    for field in &fields {
        let mut values = Map::new();
        values.insert("review".into(), Value::from(ACCEPTED));
        values.insert(
            "reviewedBy".into(),
            Value::from(cx.caller().subject.as_str()),
        );
        values.insert("reviewedAt".into(), Value::from(cx.now()));
        cx.update_row(RECORDS, field, values)?;
    }
    let reviewing = mark(cx)?;
    Ok(json!({ "accepted": fields, "inReview": reviewing }))
}

// reject replaces proposals as the reviewer's own write: each field cleared,
// or set to the patch's value.
fn reject(cx: &Cx<'_, ProvenanceConfig>, params: &Value) -> Hook<Value> {
    let fields = strings(params.get("fields"));
    proposed_of(cx, &fields)?;
    let empty = Map::new();
    let given = params
        .get("patch")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    for name in crate::derived::js_keys(given) {
        if !fields.iter().any(|field| field == name) {
            return Err(cx.veto(
                Veto::new(format!(
                    "reject's patch sets {name}, which its fields do not name"
                ))
                .with_code("not_recorded"),
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
    let subject = cx.caller().subject.clone();
    let acting_for = cx.caller().acting_for.clone();
    let rows = cx.sub_rows(RECORDS)?;
    for field in &fields {
        record(
            cx,
            &rows,
            field,
            &Written {
                source: NAME,
                by: &subject,
                acting_for: acting_for.as_deref(),
                confidence: None,
                review: Some(REJECTED),
                reviewed_by: Some(&subject),
            },
        )?;
    }
    let reviewing = mark(cx)?;
    Ok(json!({ "rejected": fields, "inReview": reviewing }))
}

// row_of names the row a message is about: its type and its key.
fn row_of<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// The configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    if let Some(fields) = config.get("fields") {
        list_issues("/fields", fields, None, &mut issues);
    }
    if let Some(review) = config.get("review") {
        review_issues(review, &mut issues);
    }
    if let Some(gates) = config.get("gates") {
        gates_issues(gates, &mut issues);
    }
    issues
}

fn review_issues(review: &Value, issues: &mut Issues) {
    let at = "/review";
    let Some(members) = review.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &REVIEW_KEYS, issues);
    for key in ["sources", "permission"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(sources) = members.get("sources") {
        list_issues("/review/sources", sources, None, issues);
    }
    if let Some(permission) = members.get("permission") {
        schema::text_issues("/review/permission", permission, issues);
    }
    if let Some(below) = members.get("below") {
        match below.as_f64().filter(|_| below.is_number()) {
            None => issues.push("/review/below", "type", "must be number"),
            Some(below) => {
                if below < 0.0 {
                    issues.push("/review/below", "minimum", "must be >= 0");
                }
                if below > 1.0 {
                    issues.push("/review/below", "maximum", "must be <= 1");
                }
            }
        }
    }
}

fn gates_issues(gates: &Value, issues: &mut Issues) {
    let at = "/gates";
    let Some(named) = gates.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (state, gate) in named {
        // A key that is no state's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = schema::name_issues(state);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push(at, keyword, message);
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        let at = schema::member(at, state);
        let Some(members) = gate.as_object() else {
            issues.push(at, "type", "must be object");
            continue;
        };
        unknown_keys(&at, members, &["reviewed"], issues);
        match members.get("reviewed") {
            None => issues.push(&at, "required", "must have required property 'reviewed'"),
            Some(reviewed) => reviewed_issues(&schema::member(&at, "reviewed"), reviewed, issues),
        }
    }
}

// reviewed_issues lists what reviewed breaks of its oneOf, as ajv reports
// it: nothing when exactly one branch holds (a list of fields, or "all");
// else every branch's issues and the oneOf's own.
fn reviewed_issues(at: &str, reviewed: &Value, issues: &mut Issues) {
    let mut listed = Issues::default();
    list_issues(at, reviewed, None, &mut listed);
    let mut all = Issues::default();
    if reviewed != &Value::from("all") {
        all.push(at, "const", "must be equal to constant");
    }
    if listed.is_empty() != all.is_empty() {
        return;
    }
    issues.extend(listed);
    issues.extend(all);
    issues.push(at, "oneOf", "must match exactly one schema in oneOf");
}

// The operations' paramsSchemas, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = match operation {
        ACCEPT => &["fields"],
        REJECT => &["fields", "patch"],
        _ => return issues,
    };
    params_closed("", given, keys, &mut issues);
    match given.get("fields") {
        None => issues.push("", "required", "must have required property 'fields'"),
        Some(fields) => list_issues("/fields", fields, None, &mut issues),
    }
    if given.get("patch").is_some_and(|patch| !patch.is_object()) {
        issues.push("/patch", "type", "must be object");
    }
    issues
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
            flow: None,
        }
    }

    #[test]
    fn a_write_from_a_source_is_proposed_unless_its_confidence_reaches_below() {
        assert_eq!(
            config(None).review_of("Derived", Some(0.99), None),
            Some(PROPOSED)
        );
        assert_eq!(
            config(Some(0.8)).review_of("Derived", Some(0.6), None),
            Some(PROPOSED)
        );
        assert_eq!(
            config(Some(0.8)).review_of("Derived", None, None),
            Some(PROPOSED)
        );
        assert_eq!(
            config(Some(0.8)).review_of("Derived", Some(0.8), None),
            Some(ACCEPTED)
        );
    }

    #[test]
    fn another_write_over_a_proposal_edits_it_and_is_otherwise_not_reviewed() {
        assert_eq!(
            config(None).review_of(CALLER, None, Some(PROPOSED)),
            Some(EDITED)
        );
        assert_eq!(config(None).review_of(CALLER, None, Some(ACCEPTED)), None);
        assert_eq!(config(None).review_of("Revisions", None, None), None);
    }
}
