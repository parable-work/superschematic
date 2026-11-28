//! Derived, own fields a model outside the core fills (D73), ported from
//! the engine's `derived.rs` with its rules and words. The config names
//! own fields, each with the own fields it is derived from, an instruction
//! and a model label, and optionally the states of the type's Workflow it
//! is derived in. The core calls no model: a deriver holding the config's
//! permission pulls what is stale (`staleDerivations`, an operation of the
//! table as a whole), asks its model, and settles the answers
//! (`settleDerivations`) with the hash it pulled.
//!
//! A derived value is an own field, typed by the table: the field's JSON
//! Schema, which the host gives in the type's spec ([`FieldSpec`]'s
//! `schema`), is the output the deriver asks for, and a value it refuses
//! (its type, its enum) is not written. `applyDerived` writes the values of
//! one row through [`Cx::update_attributed`], attributed to Derived with
//! each value's confidence (D73), so every use's validation and guard
//! judges them and Provenance hears where they came from.
//! `settleDerivations` calls it on each row ([`Cx::call_on`]), and a
//! refusal undoes that row's call alone.
//!
//! The state is the use's sub-table `derivations`, one row per derived
//! field, keyed by its name: the hash of its inputs, instruction, model and
//! output (its source hash), the hash of its last settled value, who
//! settled it and when, with what model and confidence, an override, the
//! failed attempts and the last failure, and whether rederive forced it. A
//! field is stale while its source hash differs from its settled hash, or
//! rederive forced it, and no write overrode it; a deriver wants it while it
//! is stale, its inputs hold a value and it has attempts left. Every write
//! of the row keeps the sub-table in line, in the write's own command: it
//! hashes the inputs again, a moved hash clearing the attempts, and when a
//! caller's create or update, or another behavior's operation, writes a
//! derived field it records the override, which stops the derivation until
//! `rederive`. A row the use reaches with no sub-table row for a field,
//! one written before the table composed Derived, counts a value it holds
//! as overridden by `existing`, so adding Derived never writes over a value
//! a person gave. `fields` and `stale` are computed at a read from the
//! sub-table.
//!
//! A failure, a value the field refuses and another behavior's veto count
//! an attempt and write no own field. The core has no write of a row's
//! sub-table outside an operation on it, so `settleDerivations` counts one
//! through `applyDerived` with the entry `{ sourceHash, attempt: { failure?
//! } }`, which only the use's own operation of the table may give: the
//! row's version moves with it, where the engine's count appended no event.
//! After `maxAttempts` the field leaves the queue until its source hash
//! moves or rederive asks for it, and `failedDerivations` lists it. The
//! settled value stays while a field is stale.
//!
//! `staleDerivations` and `failedDerivations` read the table's rows a page
//! at a time with their sub-table rows ([`superschematic_rules::ReadRequest::Rows`]):
//! a scan, where the engine kept an index of the wanted fields. A column the
//! scan could read by is an owned field the engine's declaration, which the
//! core reads in place, would list among Derived's fields, so it waits for
//! the engine to go (D103's step 7). A page lists at most `limit` fields,
//! in the rows' key order and each row's in the config's order, and its
//! cursor names the last row and field listed.
//!
//! Held to D103's test, Derived passes: its state is its own (each field's
//! hashes, override and attempts), its rules say how that state changes (a
//! moved input makes a field stale, a person's write stops it, failures
//! run out), and a field a model fills from other fields recurs across
//! applications (a ticket's category, an article's summary).
//!
//! configChange: any config may change, and Derived may be added to a table
//! that has rows, each row's sub-table filling at its next write. It cannot
//! be removed from one: the sub-table rows its rows have would stay behind
//! with nothing to delete them. A field the config stops deriving keeps its
//! sub-table row, which no read lists.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use sha2::{Digest, Sha256};
use superschematic_rules::{
    Behavior, Change, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec,
    GuardRequest, Hook, ReadRequest, Refusal, Row, SchemaIssue, Stop, SubRow, TableRef,
    ValidationIssue, Veto, View,
};

use crate::declared;
use crate::flow;
use crate::paging::{base64url_decode, base64url_encode, DEFAULT_PAGE_SIZE};
use crate::schema::{self, Issues};
use crate::values;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Derived";

/// Derived's declaration, read in place: the copy `make behaviors` writes of
/// the one the compiler registers.
pub const DECLARATION: &str = include_str!("../declarations/Derived.behavior.json");

/// The sub-table of a row's derived fields, keyed by the field's name.
pub const DERIVATIONS: &str = "derivations";
/// The computed field of each derived field's last value and override.
pub const FIELDS: &str = "fields";
/// The computed field of the derived fields that are stale.
pub const STALE: &str = "stale";

const STALE_DERIVATIONS: &str = "staleDerivations";
const SETTLE_DERIVATIONS: &str = "settleDerivations";
const FAILED_DERIVATIONS: &str = "failedDerivations";
const APPLY_DERIVED: &str = "applyDerived";
const REDERIVE: &str = "rederive";

/// The member of an applyDerived entry that counts a failed attempt, which
/// only the use's own settleDerivations gives.
const ATTEMPT: &str = "attempt";

/// Who a field held when its sub-table row was first made counts as
/// overridden by.
const EXISTING: &str = "existing";

const DEFAULT_MAX_ATTEMPTS: i64 = 3;

// The configSchema's and the paramsSchemas' bounds.
const MAX_FIELDS: usize = 16;
const MAX_FROM: usize = 16;
const MAX_NAME: usize = 64;
const MAX_INSTRUCTION: usize = 4000;
const MAX_MODEL: usize = 200;
const MAX_ATTEMPTS_BOUND: i64 = 10;
const MAX_LIMIT: i64 = 100;
const MAX_ITEMS: usize = 100;
const MAX_CLASS: usize = 64;
const MAX_MESSAGE: usize = 2000;
const MAX_MODEL_ID: usize = 200;

const CONFIG_KEYS: [&str; 3] = ["fields", "maxAttempts", "permission"];
const FIELD_KEYS: [&str; 4] = ["from", "instruction", "model", "states"];

/// One derived field of a Derived config.
#[derive(Clone, Debug, PartialEq)]
pub struct DerivedField {
    /// The own field the value is written to.
    pub name: String,
    /// The own fields the value is derived from, in the config's order.
    pub from: Vec<String>,
    /// What the model is asked.
    pub instruction: String,
    /// The label of the model, which the core only compares.
    pub model: String,
    /// The states of the type's Workflow the field is derived in; every
    /// state when `None`.
    pub states: Option<Vec<String>>,
    /// The field's JSON Schema, as the host gives it: the output the
    /// deriver asks for; `{}` when the host gives none.
    pub output: Value,
    /// The field's IR type's name, which a refusal of its value names.
    pub type_name: String,
}

/// Derived's config, with its defaults filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct DerivedConfig {
    /// The derived fields, in the config's order.
    pub fields: Vec<DerivedField>,
    /// The permission a deriver needs.
    pub permission: String,
    /// How many failed attempts a field takes before it waits.
    pub max_attempts: i64,
    /// The id of the Workflow use whose status a field's states name, when
    /// a field names states.
    pub status: Option<String>,
}

impl DerivedConfig {
    /// The derived field of a name; `None` when the config derives none.
    pub fn field(&self, name: &str) -> Option<&DerivedField> {
        self.fields.iter().find(|field| field.name == name)
    }

