//! Derived, the core's own fields that a model outside the engine fills
//! (D73). The config names own top-level fields, each with the fields it
//! is derived from, an instruction and a model label, and optionally the
//! states of the type's Workflow it is derived in. The engine calls no
//! model, as Search calls no embedder (D16, amended: search's vectors): a
//! deriver holding the config's permission pulls what is stale
//! (staleDerivations), asks its model, and settles the answers
//! (settleDerivations) with the hash it pulled.
//!
//! A derived value is an own field, typed by the schema: the field's JSON
//! Schema is the output the deriver asks for, and a value the type refuses
//! is not written. applyDerived writes the values through the operation's
//! update(), attributed to Derived with each value's confidence (D16,
//! amended: an operation's update() attributes what it writes), so every
//! validate and every guard on the type judges them, and the instance's
//! event records them. settleDerivations invokes applyDerived once for
//! each instance, in a savepoint, so a refusal skips that instance's
//! items alone.
//!
//! The behavior keeps one row for each instance and derived field: the
//! hash of its inputs, instruction, model and output (its source hash),
//! the hash of its last settled value, who settled it and when, an
//! override, the failed attempts and whether a deriver is wanted. A field
//! is stale while its source hash differs from its settled hash, or
//! rederive forced it, and no write overrode it. afterChange keeps the
//! rows in the transaction of every change: it hashes the inputs again,
//! and when a caller's create or update, or another behavior's operation,
//! writes a derived field, it records the override, which stops the
//! derivation until rederive. A field whose inputs all hold nothing wants
//! no value.
//!
//! A failure, a value the type refuses and another behavior's veto count
//! an attempt in the behavior's own table and append no event, as a
//! settled vector appends none: the count changes what staleDerivations
//! lists and no read of an instance. After maxAttempts the field leaves
//! the queue until its source hash moves or rederive asks for it, and
//! failedDerivations lists it. The settled value stays while a field is
//! stale: it is still the best value the instance has.
//!
//! A field with states is listed and applied only while the status is one
//! of them; staleDerivations reads the statuses of a page's instances in
//! one read.
//!
//! configChange: any config may change, and Derived may be added to and
//! removed from a schema with instances. afterConfigChange hashes every
//! instance's inputs again, so a new instruction, model or type makes the
//! field stale and keeps its value, drops the rows of a field the config no
//! longer derives, and drops every row when the behavior goes. A field an
//! instance already holds when its row is first made counts as overridden
//! (`by: "existing"`), so adding Derived never writes over a value a
//! person gave.

use std::collections::HashSet;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, IndexSpec, InstanceCx, InstanceView,
    SchemaCx, SqlReader, SqlWriter, TableSpec,
};
use superschematic_engine::errors::{EngineErrorKind, SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, number_value, sha256_hex, stringify};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, Hooks, InstanceChange,
    OperationCx, Result,
};

use crate::staterules::status_of;

/// One derived field of a Derived config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct DerivedField {
    /// The own top-level field the value is written to.
    pub name: String,
    /// The own top-level fields the value is derived from, in the config's
    /// order.
    pub from: Vec<String>,
    /// What the model is asked.
    pub instruction: String,
    /// The label of the model, which the engine only compares.
    pub model: String,
    /// The states of the type's Workflow the field is derived in; every
    /// state when `None`.
    pub states: Option<Vec<String>>,
    /// The field's JSON Schema, as the describe document writes it: the
    /// output the deriver asks for.
    pub output: Value,
}

/// Derived's config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct DerivedConfig {
    /// The derived fields, in the config's order.
    pub fields: Vec<DerivedField>,
    /// The permission a deriver needs.
    pub permission: String,
    /// How many failed attempts a field takes before it waits.
    pub max_attempts: i64,
}

impl DerivedConfig {
    /// The derived field of a name; `None` when the config derives none.
    pub fn field(&self, name: &str) -> Option<&DerivedField> {
        self.fields.iter().find(|field| field.name == name)
    }
}

const NAME: &str = "Derived";

const COLUMNS: &str = "row, id, field, source_hash, settled_hash, model, confidence, settled_at, settled_by, overridden_by, overridden_at, attempts, failure, forced, wanted";

// inputs is a field's inputs that hold a value, in its from's order.
fn inputs(field: &DerivedField, data: &Map<String, Value>) -> Map<String, Value> {
    field
        .from
        .iter()
        .filter_map(|name| match data.get(name) {
            None | Some(Value::Null) => None,
            Some(value) => Some((name.clone(), value.clone())),
        })
        .collect()
}