    /// Reads a config held to the declaration's configSchema, in the schema
    /// loader's words, and to the type: every derived field and input an
    /// own field of the type, no field derived from itself or from another
    /// derived field, none Constants keeps, and each field's states the
    /// type's Workflow's.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<DerivedConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let empty = Map::new();
        let given = raw
            .get("fields")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let own = target.fields();
        let kept = constants_of(target);
        let type_name = target.type_name();
        let mut fields = Vec::new();
        let mut status = None;
        for name in js_keys(given) {
            let entry = &given[name];
            let Some(spec) = own.iter().find(|field| field.name == name) else {
                return Err(format!("fields: {type_name} has no field {name}").into());
            };
            if kept.iter().any(|kept| kept == name) {
                return Err(format!(
                    "fields: {name} is a field Constants keeps, which no model may write after the create"
                )
                .into());
            }
            let from = strings(entry.get("from"));
            for input in &from {
                if input == name {
                    return Err(format!("fields: {name} is derived from itself").into());
                }
                if given.contains_key(input) {
                    return Err(format!(
                        "fields: {name} is derived from {input}, which is derived too"
                    )
                    .into());
                }
                if !own.iter().any(|field| &field.name == input) {
                    return Err(format!(
                        "fields: {name} is derived from {input}, which {type_name} does not have"
                    )
                    .into());
                }
            }
            let states = entry.get("states").map(|states| strings(Some(states)));
            if let Some(named) = &states {
                let flows = target
                    .uses()
                    .iter()
                    .filter(|entry| entry.behavior == workflow::NAME)
                    .count();
                if flows == 0 {
                    return Err(format!(
                        "fields: {name} names states, and the type has no Workflow"
                    )
                    .into());
                }
                let flow = flow::flow_of(target, None)?;
                for state in named {
                    if flow.lacks(state) {
                        return Err(format!(
                            "fields: {name}: state \"{state}\" is not a state of the type's Workflow ({})",
                            flow.listed()
                        )
                        .into());
                    }
                }
                status = Some(flow.use_id);
            }
            fields.push(DerivedField {
                name: name.to_owned(),
                from,
                instruction: text(entry, "instruction"),
                model: text(entry, "model"),
                states,
                output: output_of(spec),
                type_name: spec.type_name.clone(),
            });
        }
        Ok(DerivedConfig {
            fields,
            permission: text(raw, "permission"),
            max_attempts: raw
                .get("maxAttempts")
                .and_then(whole)
                .unwrap_or(DEFAULT_MAX_ATTEMPTS),
            status,
        })
    }
}

/// Derived: own fields a model outside the core fills.
#[derive(Clone, Copy, Debug, Default)]
pub struct Derived;

impl Behavior for Derived {
    type Config = DerivedConfig;

    // The declaration file names the computed fields and stale and the
    // sub-table derivations.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<DerivedConfig, ConfigError> {
        DerivedConfig::parse(raw, target)
    }

    // See the module's header: any config may change, and the behavior may
    // be added to a table with rows, not removed from one.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&DerivedConfig>,
        after: Option<&DerivedConfig>,
    ) -> Hook<Option<String>> {
        if before.is_none() || after.is_some() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(
            "the derivations its instances have would stay behind with nothing to delete them"
                .to_owned(),
        ))
    }

    fn check_params(
        &self,
        _config: &DerivedConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // An applyDerived entry that counts an attempt is the use's own
    // settleDerivations' alone: any other caller's is refused as its
    // paramsSchema would refuse the member.
    fn guard(
        &self,
        view: &View<'_, DerivedConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let GuardRequest::Operation {
            use_id,
            operation: APPLY_DERIVED,
            params,
            caller,
            caller_type,
            ..
        } = request
        else {
            return Ok(None);
        };
        if *use_id != view.use_id() {
            return Ok(None);
        }
        let own = *caller == Some(view.use_id()) && *caller_type == Some(view.type_name());
        if own {
            return Ok(None);
        }
        let issues: Vec<SchemaIssue> = entries(params)
            .filter(|(_, entry)| entry.get(ATTEMPT).is_some())
            .map(|(name, _)| {
                SchemaIssue::new(
                    schema::member("/fields", name),
                    format!("must NOT have additional properties: {ATTEMPT}"),
                )
            })
            .collect();
        if issues.is_empty() {
            return Ok(None);
        }
        Err(Refusal::operation_params(view.behavior(), view.use_id(), APPLY_DERIVED, issues).into())
    }

    fn operation(&self, cx: &Cx<'_, DerivedConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            STALE_DERIVATIONS => stale_derivations(cx, params),
            SETTLE_DERIVATIONS => settle_derivations(cx, params),
            FAILED_DERIVATIONS => failed_derivations(cx, params),
            APPLY_DERIVED => apply_derived(cx, params),
            REDERIVE => rederive(cx, params),
            _ => Err(cx.defect(format!("{name} is not an operation of Derived"))),
        }
    }

    // Every write of the row keeps its derivations in line, and records the
    // override of a derived field a caller or another behavior wrote.
    fn after_change(&self, cx: &Cx<'_, DerivedConfig>, change: &Change<'_>) -> Hook<()> {
        let empty = Map::new();
        let own = cx.own();
        let subject = cx.caller().subject.clone();
        let (before, create): (&Map<String, Value>, bool) = match change {
            Change::Create => (&empty, true),
            Change::Update { before, .. } => (before, false),
            Change::Operation {
                before: Some(before),
                ..
            } => (before, false),
            _ => return Ok(()),
        };
        let writer = |field: &str| -> String {
            match change {
                Change::Operation {
                    use_id,
                    attribution,
                    ..
                } => attribution
                    .and_then(|attribution| attribution.get(field))
                    .and_then(|entry| entry.get("behavior"))
                    .and_then(Value::as_str)
                    .map_or_else(|| (*use_id).to_owned(), str::to_owned),
                _ => subject.clone(),
            }
        };
        let rows = cx.sub_rows(DERIVATIONS)?;
        for field in &cx.config().fields {
            let written = !values::equal(before.get(&field.name), own.get(&field.name))
                && !(create && !values::held(&own, &field.name));
            let by = written.then(|| writer(&field.name)).filter(|by| by != NAME);
            let found = rows.iter().find(|row| row.key == field.name);
            refresh(cx, field, &own, found, by.as_deref(), !create)?;
        }
        Ok(())
    }

    // fields and stale, from the sub-table.
    fn read_fields(
        &self,
        config: &DerivedConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let rows = tables.get(DERIVATIONS).map_or(&[][..], Vec::as_slice);
        let mut fields = Map::new();
        fields.insert(FIELDS.into(), Value::Object(fields_of(config, rows)));
        fields.insert(STALE.into(), Value::Array(stale_of(config, rows)));
        fields
    }
}

/// A derived field's state, as its sub-table row holds it.
#[derive(Clone, Debug, Default, PartialEq)]
struct Derivation {
    source: Option<String>,
    settled: Option<String>,
    overridden_by: Option<String>,
    overridden_at: Option<i64>,
    attempts: i64,
    forced: bool,
}

impl Derivation {
    fn of(row: &SubRow) -> Self {
        let text = |member: &str| row.value(member).and_then(Value::as_str).map(str::to_owned);
        Derivation {
            source: text("sourceHash"),
            settled: text("settledHash"),
            overridden_by: text("overriddenBy"),
            overridden_at: row.value("overriddenAt").and_then(whole),
            attempts: row.value("attempts").and_then(whole).unwrap_or(0),
            forced: row.value("forced") == Some(&Value::Bool(true)),
        }
    }

    fn overridden(&self) -> bool {
        self.overridden_at.is_some()
    }

    // stale is whether its value is behind its inputs: they hold a value,
    // no write overrode it, and its hash moved or rederive forced it.
    fn stale(&self) -> bool {
        self.source.is_some() && !self.overridden() && (self.forced || self.settled != self.source)
    }

    // wanted is whether a deriver should compute it: stale, with attempts
    // left.
    fn wanted(&self, max_attempts: i64) -> bool {
        self.stale() && self.attempts < max_attempts
    }

    // failed is whether it ran out of attempts: listed by failedDerivations.
    fn failed(&self, max_attempts: i64) -> bool {
        !self.overridden() && self.attempts >= max_attempts
    }
}

// refresh brings one field's sub-table row in line with the row's own
// fields: its source hash, a new one clearing the attempts, the failure and
// rederive's force, and the override a write gives. A row first made for a
// field the row already holds, when the use reaches a row that predates it,
// counts as overridden by `existing`. It writes only what moved.
fn refresh(
    cx: &Cx<'_, DerivedConfig>,
    field: &DerivedField,
    own: &Map<String, Value>,
    found: Option<&SubRow>,
    by: Option<&str>,
    predates: bool,
) -> Hook<()> {
    let source = source_hash(field, own);
    let now = cx.now();
    let Some(found) = found else {
        let overridden = by
            .map(str::to_owned)
            .or_else(|| (predates && values::held(own, &field.name)).then(|| EXISTING.to_owned()));
        let mut values = Map::new();
        values.insert("sourceHash".into(), source.map_or(Value::Null, Value::from));
        if let Some(by) = overridden {
            values.insert("overriddenBy".into(), Value::from(by));
            values.insert("overriddenAt".into(), Value::from(now));
        }
        values.insert("attempts".into(), Value::from(0));
        values.insert("forced".into(), Value::Bool(false));
        return cx.insert_row(DERIVATIONS, &field.name, values);
    };
    let held = Derivation::of(found);
    let moved = held.source != source;
    let mut values = Map::new();
    if moved {
        values.insert("sourceHash".into(), source.map_or(Value::Null, Value::from));
        if held.attempts != 0 {
            values.insert("attempts".into(), Value::from(0));
        }
        if found.value("failure").is_some() {
            values.insert("failure".into(), Value::Null);
        }
        if held.forced {
            values.insert("forced".into(), Value::Bool(false));
        }
    }
    if let Some(by) = by {
        values.insert("overriddenBy".into(), Value::from(by));
        values.insert("overriddenAt".into(), Value::from(now));
    }
    if values.is_empty() {
        return Ok(());
    }
    cx.update_row(DERIVATIONS, &field.name, values)
}

// fields_of is each derived field's last value settled and its override,
// in the config's order; a field with neither is absent.
fn fields_of(config: &DerivedConfig, rows: &[SubRow]) -> Map<String, Value> {
    let mut out = Map::new();
    for field in &config.fields {
        let Some(row) = rows.iter().find(|row| row.key == field.name) else {
            continue;
        };
        let mut entry = Map::new();
        if let (Some(hash), Some(at)) = (row.value("settledHash"), row.value("settledAt")) {
            entry.insert("sourceHash".into(), hash.clone());
            if let Some(model) = row.value("model") {
                entry.insert("model".into(), model.clone());
            }
            entry.insert("settledAt".into(), at.clone());
            if let Some(by) = row.value("settledBy") {
                entry.insert("settledBy".into(), by.clone());
            }
            if let Some(confidence) = row.value("confidence") {
                entry.insert("confidence".into(), confidence.clone());
            }
        }
        if let (Some(by), Some(at)) = (row.value("overriddenBy"), row.value("overriddenAt")) {
            entry.insert("overridden".into(), json!({"by": by, "at": at}));
        }
        if !entry.is_empty() {
            out.insert(field.name.clone(), Value::Object(entry));
        }
    }
    out
}

// stale_of is the derived fields that are stale, in the config's order.
fn stale_of(config: &DerivedConfig, rows: &[SubRow]) -> Vec<Value> {
    config
        .fields
        .iter()
        .filter(|field| {
            rows.iter()
                .find(|row| row.key == field.name)
                .is_some_and(|row| Derivation::of(row).stale())
        })
        .map(|field| Value::from(field.name.as_str()))
        .collect()
}

// inputs is a field's inputs that hold a value, in its from's order.
fn inputs(field: &DerivedField, own: &Map<String, Value>) -> Map<String, Value> {
    field
        .from
        .iter()
        .filter(|name| values::held(own, name))
        .map(|name| (name.clone(), own[name.as_str()].clone()))
        .collect()
}

// source_hash is the hash a value is derived under: the SHA-256 of the
// model, the instruction, the output and the inputs, as JSON.stringify
// writes them, as the engine hashed them. Inputs that all hold nothing
// want no value.
fn source_hash(field: &DerivedField, own: &Map<String, Value>) -> Option<String> {
    let inputs = inputs(field, own);
    if inputs.is_empty() {
        return None;
    }
    let text = stringify(&json!([
        field.model,
        field.instruction,
        field.output,
        Value::Object(inputs)
    ]));
    let digest = Sha256::digest(text.as_bytes());
    Some(digest.iter().map(|byte| format!("{byte:02x}")).collect())
}

// require refuses a caller without the config's permission, in the
// engine's words but for the namespace, which a table has none of.
fn require(cx: &Cx<'_, DerivedConfig>, what: &str) -> Hook<()> {
    let permission = &cx.config().permission;
    if cx.can(permission)? {
        return Ok(());
    }
    Err(cx.forbidden(format!(
        "{} may not {what} the derived fields of {}: it needs {permission}",
        cx.caller().subject,
        cx.type_name()
    )))
}

// status_on is a row's status under the config's Workflow, when a field
// names states.
fn status_on<'r>(config: &DerivedConfig, row: &'r Row) -> Option<&'r str> {
    config
        .status
        .as_deref()
        .and_then(|use_id| workflow::status_of(row, use_id))
}

// in_states is whether a status is one a field is derived in.
fn in_states(field: &DerivedField, status: Option<&str>) -> bool {
    field.states.as_ref().is_none_or(|states| {
        status.is_some_and(|status| states.iter().any(|state| state == status))
    })
}

/// A page's position: the row and the field it lists after.
struct Cursor {
    key: String,
    field: String,
}

// A list operation's limit and cursor.
struct ListRequest {
    limit: usize,
    after: Option<Cursor>,
}

fn list_request(cx: &Cx<'_, DerivedConfig>, operation: &str, params: &Value) -> Hook<ListRequest> {
    let limit = params
        .get("limit")
        .and_then(whole)
        .unwrap_or(DEFAULT_PAGE_SIZE);
    let limit = usize::try_from(limit).unwrap_or(0);
    let Some(cursor) = params.get("cursor").and_then(Value::as_str) else {
        return Ok(ListRequest { limit, after: None });
    };
    let decoded = String::from_utf8_lossy(&base64url_decode(cursor)).into_owned();
    let after = decoded
        .strip_prefix("after:")
        .and_then(|rest| serde_json::from_str::<Value>(rest).ok())
        .and_then(|pair| {
            let pair = pair.as_array()?;
            match pair.as_slice() {
                [Value::String(key), Value::String(field)] => Some(Cursor {
                    key: key.clone(),
                    field: field.clone(),
                }),
                _ => None,
            }
        });
    match after {
        Some(after) => Ok(ListRequest {
            limit,
            after: Some(after),
        }),
        None => Err(Refusal::operation_params(
            cx.behavior(),
            cx.use_id(),
            operation,
            vec![SchemaIssue::new(
                "/cursor",
                "is not a cursor this operation returned",
            )],
        )
        .into()),
    }
}