// source_hash is the hash a value is derived under: of the model, the
// instruction, the output and the inputs. Inputs that all hold nothing
// want no value.
fn source_hash(field: &DerivedField, data: &Map<String, Value>) -> Option<String> {
    let inputs = inputs(field, data);
    (!inputs.is_empty()).then(|| {
        sha256_hex(&stringify(&json!([
            field.model,
            field.instruction,
            field.output,
            Value::Object(inputs)
        ])))
    })
}

// The behavior's rows of one instance's field, as its SQL names them.
fn key(namespace: &str, schema: &str, id: &str, field: &str) -> Vec<SqlValue> {
    vec![
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(id),
        SqlValue::from(field),
    ]
}

fn row_of(sql: &SqlReader<'_>, at: &[SqlValue]) -> Result<Option<Row>> {
    sql.get(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND id = ? AND field = ?",
            sql.table("fields")?
        ),
        at,
    )
}

// wanted is whether a deriver should compute a field: it has inputs, no
// write overrode it, its value is stale or rederive forced it, and it has
// attempts left.
fn wanted(
    source: Option<&str>,
    settled: Option<&str>,
    overridden: bool,
    forced: bool,
    attempts: i64,
    max_attempts: i64,
) -> bool {
    source.is_some() && !overridden && (forced || settled != source) && attempts < max_attempts
}

// refresh brings one field's row in line with the instance's fields: its
// source hash, a new one clearing the failed attempts, and the override a
// write gives, if any. A row first made for a field the instance already
// holds counts as overridden by `existing` when `existing` is set.
fn refresh(
    sql: &SqlWriter<'_>,
    config: &DerivedConfig,
    (namespace, schema, id): (&str, &str, &str),
    field: &DerivedField,
    data: &Map<String, Value>,
    overridden: Option<(&str, i64)>,
    existing: Option<i64>,
) -> Result<()> {
    let at = key(namespace, schema, id, &field.name);
    let table = sql.table("fields")?;
    let source = source_hash(field, data);
    let Some(row) = row_of(sql, &at)? else {
        let held = !matches!(data.get(&field.name), None | Some(Value::Null));
        let overridden = overridden.or(existing.filter(|_| held).map(|at| ("existing", at)));
        let wants = wanted(
            source.as_deref(),
            None,
            overridden.is_some(),
            false,
            0,
            config.max_attempts,
        );
        let mut values = at;
        values.extend([
            source.clone().map_or(SqlValue::Null, SqlValue::Text),
            overridden.map_or(SqlValue::Null, |(by, _)| SqlValue::from(by)),
            overridden.map_or(SqlValue::Null, |(_, at)| SqlValue::Integer(at)),
            SqlValue::Integer(i64::from(wants)),
        ]);
        sql.run(
            format!("INSERT INTO {table} (namespace, schema, id, field, source_hash, overridden_by, overridden_at, attempts, forced, wanted) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?)"),
            &values,
        )?;
        return Ok(());
    };
    let moved = row.opt_text("source_hash")? != source.as_deref();
    let attempts = if moved { 0 } else { row.i64("attempts")? };
    let forced = !moved && row.i64("forced")? == 1;
    let (by, at_time) = match overridden {
        Some((by, at)) => (Some(by.to_owned()), Some(at)),
        None => (
            row.opt_text("overridden_by")?.map(str::to_owned),
            row.opt_i64("overridden_at")?,
        ),
    };
    let wants = wanted(
        source.as_deref(),
        row.opt_text("settled_hash")?,
        at_time.is_some(),
        forced,
        attempts,
        config.max_attempts,
    );
    let failure = if moved {
        SqlValue::Null
    } else {
        row.opt_text("failure")?
            .map_or(SqlValue::Null, SqlValue::from)
    };
    sql.run(
        format!("UPDATE {table} SET source_hash = ?, overridden_by = ?, overridden_at = ?, attempts = ?, failure = ?, forced = ?, wanted = ? WHERE row = ?"),
        &[
            source.map_or(SqlValue::Null, SqlValue::Text),
            by.map_or(SqlValue::Null, SqlValue::Text),
            at_time.map_or(SqlValue::Null, SqlValue::Integer),
            SqlValue::Integer(attempts),
            failure,
            SqlValue::Integer(i64::from(forced)),
            SqlValue::Integer(i64::from(wants)),
            SqlValue::Integer(row.i64("row")?),
        ],
    )?;
    Ok(())
}