fn cursor_of(key: &str, field: &str) -> String {
    base64url_encode(format!("after:{}", json!([key, field])).as_bytes())
}

// scan lists the fields `pick` keeps of the table's rows, in key order and
// each row's in the config's order, after the cursor: at most `limit`, with
// the cursor of the next page when more follow. The cursor's row is read
// again for its fields after the cursor's.
fn scan<T>(
    cx: &Cx<'_, DerivedConfig>,
    request: &ListRequest,
    pick: impl Fn(&DerivedField, &str, &Row, &SubRow) -> Option<T>,
) -> Hook<(Vec<T>, Option<String>)> {
    let config = cx.config();
    let (type_name, use_id) = (cx.type_name(), cx.use_id());
    let mut found: Vec<(String, String, T)> = Vec::new();
    let take = |found: &mut Vec<(String, String, T)>,
                key: &str,
                row: &Row,
                rows: &[SubRow],
                after: Option<&str>| {
        let mut passed = after.is_none();
        for field in &config.fields {
            if !passed {
                passed = after == Some(field.name.as_str());
                continue;
            }
            let Some(sub) = rows.iter().find(|sub| sub.key == field.name) else {
                continue;
            };
            if let Some(item) = pick(field, key, row, sub) {
                found.push((key.to_owned(), field.name.clone(), item));
            }
        }
    };
    let mut after = None;
    if let Some(cursor) = &request.after {
        if let Some(row) = cx.read_row(type_name, &cursor.key)? {
            let rows = cx.read_sub_rows(type_name, &cursor.key, use_id, DERIVATIONS)?;
            take(&mut found, &cursor.key, row, rows, Some(&cursor.field));
        }
        after = Some(cursor.key.clone());
    }
    let tables = [TableRef::new(use_id, DERIVATIONS)];
    while found.len() <= request.limit {
        let page = cx.read_rows(type_name, &Map::new(), &tables, after.as_deref())?;
        for keyed in &page.rows {
            take(
                &mut found,
                &keyed.key,
                &keyed.row,
                keyed.table(use_id, DERIVATIONS),
                None,
            );
        }
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => break,
        }
    }
    let more = found.len() > request.limit;
    found.truncate(request.limit);
    let next = more
        .then(|| found.last().map(|(key, field, _)| cursor_of(key, field)))
        .flatten();
    Ok((found.into_iter().map(|(_, _, item)| item).collect(), next))
}

fn page_json(items: Vec<Value>, next: Option<String>) -> Value {
    json!({ "items": items, "next": next.map_or(Value::Null, Value::String) })
}

// staleDerivations lists the fields a deriver should compute: each wanted
// one in its states, with its inputs, instruction, model, output, the hash
// to present and the attempt number.
fn stale_derivations(cx: &Cx<'_, DerivedConfig>, params: &Value) -> Hook<Value> {
    require(cx, "pull")?;
    let request = list_request(cx, STALE_DERIVATIONS, params)?;
    let only = params.get("field").and_then(Value::as_str);
    let config = cx.config();
    let (items, next) = scan(cx, &request, |field, key, row, sub| {
        let held = Derivation::of(sub);
        if only.is_some_and(|only| only != field.name)
            || !held.wanted(config.max_attempts)
            || !in_states(field, status_on(config, row))
        {
            return None;
        }
        Some(json!({
            "id": key,
            "field": field.name,
            "sourceHash": held.source,
            "attempt": held.attempts + 1,
            "instruction": field.instruction,
            "model": field.model,
            "inputs": Value::Object(inputs(field, &row.own)),
            "output": field.output,
        }))
    })?;
    Ok(page_json(items, next))
}

// failedDerivations lists the fields out of attempts, with their last
// failure.
fn failed_derivations(cx: &Cx<'_, DerivedConfig>, params: &Value) -> Hook<Value> {
    require(cx, "list")?;
    let request = list_request(cx, FAILED_DERIVATIONS, params)?;
    let config = cx.config();
    let (items, next) = scan(cx, &request, |field, key, _row, sub| {
        let held = Derivation::of(sub);
        if !held.failed(config.max_attempts) {
            return None;
        }
        let mut item = Map::new();
        item.insert("id".into(), Value::from(key));
        item.insert("field".into(), Value::from(field.name.as_str()));
        item.insert("attempts".into(), Value::from(held.attempts));
        if let Some(failure) = sub.value("failure") {
            item.insert("failure".into(), failure.clone());
        }
        Some(Value::Object(item))
    })?;
    Ok(page_json(items, next))
}

/// One settle item's values for applyDerived, by row in the order the rows
/// first come.
type Groups<'p> = Vec<(&'p str, Map<String, Value>)>;

fn group<'p>(groups: &mut Groups<'p>, id: &'p str, field: &str, entry: Value) {
    match groups.iter_mut().find(|(known, _)| *known == id) {
        Some((_, fields)) => {
            fields.insert(field.to_owned(), entry);
        }
        None => {
            let mut fields = Map::new();
            fields.insert(field.to_owned(), entry);
            groups.push((id, fields));
        }
    }
}

// settleDerivations writes the values row by row through applyDerived and
// counts the failures, skipping what no longer holds: a field the config
// does not derive, a row or a derivation that is not there, an overridden
// field, and a hash that moved.
fn settle_derivations(cx: &Cx<'_, DerivedConfig>, params: &Value) -> Hook<Value> {
    require(cx, "settle")?;
    let items = params
        .get("items")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default();
    let mut issues = Vec::new();
    let mut seen: Vec<(&str, &str)> = Vec::new();
    for (position, item) in items.iter().enumerate() {
        let (id, field) = (text_of(item, "id"), text_of(item, "field"));
        if seen.contains(&(id, field)) {
            issues.push(SchemaIssue::new(
                format!("/items/{position}"),
                format!("settles {field} of {id} a second time in one call"),
            ));
        }
        seen.push((id, field));
        if item.get("value").is_some() == item.get("failure").is_some() {
            issues.push(SchemaIssue::new(
                format!("/items/{position}"),
                "holds a value or a failure, and not both",
            ));
        }
    }
    if !issues.is_empty() {
        return Err(Refusal::operation_params(
            cx.behavior(),
            cx.use_id(),
            SETTLE_DERIVATIONS,
            issues,
        )
        .into());
    }
    let config = cx.config();
    let (type_name, use_id) = (cx.type_name(), cx.use_id());
    // Every row the items name, and its derivations, in one round.
    let mut reads = Vec::new();
    for (id, _) in &seen {
        let row = ReadRequest::row(type_name, *id);
        if !reads.contains(&row) {
            reads.push(row);
            reads.push(ReadRequest::sub_rows(type_name, *id, use_id, DERIVATIONS));
        }
    }
    cx.read_many(&reads)?;
    let mut skipped = Vec::new();
    let skip = |skipped: &mut Vec<Value>, id: &str, field: &str, reason: &str| {
        skipped.push(json!({"id": id, "field": field, "reason": reason}));
    };
    let mut values: Groups<'_> = Vec::new();
    let mut failures: Groups<'_> = Vec::new();
    let mut failed = 0_i64;
    for item in items {
        let (id, name) = (text_of(item, "id"), text_of(item, "field"));
        let presented = item.get("sourceHash").and_then(Value::as_str);
        if config.field(name).is_none() {
            skip(&mut skipped, id, name, "not_derived");
            continue;
        }
        if cx.row_of(type_name, id)?.is_none() {
            skip(&mut skipped, id, name, "not_found");
            continue;
        }
        let rows = cx.sub_rows_of(type_name, id, use_id, DERIVATIONS)?;
        let Some(held) = rows.iter().find(|row| row.key == name).map(Derivation::of) else {
            skip(&mut skipped, id, name, "not_found");
            continue;
        };
        // An override outlasts a moved hash, so it is the reason given.
        if held.overridden() {
            skip(&mut skipped, id, name, "overridden");
            continue;
        }
        if held.source.as_deref() != presented {
            skip(&mut skipped, id, name, "moved");
            continue;
        }
        if let Some(failure) = item.get("failure") {
            let attempt = json!({ "failure": failure });
            group(
                &mut failures,
                id,
                name,
                json!({ "sourceHash": presented, ATTEMPT: attempt }),
            );
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
        group(&mut values, id, name, Value::Object(entry));
    }
    let mut waiting: Option<Stop> = None;
    for (id, fields) in failures {
        match cx.call_on(
            type_name,
            id,
            use_id,
            APPLY_DERIVED,
            &json!({ "fields": fields }),
        ) {
            Ok(_) => {}
            Err(stop) if stop.is_waiting() => {
                waiting.get_or_insert(stop);
            }
            Err(_) if waiting.is_some() => {}
            // Another use refused the count, as a guard that holds every
            // writing operation of another behavior does (Lease's): the
            // fields keep their attempts and are skipped, as a refused
            // value's are.
            Err(Stop::Refuse(refusal)) => {
                let Some((reason, extra, _)) = settle_refusal(&refusal) else {
                    return Err(Stop::Refuse(refusal));
                };
                for name in fields.keys() {
                    let mut out = Map::new();
                    out.insert("id".into(), Value::from(id));
                    out.insert("field".into(), Value::from(name.as_str()));
                    out.insert("reason".into(), Value::from(reason.as_str()));
                    out.extend(extra.clone());
                    skipped.push(Value::Object(out));
                    failed -= 1;
                }
            }
            Err(stop) => return Err(stop),
        }
    }
    let mut settled = 0_i64;
    for (id, fields) in values {
        let names: Vec<String> = fields.keys().cloned().collect();
        let refusal = match cx.call_on(
            type_name,
            id,
            use_id,
            APPLY_DERIVED,
            &json!({ "fields": fields }),
        ) {
            Ok(_) => {
                settled += names.len() as i64;
                continue;
            }
            Err(stop) if stop.is_waiting() => {
                waiting.get_or_insert(stop);
                continue;
            }
            Err(_) if waiting.is_some() => continue,
            Err(Stop::Refuse(refusal)) => refusal,
            Err(stop) => return Err(stop),
        };
        // The refusal undid the row's call.
        let Some((reason, extra, counts)) = settle_refusal(&refusal) else {
            return Err(Stop::Refuse(refusal));
        };
        let mut attempts = Map::new();
        for name in &names {
            let mut out = Map::new();
            out.insert("id".into(), Value::from(id));
            out.insert("field".into(), Value::from(name.as_str()));
            out.insert("reason".into(), Value::from(reason.as_str()));
            out.extend(extra.clone());
            skipped.push(Value::Object(out));
            if counts {
                let presented = fields_hash(&fields, name);
                attempts.insert(
                    name.clone(),
                    json!({ "sourceHash": presented, ATTEMPT: {} }),
                );
            }
        }
        if !attempts.is_empty() {
            match cx.call_on(
                type_name,
                id,
                use_id,
                APPLY_DERIVED,
                &json!({ "fields": attempts }),
            ) {
                Ok(_) => {}
                Err(stop) if stop.is_waiting() => {
                    waiting.get_or_insert(stop);
                }
                // Another use refuses the count too: the field keeps its
                // attempts.
                Err(Stop::Refuse(_)) => {}
                Err(stop) => return Err(stop),
            }
        }
    }
    if let Some(stop) = waiting {
        return Err(stop);
    }
    Ok(json!({"settled": settled, "failed": failed, "skipped": skipped}))
}

fn fields_hash(fields: &Map<String, Value>, name: &str) -> Value {
    fields
        .get(name)
        .and_then(|entry| entry.get("sourceHash"))
        .cloned()
        .unwrap_or(Value::Null)
}

// settle_refusal is why a refused applyDerived skipped its fields: the
// reason, what the skip says beside it, and whether it counts an attempt;
// `None` for a refusal that refuses the settle.
fn settle_refusal(refusal: &Refusal) -> Option<(String, Map<String, Value>, bool)> {
    let detail = |member: &str| {
        refusal
            .details
            .as_ref()
            .and_then(|details| details.get(member))
            .cloned()
    };
    match refusal.class.as_str() {
        "BehaviorVetoError" if detail("behavior") == Some(Value::from(NAME)) => Some((
            detail("vetoCode")
                .and_then(|code| code.as_str().map(str::to_owned))
                .unwrap_or_default(),
            Map::new(),
            false,
        )),
        "BehaviorVetoError" => {
            let mut extra = Map::new();
            if let Some(behavior) = detail("behavior") {
                extra.insert("behavior".into(), behavior);
            }
            if let Some(code) = detail("vetoCode") {
                extra.insert("code".into(), code);
            }
            Some(("vetoed".to_owned(), extra, true))
        }
        "InstanceValidationError" => {
            let mut extra = Map::new();
            extra.insert("issues".into(), detail("issues").unwrap_or(json!([])));
            Some(("invalid".to_owned(), extra, true))
        }
        _ if refusal.code == Code::NotFound => Some(("not_found".to_owned(), Map::new(), false)),
        _ => None,
    }
}

// applyDerived writes derived values to the row's own fields, attributed to
// Derived with each one's confidence, and settles their derivations; an
// entry that counts an attempt (settleDerivations') counts it.
fn apply_derived(cx: &Cx<'_, DerivedConfig>, params: &Value) -> Hook<Value> {
    require(cx, "apply")?;
    let config = cx.config();
    let rows = cx.sub_rows(DERIVATIONS)?;
    let row = row_of(cx);
    let mut patch = Map::new();
    let mut attribution = Map::new();
    let mut issues = Vec::new();
    let mut settled: Vec<(String, Option<Value>)> = Vec::new();
    let mut counted: Vec<(String, i64, Option<Value>)> = Vec::new();
    for (name, item) in entries(params) {
        let Some(field) = config.field(name) else {
            return Err(cx.veto(
                Veto::new(format!("{} derives no field {name}", cx.type_name()))
                    .with_code("not_derived"),
            ));
        };
        let held = rows
            .iter()
            .find(|row| row.key == name)
            .map(Derivation::of)
            .unwrap_or_default();
        if let Some(attempt) = item.get(ATTEMPT) {
            counted.push((
                name.to_owned(),
                held.attempts + 1,
                attempt.get("failure").cloned(),
            ));
            continue;
        }
        if held.overridden() {
            return Err(cx.veto(
                Veto::new(format!(
                    "{name} of {} was written by {}; rederive lets Derived write it again",
                    row,
                    held.overridden_by.as_deref().unwrap_or_default()
                ))
                .with_code("overridden"),
            ));
        }
        let status = config
            .status
            .as_deref()
            .and_then(|use_id| cx.get_of(use_id, "status"))
            .and_then(|status| status.as_str().map(str::to_owned));
        if !in_states(field, status.as_deref()) {
            return Err(cx.veto(
                Veto::new(format!(
                    "{row} is {}, and {name} is derived only in {}",
                    status.as_deref().unwrap_or("in no state"),
                    field.states.as_deref().unwrap_or_default().join(", ")
                ))
                .with_code("out_of_state"),
            ));
        }
        let presented = item.get("sourceHash").and_then(Value::as_str);
        if held.source.is_none() || held.source.as_deref() != presented {
            return Err(cx.veto(
                Veto::new(format!(
                    "{name} of {row} was derived from inputs, an instruction, a model or a type it no longer has"
                ))
                .with_code("moved"),
            ));
        }
        let value = item.get("value").cloned().unwrap_or(Value::Null);
        issues.extend(value_issues(field, &value));
        patch.insert(name.to_owned(), value);
        let mut entry = Map::new();
        if let Some(confidence) = item.get("confidence") {
            entry.insert("confidence".into(), confidence.clone());
        }
        attribution.insert(name.to_owned(), Value::Object(entry));
        settled.push((name.to_owned(), item.get("confidence").cloned()));
    }
    if !issues.is_empty() {
        return Err(Refusal::invalid_instance(cx.type_name(), issues).into());
    }
    if !patch.is_empty() {
        cx.update_attributed(&Value::Object(patch), &Value::Object(attribution))?;
    }
    let subject = cx.caller().subject.clone();
    for (name, confidence) in &settled {
        let model = config.field(name).map(|field| field.model.clone());
        let held = rows.iter().find(|row| &row.key == name);
        let source = held.and_then(|row| row.value("sourceHash")).cloned();
        let mut values = Map::new();
        values.insert("settledHash".into(), source.unwrap_or(Value::Null));
        values.insert("model".into(), model.map_or(Value::Null, Value::from));
        values.insert(
            "confidence".into(),
            confidence.clone().unwrap_or(Value::Null),
        );
        values.insert("settledAt".into(), Value::from(cx.now()));
        values.insert("settledBy".into(), Value::from(subject.as_str()));
        values.insert("attempts".into(), Value::from(0));
        values.insert("failure".into(), Value::Null);
        values.insert("forced".into(), Value::Bool(false));
        cx.update_row(DERIVATIONS, name, values)?;
    }
    for (name, attempts, failure) in counted {
        let mut values = Map::new();
        values.insert("attempts".into(), Value::from(attempts));
        if let Some(failure) = failure {
            values.insert("failure".into(), failure);
        }
        cx.update_row(DERIVATIONS, &name, values)?;
    }
    let names: Vec<Value> = settled
        .into_iter()
        .map(|(name, _)| Value::from(name))
        .collect();
    Ok(json!({ "settled": names }))
}

// rederive asks for fields again: their overrides, attempts and failures
// cleared, and each forced stale while its inputs hold a value. A row that
// predates the use gets the fields' derivations here.
fn rederive(cx: &Cx<'_, DerivedConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let names: Vec<String> = match params.get("fields") {
        Some(fields) => strings(Some(fields)),
        None => config
            .fields
            .iter()
            .map(|field| field.name.clone())
            .collect(),
    };
    for name in &names {
        if config.field(name).is_none() {
            return Err(cx.veto(
                Veto::new(format!("{} derives no field {name}", cx.type_name()))
                    .with_code("not_derived"),
            ));
        }
    }
    let own = cx.own();
    let rows = cx.sub_rows(DERIVATIONS)?;
    for name in &names {
        let Some(field) = config.field(name) else {
            continue;
        };
        let mut values = Map::new();
        values.insert("overriddenBy".into(), Value::Null);
        values.insert("overriddenAt".into(), Value::Null);
        values.insert("attempts".into(), Value::from(0));
        values.insert("failure".into(), Value::Null);
        values.insert("forced".into(), Value::Bool(true));
        if rows.iter().any(|row| &row.key == name) {
            cx.update_row(DERIVATIONS, name, values)?;
        } else {
            values.insert(
                "sourceHash".into(),
                source_hash(field, &own).map_or(Value::Null, Value::from),
            );
            values.retain(|_, value| !value.is_null());
            cx.insert_row(DERIVATIONS, name, values)?;
        }
    }
    let rows = cx.sub_rows(DERIVATIONS)?;
    Ok(json!({ "stale": stale_of(config, &rows) }))
}