// count_attempt records a failed attempt at a field: the failure, if one
// is given, and whether attempts are left.
fn count_attempt(
    sql: &SqlWriter<'_>,
    config: &DerivedConfig,
    row: &Row,
    failure: Option<&Value>,
) -> Result<()> {
    let attempts = row.i64("attempts")? + 1;
    let wants = wanted(
        row.opt_text("source_hash")?,
        row.opt_text("settled_hash")?,
        row.opt_i64("overridden_at")?.is_some(),
        row.i64("forced")? == 1,
        attempts,
        config.max_attempts,
    );
    let failure = match failure {
        Some(failure) => SqlValue::Text(stringify(failure)),
        None => row
            .opt_text("failure")?
            .map_or(SqlValue::Null, SqlValue::from),
    };
    sql.run(
        format!(
            "UPDATE {} SET attempts = ?, failure = ?, wanted = ? WHERE row = ?",
            sql.table("fields")?
        ),
        &[
            SqlValue::Integer(attempts),
            failure,
            SqlValue::Integer(i64::from(wants)),
            SqlValue::Integer(row.i64("row")?),
        ],
    )?;
    Ok(())
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

// require_permission refuses a caller without the config's permission.
fn require_permission(
    can: bool,
    subject: &str,
    what: &str,
    schema: &str,
    namespace: &str,
    permission: &str,
) -> Result<()> {
    if can {
        return Ok(());
    }
    Err(EngineError::new(
        EngineErrorCode::Forbidden,
        format!(
            "{subject} may not {what} the derived fields of {schema} in namespace {namespace}: it needs {permission}"
        ),
    )
    .into())
}

fn params_error(operation: &str, issues: Vec<SchemaIssue>) -> Error {
    EngineError::operation_params(NAME, operation, issues).into()
}

// in_states is whether a status is one a field is derived in.
fn in_states(field: &DerivedField, status: Option<&str>) -> bool {
    field
        .states
        .as_ref()
        .is_none_or(|states| status.is_some_and(|status| states.iter().any(|s| s == status)))
}

// strings reads a list of strings the configSchema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

fn stale_derivations(
    _: &Derived,
    cx: &SchemaCx<'_, DerivedConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    require_permission(
        cx.can(&config.permission)?,
        &cx.principal().subject,
        "pull",
        cx.schema(),
        cx.namespace(),
        &config.permission,
    )?;
    let request = page_request(NAME, "staleDerivations", params)?;
    let sql = cx.sql();
    let mut values = vec![
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::Integer(request.after),
    ];
    let only = match params.get("field").and_then(Value::as_str) {
        Some(field) => {
            values.push(SqlValue::from(field));
            " AND field = ?"
        }
        None => "",
    };
    values.push(SqlValue::Integer(request.limit + 1));
    let rows = sql.all(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND wanted = 1 AND row > ?{only} ORDER BY row LIMIT ?",
            sql.table("fields")?
        ),
        &values,
    )?;
    let found = page(rows, request.limit, |row| {
        row.i64("row").unwrap_or_default()
    });
    let ids: Vec<&str> = found
        .items
        .iter()
        .filter_map(|row| row.opt_text("id").ok().flatten())
        .collect();
    let stated = config.fields.iter().any(|field| field.states.is_some());
    let wanted_fields: &[&str] = if stated { &["Workflow.status"] } else { &[] };
    let records = if ids.is_empty() {
        Vec::new()
    } else {
        cx.instances()
            .get_many(cx.schema(), &ids, Some(wanted_fields))?
    };
    let mut items = Vec::new();
    for row in &found.items {
        let id = row.text("id")?;
        let Some(field) = config.field(row.text("field")?) else {
            continue;
        };
        let Some((_, record)) = records.iter().find(|(found, _)| found == id) else {
            continue;
        };
        let status = record
            .behavior_field("Workflow", "status")
            .and_then(Value::as_str);
        if !in_states(field, status) {
            continue;
        }
        items.push(json!({
            "id": id,
            "field": field.name,
            "sourceHash": row.text("source_hash")?,
            "attempt": row.i64("attempts")? + 1,
            "instruction": field.instruction,
            "model": field.model,
            "inputs": Value::Object(inputs(field, &record.data)),
            "output": field.output,
        }));
    }
    Ok(json!({
        "items": items,
        "next": found.next.map_or(Value::Null, Value::String),
    }))
}