// row_of names the row a message is about: its type and its key.
fn row_of<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// value_issues holds a derived value to its field's JSON Schema, as the
// table's validators word it: its enum, then its type. A null is a value
// only a field whose schema admits null, or gives no type, takes.
fn value_issues(field: &DerivedField, value: &Value) -> Vec<ValidationIssue> {
    let schema = &field.output;
    let types: Vec<&str> = match schema.get("type") {
        Some(Value::String(one)) => vec![one.as_str()],
        Some(Value::Array(many)) => many.iter().filter_map(Value::as_str).collect(),
        _ => Vec::new(),
    };
    if value.is_null() {
        if types.is_empty() || types.contains(&"null") {
            return Vec::new();
        }
        return vec![ValidationIssue::new(
            &field.name,
            "required",
            format!("{} is required.", field.type_name),
        )];
    }
    if let Some(allowed) = schema.get("enum").and_then(Value::as_array) {
        if !value.is_string() {
            return vec![ValidationIssue::new(
                &field.name,
                "type",
                format!("{} must be a string.", field.type_name),
            )];
        }
        if !allowed
            .iter()
            .any(|known| values::equal(Some(known), Some(value)))
        {
            return vec![ValidationIssue::new(
                &field.name,
                "enum",
                format!("{} must be one of the allowed values.", field.type_name),
            )];
        }
        return Vec::new();
    }
    let fits = |kind: &str| match kind {
        "string" => value.is_string(),
        "number" => value.is_number(),
        "integer" => value.as_number().is_some_and(|_| whole(value).is_some()),
        "boolean" => value.is_boolean(),
        "object" => value.is_object(),
        "array" => value.is_array(),
        _ => true,
    };
    match types.iter().find(|kind| **kind != "null") {
        Some(kind) if !types.iter().any(|kind| fits(kind)) => {
            let expected = match *kind {
                "integer" | "array" | "object" => format!("an {kind}"),
                _ => format!("a {kind}"),
            };
            vec![ValidationIssue::new(
                &field.name,
                "type",
                format!("expected {expected}"),
            )]
        }
        _ => Vec::new(),
    }
}

// output_of is the output a deriver asks for a field: its JSON Schema, as
// the host gives it, or `{}`.
fn output_of(spec: &FieldSpec) -> Value {
    spec.schema
        .clone()
        .unwrap_or_else(|| Value::Object(Map::new()))
}

// constants_of is the fields the type's Constants keeps.
fn constants_of(target: &ConfigTarget<'_>) -> Vec<String> {
    target
        .uses()
        .iter()
        .filter(|entry| entry.behavior == crate::constants::NAME)
        .flat_map(|entry| {
            strings(
                entry
                    .config
                    .as_ref()
                    .and_then(|config| config.get("fields")),
            )
        })
        .collect()
}

// entries is an applyDerived's fields, by name, in the order JavaScript
// iterates them.
fn entries(params: &Value) -> impl Iterator<Item = (&str, &Value)> {
    let fields = params.get("fields").and_then(Value::as_object);
    fields
        .map(|fields| {
            js_keys(fields)
                .into_iter()
                .map(|name| (name, &fields[name]))
                .collect::<Vec<_>>()
        })
        .unwrap_or_default()
        .into_iter()
}

fn text(value: &Value, member: &str) -> String {
    text_of(value, member).to_owned()
}

fn text_of<'v>(value: &'v Value, member: &str) -> &'v str {
    value
        .get(member)
        .and_then(Value::as_str)
        .unwrap_or_default()
}

// strings reads a list of strings a schema let through.
pub(crate) fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
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

/// An object's keys in the order a JavaScript object iterates them: the
/// keys that are array indexes (`0` to 2^32 - 2, with no leading zero)
/// ascending, then the rest in the order they were inserted.
pub(crate) fn js_keys(members: &Map<String, Value>) -> Vec<&str> {
    let index = |key: &str| -> Option<u32> {
        if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        if key.len() > 1 && key.starts_with('0') {
            return None;
        }
        key.parse::<u32>().ok().filter(|index| *index != u32::MAX)
    };
    let mut indexes: Vec<(u32, &str)> = Vec::new();
    let mut rest: Vec<&str> = Vec::new();
    for key in members.keys() {
        match index(key) {
            Some(at) => indexes.push((at, key)),
            None => rest.push(key),
        }
    }
    indexes.sort_unstable();
    indexes
        .into_iter()
        .map(|(_, key)| key)
        .chain(rest)
        .collect()
}

/// `JSON.stringify(value)`, compact: an object's members in the order
/// [`js_keys`] gives, a number as JavaScript writes the double it reads.
pub(crate) fn stringify(value: &Value) -> String {
    let mut out = String::new();
    write_js(value, &mut out);
    out
}

fn write_js(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Value::Number(number) => {
            let double = match number.as_i64() {
                Some(whole) => whole as f64,
                None => number.to_string().parse::<f64>().unwrap_or(f64::NAN),
            };
            if !double.is_finite() {
                out.push_str("null");
            } else if double == 0.0 {
                out.push('0');
            } else {
                out.push_str(ryu_js::Buffer::new().format(double));
            }
        }
        Value::String(text) => quote_into(text, out),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_js(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => {
            out.push('{');
            for (index, key) in js_keys(members).into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                quote_into(key, out);
                out.push(':');
                write_js(&members[key], out);
            }
            out.push('}');
        }
    }
}

// quote_into writes a string literal with JavaScript's escapes.
fn quote_into(text: &str, out: &mut String) {
    out.push('"');
    for c in text.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

// The configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    for key in ["fields", "permission"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(permission) = config.get("permission") {
        schema::text_issues("/permission", permission, &mut issues);
    }
    if let Some(max) = config.get("maxAttempts") {
        schema::integer_issues(
            "/maxAttempts",
            max,
            Some(1),
            Some(MAX_ATTEMPTS_BOUND),
            &mut issues,
        );
    }
    if let Some(fields) = config.get("fields") {
        fields_issues(fields, &mut issues);
    }
    issues
}

fn fields_issues(fields: &Value, issues: &mut Issues) {
    let at = "/fields";
    let Some(named) = fields.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    if named.len() > MAX_FIELDS {
        issues.push(
            at,
            "maxProperties",
            format!("must NOT have more than {MAX_FIELDS} properties"),
        );
    }
    for (name, entry) in named {
        // A name its propertyNames refuses fails at the object, after the
        // name's own failures.
        let length = schema::length(name);
        if !(1..=MAX_NAME).contains(&length) {
            if length < 1 {
                issues.push(at, "minLength", "must NOT have fewer than 1 characters");
            } else {
                issues.push(
                    at,
                    "maxLength",
                    format!("must NOT have more than {MAX_NAME} characters"),
                );
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        field_issues(&schema::member(at, name), entry, issues);
    }
}

fn field_issues(at: &str, entry: &Value, issues: &mut Issues) {
    let Some(members) = entry.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &FIELD_KEYS, issues);
    for key in ["from", "instruction", "model"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(from) = members.get("from") {
        list_issues(&schema::member(at, "from"), from, Some(MAX_FROM), issues);
    }
    if let Some(states) = members.get("states") {
        list_issues(&schema::member(at, "states"), states, None, issues);
    }
    for (key, max) in [("instruction", MAX_INSTRUCTION), ("model", MAX_MODEL)] {
        if let Some(value) = members.get(key) {
            bounded_text(&schema::member(at, key), value, 1, max, issues);
        }
    }
}

// list_issues lists what a list of names breaks of `{ type: array,
// minItems: 1, maxItems?, uniqueItems: true, items: { type: string,
// minLength: 1 } }`.
pub(crate) fn list_issues(at: &str, value: &Value, max: Option<usize>, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if let Some(max) = max.filter(|max| items.len() > *max) {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {max} items"),
        );
    }
    for (i, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// bounded_text lists what a value breaks of `{ type: string, minLength,
// maxLength }`.
fn bounded_text(at: &str, value: &Value, min: usize, max: usize, issues: &mut Issues) {
    let Some(text) = value.as_str() else {
        issues.push(at, "type", "must be string");
        return;
    };
    let length = schema::length(text);
    if length < min {
        issues.push(
            at,
            "minLength",
            format!("must NOT have fewer than {min} characters"),
        );
    }
    if length > max {
        issues.push(
            at,
            "maxLength",
            format!("must NOT have more than {max} characters"),
        );
    }
}

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
pub(crate) fn unknown_keys(
    at: &str,
    members: &Map<String, Value>,
    known: &[&str],
    issues: &mut Issues,
) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}

// params_closed lists the members a closed parameter object does not take,
// in the behavior registry's words.
pub(crate) fn params_closed(
    at: &str,
    members: &Map<String, Value>,
    known: &[&str],
    issues: &mut Issues,
) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
}

// The operations' paramsSchemas, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    match operation {
        STALE_DERIVATIONS | FAILED_DERIVATIONS => {
            let keys: &[&str] = if operation == STALE_DERIVATIONS {
                &["cursor", "field", "limit"]
            } else {
                &["cursor", "limit"]
            };
            params_closed("", given, keys, &mut issues);
            if let Some(limit) = given.get("limit") {
                schema::integer_issues("/limit", limit, Some(1), Some(MAX_LIMIT), &mut issues);
            }
            if given
                .get("cursor")
                .is_some_and(|cursor| !cursor.is_string())
            {
                issues.push("/cursor", "type", "must be string");
            }
            if let Some(field) = given.get("field") {
                schema::text_issues("/field", field, &mut issues);
            }
        }
        SETTLE_DERIVATIONS => {
            params_closed("", given, &["items"], &mut issues);
            match given.get("items") {
                None => issues.push("", "required", "must have required property 'items'"),
                Some(Value::Array(items)) => {
                    if items.len() > MAX_ITEMS {
                        issues.push(
                            "/items",
                            "maxItems",
                            format!("must NOT have more than {MAX_ITEMS} items"),
                        );
                    }
                    for (i, item) in items.iter().enumerate() {
                        settle_item_issues(&format!("/items/{i}"), item, &mut issues);
                    }
                }
                Some(_) => issues.push("/items", "type", "must be array"),
            }
        }
        APPLY_DERIVED => {
            params_closed("", given, &["fields"], &mut issues);
            match given.get("fields") {
                None => issues.push("", "required", "must have required property 'fields'"),
                Some(Value::Object(fields)) => {
                    if fields.is_empty() {
                        issues.push(
                            "/fields",
                            "minProperties",
                            "must NOT have fewer than 1 properties",
                        );
                    }
                    for (name, entry) in fields {
                        apply_entry_issues(&schema::member("/fields", name), entry, &mut issues);
                    }
                }
                Some(_) => issues.push("/fields", "type", "must be object"),
            }
        }
        REDERIVE => {
            params_closed("", given, &["fields"], &mut issues);
            if let Some(fields) = given.get("fields") {
                list_issues("/fields", fields, None, &mut issues);
            }
        }
        _ => {}
    }
    issues
}