// settle_derivations writes the values instance by instance through
// applyDerived and counts the failures, skipping what no longer holds.
#[allow(clippy::too_many_lines)]
fn settle_derivations(
    _: &Derived,
    cx: &SchemaCx<'_, DerivedConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    require_permission(
        cx.can(&config.permission)?,
        &cx.principal().subject,
        "settle",
        cx.schema(),
        cx.namespace(),
        &config.permission,
    )?;
    let items = params
        .get("items")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default();
    let mut issues = Vec::new();
    let mut seen: HashSet<(&str, &str)> = HashSet::new();
    for (position, item) in items.iter().enumerate() {
        let id = item.get("id").and_then(Value::as_str).unwrap_or_default();
        let field = item
            .get("field")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if !seen.insert((id, field)) {
            issues.push(SchemaIssue::new(
                format!("/items/{position}"),
                format!("settles {field} of {id} a second time in one call"),
            ));
        }
        if item.get("value").is_some() == item.get("failure").is_some() {
            issues.push(SchemaIssue::new(
                format!("/items/{position}"),
                "holds a value or a failure, and not both".to_owned(),
            ));
        }
    }
    if !issues.is_empty() {
        return Err(params_error("settleDerivations", issues));
    }
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    let mut skipped = Vec::new();
    let skip = |skipped: &mut Vec<Value>, id: &str, field: &str, reason: &str| {
        skipped.push(json!({"id": id, "field": field, "reason": reason}));
    };
    let (mut settled, mut failed) = (0_i64, 0_i64);
    // The values of each instance, in the order their instances first come.
    let mut groups: Vec<(&str, Map<String, Value>)> = Vec::new();
    for item in items {
        let id = item.get("id").and_then(Value::as_str).unwrap_or_default();
        let name = item
            .get("field")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let presented = item.get("sourceHash").and_then(Value::as_str);
        if config.field(name).is_none() {
            skip(&mut skipped, id, name, "not_derived");
            continue;
        }
        let Some(row) = row_of(&sql, &key(namespace, schema, id, name))? else {
            skip(&mut skipped, id, name, "not_found");
            continue;
        };
        // An override outlasts a moved hash, so it is the reason given.
        if row.opt_i64("overridden_at")?.is_some() {
            skip(&mut skipped, id, name, "overridden");
            continue;
        }
        if row.opt_text("source_hash")? != presented {
            skip(&mut skipped, id, name, "moved");
            continue;
        }
        if let Some(failure) = item.get("failure") {
            count_attempt(&sql, config, &row, Some(failure))?;
            failed += 1;
            continue;
        }
        let mut entry = Map::new();
        entry.insert(
            "sourceHash".into(),
            Value::from(presented.unwrap_or_default()),
        );
        entry.insert("value".into(), item["value"].clone());
        for member in ["confidence", "usage"] {
            if let Some(value) = item.get(member) {
                entry.insert(member.into(), value.clone());
            }
        }
        match groups.iter_mut().find(|(group, _)| *group == id) {
            Some((_, fields)) => {
                fields.insert(name.to_owned(), Value::Object(entry));
            }
            None => {
                let mut fields = Map::new();
                fields.insert(name.to_owned(), Value::Object(entry));
                groups.push((id, fields));
            }
        }
    }
    for (id, fields) in groups {
        let names: Vec<String> = fields.keys().cloned().collect();
        let applied = cx.instances().invoke(
            schema,
            id,
            "applyDerived",
            &json!({ "fields": fields }),
            None,
        );
        let refused = match applied {
            Ok(_) => {
                settled += names.len() as i64;
                continue;
            }
            Err(Error::Engine(error)) => error,
            Err(other) => return Err(other),
        };
        // The refusal took the instance's writes back with its savepoint.
        let (reason, extra, counts) = match refused.kind() {
            EngineErrorKind::BehaviorVeto {
                behavior,
                veto_code,
                ..
            } if behavior == NAME => (veto_code.clone().unwrap_or_default(), Map::new(), false),
            EngineErrorKind::BehaviorVeto {
                behavior,
                veto_code,
                ..
            } => {
                let mut extra = Map::new();
                extra.insert("behavior".into(), Value::from(behavior.as_str()));
                if let Some(code) = veto_code {
                    extra.insert("code".into(), Value::from(code.as_str()));
                }
                ("vetoed".to_owned(), extra, true)
            }
            EngineErrorKind::InstanceValidation { issues } => {
                let mut extra = Map::new();
                extra.insert("issues".into(), json!(issues));
                ("invalid".to_owned(), extra, true)
            }
            _ if refused.code() == EngineErrorCode::NotFound => {
                ("not_found".to_owned(), Map::new(), false)
            }
            _ => return Err(Error::Engine(refused)),
        };
        for name in &names {
            let mut out = Map::new();
            out.insert("id".into(), Value::from(id));
            out.insert("field".into(), Value::from(name.as_str()));
            out.insert("reason".into(), Value::from(reason.as_str()));
            out.extend(extra.clone());
            skipped.push(Value::Object(out));
            if counts {
                if let Some(row) = row_of(&sql, &key(namespace, schema, id, name))? {
                    count_attempt(&sql, config, &row, None)?;
                }
            }
        }
    }
    Ok(json!({"settled": settled, "failed": failed, "skipped": skipped}))
}