fn settle_item_issues(at: &str, item: &Value, issues: &mut Issues) {
    let Some(members) = item.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    params_closed(
        at,
        members,
        &[
            "confidence",
            "failure",
            "field",
            "id",
            "sourceHash",
            "usage",
            "value",
        ],
        issues,
    );
    for key in ["id", "field", "sourceHash"] {
        match members.get(key) {
            None => issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            ),
            Some(value) => schema::text_issues(&schema::member(at, key), value, issues),
        }
    }
    if let Some(confidence) = members.get("confidence") {
        confidence_issues(&schema::member(at, "confidence"), confidence, issues);
    }
    if let Some(usage) = members.get("usage") {
        usage_issues(&schema::member(at, "usage"), usage, issues);
    }
    if let Some(failure) = members.get("failure") {
        let at = schema::member(at, "failure");
        let Some(failure) = failure.as_object() else {
            issues.push(at, "type", "must be object");
            return;
        };
        params_closed(&at, failure, &["class", "message"], issues);
        if !failure.contains_key("message") {
            issues.push(&at, "required", "must have required property 'message'");
        }
        for (key, max) in [("class", MAX_CLASS), ("message", MAX_MESSAGE)] {
            if let Some(value) = failure.get(key) {
                bounded_text(&schema::member(&at, key), value, 0, max, issues);
            }
        }
    }
}

fn apply_entry_issues(at: &str, entry: &Value, issues: &mut Issues) {
    let Some(members) = entry.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    // An entry that counts an attempt is the use's own, which its guard
    // holds to its caller.
    if let Some(attempt) = members.get(ATTEMPT) {
        params_closed(at, members, &[ATTEMPT, "sourceHash"], issues);
        if !attempt.is_object() {
            issues.push(schema::member(at, ATTEMPT), "type", "must be object");
        }
        return;
    }
    params_closed(
        at,
        members,
        &["confidence", "sourceHash", "usage", "value"],
        issues,
    );
    for key in ["sourceHash", "value"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(hash) = members.get("sourceHash") {
        schema::text_issues(&schema::member(at, "sourceHash"), hash, issues);
    }
    if let Some(confidence) = members.get("confidence") {
        confidence_issues(&schema::member(at, "confidence"), confidence, issues);
    }
    if members.get("usage").is_some_and(|usage| !usage.is_object()) {
        issues.push(schema::member(at, "usage"), "type", "must be object");
    }
}

fn confidence_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(number) = value.as_f64().filter(|_| value.is_number()) else {
        issues.push(at, "type", "must be number");
        return;
    };
    if number < 0.0 {
        issues.push(at, "minimum", "must be >= 0");
    }
    if number > 1.0 {
        issues.push(at, "maximum", "must be <= 1");
    }
}

fn usage_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(usage) = value.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    params_closed(
        at,
        usage,
        &["costMicros", "inputTokens", "modelId", "outputTokens"],
        issues,
    );
    for key in ["inputTokens", "outputTokens", "costMicros"] {
        if let Some(count) = usage.get(key) {
            schema::integer_issues(&schema::member(at, key), count, Some(0), None, issues);
        }
    }
    if let Some(model) = usage.get("modelId") {
        bounded_text(
            &schema::member(at, "modelId"),
            model,
            0,
            MAX_MODEL_ID,
            issues,
        );
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
            output: json!({"description": "A Category value", "enum": ["billing", "bug", "other", null], "type": ["string", "null"]}),
            type_name: "Category".to_owned(),
        }
    }

    fn own(value: Value) -> Map<String, Value> {
        value.as_object().cloned().unwrap_or_default()
    }

    #[test]
    fn a_hash_is_the_engines_over_the_model_the_instruction_the_output_and_the_inputs_that_hold_a_value(
    ) {
        let both = source_hash(
            &field(),
            &own(json!({"subject": "Card declined", "body": "Refused at checkout."})),
        );
        // core-derived.json's first pull presents this hash.
        assert_eq!(
            both.as_deref(),
            Some("72c4932fe182030ceb732c6ca4718bfaa443317c233d1d4a01602f949a066861")
        );
        assert_eq!(
            source_hash(&field(), &own(json!({"subject": "Card", "body": null}))),
            source_hash(&field(), &own(json!({"subject": "Card"})))
        );
        assert_eq!(source_hash(&field(), &own(json!({"title": "x"}))), None);
    }

    #[test]
    fn a_field_is_wanted_with_inputs_no_override_a_stale_or_forced_value_and_attempts_left() {
        let held =
            |source: Option<&str>, settled: Option<&str>, over: bool, forced: bool, n| Derivation {
                source: source.map(str::to_owned),
                settled: settled.map(str::to_owned),
                overridden_by: over.then(|| "alice".to_owned()),
                overridden_at: over.then_some(1),
                attempts: n,
                forced,
            };
        assert!(held(Some("a"), None, false, false, 0).wanted(3));
        assert!(!held(Some("a"), Some("a"), false, false, 0).wanted(3));
        assert!(held(Some("a"), Some("a"), false, true, 0).wanted(3));
        assert!(!held(Some("a"), Some("b"), true, false, 0).wanted(3));
        assert!(!held(Some("a"), Some("b"), false, false, 3).wanted(3));
        assert!(!held(None, None, false, true, 0).wanted(3));
        assert!(held(Some("a"), Some("b"), false, false, 3).failed(3));
    }

    #[test]
    fn a_value_is_held_to_its_fields_enum_and_type_in_the_validators_words() {
        assert!(value_issues(&field(), &json!("bug")).is_empty());
        assert!(value_issues(&field(), &Value::Null).is_empty());
        assert_eq!(
            value_issues(&field(), &json!("payments")),
            [ValidationIssue::new(
                "category",
                "enum",
                "Category must be one of the allowed values."
            )]
        );
        let mut score = field();
        score.output = json!({"type": "integer"});
        score.type_name = "Generic.Int64".to_owned();
        assert_eq!(
            value_issues(&score, &json!("high")),
            [ValidationIssue::new(
                "category",
                "type",
                "expected an integer"
            )]
        );
        assert_eq!(
            value_issues(&score, &Value::Null),
            [ValidationIssue::new(
                "category",
                "required",
                "Generic.Int64 is required."
            )]
        );
        assert!(value_issues(&score, &json!(3)).is_empty());
    }

    #[test]
    fn stringify_writes_the_shared_cases_as_javascript_does() {
        // The cases every Rust copy of JSON.stringify shares, recorded from
        // Node.js (runtime/schema/testdata/js_stringify.json).
        let corpus: Value = serde_json::from_str(include_str!(
            "../../../../schema/testdata/js_stringify.json"
        ))
        .expect("the cases are JSON");
        let cases = corpus["cases"].as_array().expect("cases");
        assert!(!cases.is_empty());
        for case in cases {
            let input = case["input"].as_str().expect("an input");
            let value: Value = serde_json::from_str(input).expect("an input is JSON");
            assert_eq!(Value::String(stringify(&value)), case["output"], "{input}");
        }
    }

    #[test]
    fn stringify_writes_as_javascript_does() {
        assert_eq!(
            stringify(&json!({"b": 1.0, "2": [true, null, 0.5], "a": "x\n"})),
            "{\"2\":[true,null,0.5],\"b\":1,\"a\":\"x\\n\"}"
        );
    }
}