fn failed_derivations(
    _: &Derived,
    cx: &SchemaCx<'_, DerivedConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    require_permission(
        cx.can(&config.permission)?,
        &cx.principal().subject,
        "list",
        cx.schema(),
        cx.namespace(),
        &config.permission,
    )?;
    let request = page_request(NAME, "failedDerivations", params)?;
    let sql = cx.sql();
    let rows = sql.all(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND attempts >= ? AND overridden_at IS NULL AND row > ? ORDER BY row LIMIT ?",
            sql.table("fields")?
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::Integer(config.max_attempts),
            SqlValue::Integer(request.after),
            SqlValue::Integer(request.limit + 1),
        ],
    )?;
    let found = page(rows, request.limit, |row| {
        row.i64("row").unwrap_or_default()
    });
    let items = found
        .items
        .iter()
        .map(|row| {
            let mut out = Map::new();
            out.insert("id".into(), Value::from(row.text("id")?));
            out.insert("field".into(), Value::from(row.text("field")?));
            out.insert("attempts".into(), Value::from(row.i64("attempts")?));
            if let Some(failure) = row.opt_text("failure")? {
                out.insert(
                    "failure".into(),
                    serde_json::from_str(failure).unwrap_or(Value::Null),
                );
            }
            Ok(Value::Object(out))
        })
        .collect::<Result<Vec<_>>>()?;
    Ok(json!({
        "items": items,
        "next": found.next.map_or(Value::Null, Value::String),
    }))
}

fn apply_derived(
    _: &Derived,
    cx: &OperationCx<'_, DerivedConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    require_permission(
        cx.can(&config.permission)?,
        &cx.principal().subject,
        "apply",
        cx.schema(),
        cx.namespace(),
        &config.permission,
    )?;
    let empty = Map::new();
    let given = params
        .get("fields")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let sql = cx.sql();
    let (namespace, schema, id) = (cx.namespace(), cx.schema(), cx.id());
    let stated = js_keys(given).iter().any(|name| {
        config
            .field(name)
            .is_some_and(|field| field.states.is_some())
    });
    let status = if stated { status_of(cx)? } else { None };
    let mut patch = Map::new();
    let mut attribution = Map::new();
    let mut rows = Vec::new();
    for name in js_keys(given) {
        let item = &given[name];
        let Some(field) = config.field(name) else {
            return Err(refuse(
                cx,
                "applyDerived",
                format!("{schema} derives no field {name}"),
                "not_derived",
            ));
        };
        let row = row_of(&sql, &key(namespace, schema, id, name))?.ok_or_else(|| {
            Error::Internal(format!("Derived keeps no row of {name} on {schema} {id}"))
        })?;
        if row.opt_i64("overridden_at")?.is_some() {
            return Err(refuse(
                cx,
                "applyDerived",
                format!(
                    "{name} of {schema} {id} was written by {}; rederive lets Derived write it again",
                    row.opt_text("overridden_by")?.unwrap_or_default()
                ),
                "overridden",
            ));
        }
        if !in_states(field, status.as_deref()) {
            return Err(refuse(
                cx,
                "applyDerived",
                format!(
                    "{schema} {id} is {}, and {name} is derived only in {}",
                    status.as_deref().unwrap_or("in no state"),
                    field.states.as_deref().unwrap_or_default().join(", ")
                ),
                "out_of_state",
            ));
        }
        let presented = item.get("sourceHash").and_then(Value::as_str);
        if row.opt_text("source_hash")? != presented {
            return Err(refuse(
                cx,
                "applyDerived",
                format!("{name} of {schema} {id} was derived from inputs, an instruction, a model or a type it no longer has"),
                "moved",
            ));
        }
        patch.insert(
            name.to_owned(),
            item.get("value").cloned().unwrap_or(Value::Null),
        );
        let mut entry = Map::new();
        if let Some(confidence) = item.get("confidence") {
            entry.insert("confidence".into(), confidence.clone());
        }
        attribution.insert(name.to_owned(), Value::Object(entry));
        rows.push((name, row, item.get("confidence").and_then(Value::as_f64)));
    }
    cx.update_attributed(&Value::Object(patch), &Value::Object(attribution))?;
    let table = sql.table("fields")?;
    let subject = cx.principal().subject.as_str();
    let mut settled = Vec::new();
    for (name, row, confidence) in rows {
        let field = config.field(name).map(|field| field.model.as_str());
        sql.run(
            format!("UPDATE {table} SET settled_hash = source_hash, model = ?, confidence = ?, settled_at = ?, settled_by = ?, attempts = 0, failure = NULL, forced = 0, wanted = 0 WHERE row = ?"),
            &[
                field.map_or(SqlValue::Null, SqlValue::from),
                confidence.map_or(SqlValue::Null, SqlValue::Real),
                SqlValue::Integer(cx.now()),
                SqlValue::from(subject),
                SqlValue::Integer(row.i64("row")?),
            ],
        )?;
        settled.push(Value::from(name));
    }
    Ok(json!({ "settled": settled }))
}

fn rederive(_: &Derived, cx: &OperationCx<'_, DerivedConfig>, params: &Value) -> Result<Value> {
    let config = cx.config();
    let named = params.get("fields").map(|fields| strings(Some(fields)));
    let names: Vec<String> = match named {
        Some(names) => names,
        None => config
            .fields
            .iter()
            .map(|field| field.name.clone())
            .collect(),
    };
    let (namespace, schema, id) = (cx.namespace(), cx.schema(), cx.id());
    for name in &names {
        if config.field(name).is_none() {
            return Err(refuse(
                cx,
                "rederive",
                format!("{schema} derives no field {name}"),
                "not_derived",
            ));
        }
    }
    let sql = cx.sql();
    let table = sql.table("fields")?;
    for name in &names {
        sql.run(
            format!("UPDATE {table} SET overridden_by = NULL, overridden_at = NULL, attempts = 0, failure = NULL, forced = 1, wanted = CASE WHEN source_hash IS NULL THEN 0 ELSE 1 END WHERE namespace = ? AND schema = ? AND id = ? AND field = ?"),
            &key(namespace, schema, id, name),
        )?;
    }
    Ok(json!({ "stale": stale_of(cx, config)? }))
}

// rows_of reads an instance's rows, by field.
fn rows_of<C>(view: &InstanceView<'_, C>) -> Result<Vec<Row>> {
    let sql = view.sql();
    sql.all(
        format!(
            "SELECT {COLUMNS} FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
            sql.table("fields")?
        ),
        &[
            SqlValue::from(view.namespace()),
            SqlValue::from(view.schema()),
            SqlValue::from(view.id()),
        ],
    )
}

// stale_of is the derived fields that are stale, in the config's order.
fn stale_of<C>(view: &InstanceView<'_, C>, config: &DerivedConfig) -> Result<Vec<Value>> {
    let rows = rows_of(view)?;
    let mut stale = Vec::new();
    for field in &config.fields {
        let Some(row) = rows
            .iter()
            .find(|row| row.opt_text("field").ok().flatten() == Some(field.name.as_str()))
        else {
            continue;
        };
        let source = row.opt_text("source_hash")?;
        if source.is_some()
            && row.opt_i64("overridden_at")?.is_none()
            && (row.i64("forced")? == 1 || row.opt_text("settled_hash")? != source)
        {
            stale.push(Value::from(field.name.as_str()));
        }
    }
    Ok(stale)
}

// Writer names who wrote a field in a change: a principal's subject, or a
// behavior's name.
type Writer = Box<dyn Fn(&str) -> String>;

// after_change keeps the rows in line with the instance, and records the
// override of a derived field a caller or another behavior wrote.
fn after_change(
    _: &Derived,
    cx: &InstanceCx<'_, DerivedConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    let config = cx.config();
    let sql = cx.sql();
    let instance = (cx.namespace(), cx.schema(), cx.id());
    if matches!(change, InstanceChange::Delete) {
        sql.run(
            format!(
                "DELETE FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
                sql.table("fields")?
            ),
            &[
                SqlValue::from(instance.0),
                SqlValue::from(instance.1),
                SqlValue::from(instance.2),
            ],
        )?;
        return Ok(());
    }
    let data = cx.data()?;
    let subject = cx.principal().subject.clone();
    let now = cx.now();
    let empty = Map::new();
    // What the change wrote of the own fields, and who wrote each.
    let (before, writer): (Option<&Map<String, Value>>, Writer) = match change {
        InstanceChange::Create => (Some(&empty), Box::new(move |_| subject.clone())),
        InstanceChange::Update { before, .. } => {
            (Some(*before), Box::new(move |_| subject.clone()))
        }
        InstanceChange::Operation {
            behavior,
            before,
            attribution,
            ..
        } => {
            let behavior = (*behavior).to_owned();
            let attribution = attribution.cloned();
            (
                *before,
                Box::new(move |field| {
                    attribution
                        .as_ref()
                        .and_then(|attribution| attribution.get(field))
                        .and_then(|entry| entry.get("behavior"))
                        .and_then(Value::as_str)
                        .map_or_else(|| behavior.clone(), str::to_owned)
                }),
            )
        }
        InstanceChange::Delete => return Ok(()),
    };
    for field in &config.fields {
        let written = before.is_some_and(|before| {
            before.get(&field.name) != data.get(&field.name)
                && !(matches!(change, InstanceChange::Create)
                    && matches!(data.get(&field.name), None | Some(Value::Null)))
        });
        let by = written.then(|| writer(&field.name)).filter(|by| by != NAME);
        refresh(
            &sql,
            config,
            instance,
            field,
            &data,
            by.as_deref().map(|by| (by, now)),
            None,
        )?;
    }
    Ok(())
}

/// Derived: own fields a model outside the engine fills.
#[derive(Clone, Copy, Debug, Default)]
pub struct Derived;

impl Behavior for Derived {
    type Config = DerivedConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Derived.behavior.json")
    }

    // The configSchema holds the shape; this holds every derived and input
    // field to the type's own top-level fields, a field to inputs that are
    // neither derived nor secret, its states to the type's Workflow, and a
    // derived field to one Constants does not keep.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<DerivedConfig> {
        let empty = Map::new();
        let fields = raw
            .get("fields")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let schemas = target.field_schemas();
        let states: Option<Vec<String>> = target
            .configs()
            .get("Workflow")
            .and_then(|flow| flow.get("states"))
            .map(|states| strings(Some(states)));
        let constants = strings(
            target
                .configs()
                .get("Constants")
                .and_then(|constants| constants.get("fields")),
        );
        let secrets = target.secret_fields();
        let mut parsed = Vec::new();
        for name in js_keys(fields) {
            let entry = &fields[name];
            let Some(output) = schemas.get(name) else {
                return Err(Error::config(format!(
                    "fields: {} has no field {name}",
                    target.type_name()
                )));
            };
            if constants.iter().any(|kept| kept == name) {
                return Err(Error::config(format!(
                    "fields: {name} is a field Constants keeps, which no model may write after the create"
                )));
            }
            let from = strings(entry.get("from"));
            for input in &from {
                if input == name {
                    return Err(Error::config(format!(
                        "fields: {name} is derived from itself"
                    )));
                }
                if fields.contains_key(input) {
                    return Err(Error::config(format!(
                        "fields: {name} is derived from {input}, which is derived too"
                    )));
                }
                if !schemas.contains_key(input) {
                    return Err(Error::config(format!(
                        "fields: {name} is derived from {input}, which {} does not have",
                        target.type_name()
                    )));
                }
                if secrets.contains(input) {
                    return Err(Error::config(format!(
                        "fields: {name} is derived from {input}, which is secret: a secret never goes to a model"
                    )));
                }
            }
            let field_states = entry.get("states").map(|given| strings(Some(given)));
            if let Some(given) = &field_states {
                let Some(states) = &states else {
                    return Err(Error::config(format!(
                        "fields: {name} names states, and the type has no Workflow"
                    )));
                };
                for state in given {
                    if !states.contains(state) {
                        return Err(Error::config(format!(
                            "fields: {name}: state \"{state}\" is not a state of the type's Workflow ({})",
                            states.join(", ")
                        )));
                    }
                }
            }
            parsed.push(DerivedField {
                name: name.to_owned(),
                from,
                instruction: entry
                    .get("instruction")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                model: entry
                    .get("model")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                states: field_states,
                output: output.clone(),
            });
        }
        Ok(DerivedConfig {
            fields: parsed,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            max_attempts: match raw.get("maxAttempts") {
                Some(Value::Number(count)) => number_value(count) as i64,
                _ => 3,
            },
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            // Any config may change, and the behavior may be added to and
            // removed from a schema with instances: afterConfigChange hashes
            // the inputs again, or drops the rows.
            config_change: Some(|_, _, _, _| Ok(None)),
            after_config_change: Some(|_, cx| {
                let sql = cx.sql();
                let table = sql.table("fields")?;
                let scope = [SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())];
                let Some(config) = cx.config() else {
                    sql.run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
                        &scope,
                    )?;
                    return Ok(());
                };
                let names: Vec<SqlValue> = config
                    .fields
                    .iter()
                    .map(|field| SqlValue::from(field.name.as_str()))
                    .collect();
                let marks = vec!["?"; names.len()].join(", ");
                let mut params = scope.to_vec();
                params.extend(names);
                sql.run(
                    format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND field NOT IN ({marks})"),
                    &params,
                )?;
                let (namespace, schema, now) = (cx.namespace(), cx.schema(), cx.now());
                cx.each_instance(|instance| {
                    for field in &config.fields {
                        refresh(
                            &sql,
                            config,
                            (namespace, schema, &instance.id),
                            field,
                            &instance.data,
                            None,
                            Some(now),
                        )?;
                    }
                    Ok(())
                })
            }),
            migrations: vec![BehaviorMigration::new(1, "fields").table(
                TableSpec::new("fields")
                    .row_id("row")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("field", ColumnSpec::text().not_null())
                    .column("source_hash", ColumnSpec::text())
                    .column("settled_hash", ColumnSpec::text())
                    .column("model", ColumnSpec::text())
                    .column("confidence", ColumnSpec::real())
                    .column("settled_at", ColumnSpec::integer())
                    .column("settled_by", ColumnSpec::text())
                    .column("overridden_by", ColumnSpec::text())
                    .column("overridden_at", ColumnSpec::integer())
                    .column("attempts", ColumnSpec::integer().not_null())
                    .column("failure", ColumnSpec::text())
                    .column("forced", ColumnSpec::integer().not_null())
                    .column("wanted", ColumnSpec::integer().not_null())
                    .unique(&["namespace", "schema", "id", "field"])
                    .index(
                        IndexSpec::new("fields_wanted", &["namespace", "schema", "row"])
                            .filter("wanted = 1"),
                    ),
            )],
            schema_operations: vec![
                ("staleDerivations", stale_derivations),
                ("settleDerivations", settle_derivations),
                ("failedDerivations", failed_derivations),
            ],
            operations: vec![("applyDerived", apply_derived), ("rederive", rederive)],
            fields: vec![
                // The last value settled, and the override, by field in the
                // config's order.
                ("fields", |_, view| {
                    let rows = rows_of(view)?;
                    let mut out = Map::new();
                    for field in &view.config().fields {
                        let Some(row) = rows.iter().find(|row| {
                            row.opt_text("field").ok().flatten() == Some(field.name.as_str())
                        }) else {
                            continue;
                        };
                        let mut entry = Map::new();
                        if let (Some(hash), Some(at)) =
                            (row.opt_text("settled_hash")?, row.opt_i64("settled_at")?)
                        {
                            entry.insert("sourceHash".into(), Value::from(hash));
                            if let Some(model) = row.opt_text("model")? {
                                entry.insert("model".into(), Value::from(model));
                            }
                            entry.insert("settledAt".into(), Value::from(at));
                            if let Some(by) = row.opt_text("settled_by")? {
                                entry.insert("settledBy".into(), Value::from(by));
                            }
                            if let Some(confidence) = row.opt_f64("confidence")? {
                                entry.insert("confidence".into(), json!(confidence));
                            }
                        }
                        if let (Some(by), Some(at)) = (
                            row.opt_text("overridden_by")?,
                            row.opt_i64("overridden_at")?,
                        ) {
                            entry.insert("overridden".into(), json!({"by": by, "at": at}));
                        }
                        if !entry.is_empty() {
                            out.insert(field.name.clone(), Value::Object(entry));
                        }
                    }
                    Ok(Some(Value::Object(out)))
                }),
                ("stale", |_, view| {
                    Ok(Some(Value::Array(stale_of(view, view.config())?)))
                }),
            ],
            after_change: Some(after_change),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn field() -> DerivedField {
        DerivedField {
            name: "category".to_owned(),
            from: vec!["subject".to_owned(), "body".to_owned()],
            instruction: "Classify the request.".to_owned(),
            model: "standard".to_owned(),
            states: None,
            output: json!({"type": "string", "enum": ["billing", "bug"]}),
        }
    }

    #[test]
    fn a_hash_covers_the_model_the_instruction_the_output_and_the_inputs_that_hold_a_value() {
        let data = |value: Value| value.as_object().cloned().unwrap_or_default();
        let both = source_hash(
            &field(),
            &data(json!({"subject": "Card", "body": "Declined"})),
        );
        assert!(both.is_some());
        assert_eq!(
            source_hash(&field(), &data(json!({"subject": "Card", "body": null}))),
            source_hash(&field(), &data(json!({"subject": "Card"})))
        );
        assert_ne!(
            source_hash(&field(), &data(json!({"subject": "Card"}))),
            both
        );
        assert_eq!(source_hash(&field(), &data(json!({"title": "x"}))), None);
        let mut other = field();
        other.instruction = "Classify it.".to_owned();
        assert_ne!(
            source_hash(
                &other,
                &data(json!({"subject": "Card", "body": "Declined"}))
            ),
            both
        );
    }

    #[test]
    fn a_field_is_wanted_with_inputs_no_override_a_stale_or_forced_value_and_attempts_left() {
        assert!(wanted(Some("a"), None, false, false, 0, 3));
        assert!(!wanted(Some("a"), Some("a"), false, false, 0, 3));
        assert!(wanted(Some("a"), Some("a"), false, true, 0, 3));
        assert!(!wanted(Some("a"), Some("b"), true, false, 0, 3));
        assert!(!wanted(Some("a"), Some("b"), false, false, 3, 3));
        assert!(!wanted(None, None, false, true, 0, 3));
    }
}
