//! Queue, claimable work (D16; D16, amended: behaviors that serve claimable
//! work), the port of `typescript/src/queue.ts`. `claim` takes an instance
//! for its caller in one transaction: it checks that the instance can be
//! claimed, takes its lease through Lease's `acquire`, reserves its budget
//! through Budget's `reserve` when the type composes Budget, and moves its
//! status to the claimed state through Workflow's `transition`. A refusal
//! at any step leaves none of them done. `claimNext`, schema-level, claims
//! the first instance the caller can claim.
//!
//! claim's checks are the real ones, read from the instance as it is: its
//! status is one of `claim.from`, when the type composes Dependencies no
//! blocker holds it up, and no link `excludeStale` names is pinned to a
//! revision or a release its target has moved past. A lapsed lease is
//! expired first, through Lease's `expire`. Lease's acquire refuses an
//! active lease and an instance at maxExpiries, and Assignment's guard an
//! assigned instance's claim by anyone but its assignee. A direct acquire
//! on a type that composes Queue is refused (`claim_required`).
//!
//! claimNext reads candidates from Queue's own columns across the schema
//! (`sql().instances()`), through their index: copies of the facts a claim
//! checks, which Queue keeps current in the transaction of every change of
//! the instance (after_change) and of every change of an instance it waits
//! on that can move them (after_reference_change, which invokes
//! `refresh`): the status, Dependencies' blocked, Assignment's assignee,
//! Lease's expiries, the priority field's value, and until when the
//! instance is excluded: until a change while Retries shows it exhausted
//! or a link excludeStale names is stale, and, when Budget's checkReserve
//! says the reservation a claim makes does not fit, until the next UTC day
//! when that day alone makes it fit, else until a change does. A candidate
//! is in a claimable state, not blocked, at fewer than maxExpiries
//! expiries, not excluded now, unassigned or assigned to the caller (only
//! assigned to the caller with assignedOnly), and holds each match value or
//! one of a match list; candidates go highest priority first, an instance
//! without one last, then oldest, then by id. claimNext invokes claim on
//! each in turn, as the caller, until one succeeds, taking a veto, a
//! conflict or a forbidden to mean that one cannot be claimed by this
//! caller now, up to maxCandidates; when every one it tried was forbidden,
//! it returns the first refusal. A stale copy costs a skipped candidate,
//! never a wrong claim. countClaimable counts the same candidates.
//!
//! Queue records references to what can move the copies, each hearing only
//! what can: to each blocker, hearing its status; and, while the instance's
//! copies make it a candidate but for its exclusion, to each value of an
//! enclosing scope checkReserve says its answer turns on, and to the target
//! of each link excludeStale names, hearing its revision, or its release,
//! cross the one past the pinned one. A blocker's status change invokes
//! refresh on the dependent as the principal who made it; a scope's or a
//! target's change invokes it only when the copy, or what it hears, no
//! longer matches. A change the instance's own write makes is left to that
//! write's after_change.
//!
//! Storage (D58): six columns on the instances and their indexes. The
//! candidates' order and their match filters differ by database: Postgres
//! orders ids in byte order (`COLLATE "C"`), and reads a match field from
//! the row's JSON text with jsonb operators where SQLite has
//! `json_extract`, comparing a string with a string and a number or a
//! boolean (as 1 or 0) with a number, as `json_extract` gives them.
//!
//! configChange: claim, priorityField, match, maxCandidates and
//! excludeStale may change. Queue goes on a schema before it has
//! instances, and is not removed from one.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, Hears, InstanceCx, InstanceView, Reference, SchemaCx,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, js_number, number_value};
use superschematic_engine::storage::{sql_string, Dialect, Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, InstanceRecord, Result,
};
use superschematic_engine_behaviors::{link_many, link_pin};

/// The most instances one claimNext tries when the config gives no
/// maxCandidates.
pub const DEFAULT_MAX_CANDIDATES: i64 = 100;

/// excluded_until of an instance only a change lets back in:
/// `Number.MAX_SAFE_INTEGER`.
pub const EXCLUDED: i64 = 9_007_199_254_740_991;

const NAME: &str = "Queue";

// Dependencies' largest page of listBlockers.
const PAGE: i64 = 500;

// The key of a reference to a value of an enclosing budget scope is this,
// the value's path and the number it crosses; of one to a stale link's
// target, STALE and the link's name; a blocker's is "".
const SCOPE: &str = "budget";
const STALE: &str = "stale";

// What a reference to a blocker hears: its status, which alone moves
// whether it is open.
const STATUS: &str = "/behaviors/Workflow/status";

// The fields of other behaviors Queue reads of its own instance, by
// qualified name.
const WORKFLOW_STATUS: &str = "Workflow.status";
const LEASE_HOLDER: &str = "Lease.holder";
const LEASE_ACTIVE: &str = "Lease.active";
const LEASE_EXPIRIES: &str = "Lease.expiries";
const ASSIGNEE: &str = "Assignment.assignee";
const EXHAUSTED: &str = "Retries.exhausted";
const BLOCKED: &str = "Dependencies.blocked";
const LINKS_TARGETS: &str = "Links.targets";

/// The Workflow states an instance is claimed from, and the state a claim
/// moves it to.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QueueClaim {
    pub from: Vec<String>,
    pub to: String,
}

/// Queue's config, parsed: the defaults filled in, and what it reads of the
/// type's other behaviors.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QueueConfig {
    pub claim: QueueClaim,
    pub priority_field: Option<String>,
    /// The fields claimNext and countClaimable may match on (`match`).
    pub match_fields: Vec<String>,
    pub max_candidates: i64,
    /// The pinned links of the type's Links config whose staleness keeps an
    /// instance out.
    pub exclude_stale: Vec<String>,
    /// Whether the type composes Dependencies, whose blocked field a claim
    /// waits on.
    pub dependencies: bool,
    /// Whether the type composes Budget, whose reserve a claim calls and
    /// whose checkReserve says whether it fits.
    pub budget: bool,
    /// Whether the type composes Retries, whose exhausted instance a claim
    /// is refused.
    pub retries: bool,
    /// Whether the type composes Assignment, whose assignee assignedOnly
    /// reads.
    pub assignment: bool,
    /// The type's Lease maxExpiries: an instance with that many is no
    /// candidate.
    pub max_expiries: Option<i64>,
}

/// A reference Queue holds, or wants to, to hear a value move.
#[derive(Clone, Debug, PartialEq)]
struct Hearing {
    schema: String,
    id: String,
    key: String,
    hears: Hears,
}

impl Hearing {
    fn ref_key(&self) -> String {
        ref_key(&self.schema, &self.id, &self.key)
    }
}

/// Until when the copies keep an instance out, and the values whose moves
/// can let it in or keep it out.
struct Exclusion {
    until: i64,
    hearing: Vec<Hearing>,
}

/// The facts of one instance claimNext filters and orders on: Queue's own
/// columns, in the order they are written.
struct Facts {
    status: Option<String>,
    blocked: i64,
    assignee: Option<String>,
    expiries: i64,
    priority: Option<i64>,
    excluded_until: i64,
}

impl Facts {
    fn columns(&self) -> [(&'static str, SqlValue); 6] {
        [
            ("status", SqlValue::from(self.status.clone())),
            ("blocked", SqlValue::from(self.blocked)),
            ("assignee", SqlValue::from(self.assignee.clone())),
            ("expiries", SqlValue::from(self.expiries)),
            ("priority", SqlValue::from(self.priority)),
            ("excluded_until", SqlValue::from(self.excluded_until)),
        ]
    }
}

fn ref_key(schema: &str, id: &str, key: &str) -> String {
    format!("{schema}\u{0}{id}\u{0}{key}")
}

fn path_hears(path: &str, crosses: Option<f64>) -> Hears {
    Hears::Path {
        path: path.to_owned(),
        crosses,
    }
}

// hears_key is what a reference hears, as text the comparisons sort.
fn hears_key(hears: Option<&Hears>) -> String {
    match hears {
        None => "null".to_owned(),
        Some(Hears::Delete) => "delete".to_owned(),
        Some(Hears::Path { path, crosses }) => match crosses {
            Some(crosses) => format!("{path}\u{0}{}", js_number(*crosses)),
            None => path.clone(),
        },
    }
}

fn vetoed<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    reason: String,
    code: &str,
    details: Option<Value>,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: match details {
                Some(Value::Object(details)) => Some(details),
                _ => None,
            },
        },
    )
}

fn field<'r>(record: Option<&'r InstanceRecord>, behavior: &str, name: &str) -> Option<&'r Value> {
    record?.behavior_field(behavior, name)
}

fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    if let Some(exact) = number.as_i64() {
        return Some(exact);
    }
    let value = number_value(number);
    (value.is_finite() && value.fract() == 0.0).then_some(value as i64)
}

// safe_integer is a value that is a safe integer (Number.isSafeInteger).
fn safe_integer(value: Option<&Value>) -> Option<i64> {
    let number = value?.as_number()?;
    let value = number_value(number);
    (value.fract() == 0.0 && value.abs() <= EXCLUDED as f64).then_some(value as i64)
}

// blockers_of reads the instance's blockers through Dependencies'
// listBlockers, a page at a time, keeps a reference to each that exists,
// hearing its status, drops the references to the rest, and reports
// whether one is open.
fn blockers_of(cx: &InstanceCx<'_, QueueConfig>, changes: &mut bool) -> Result<bool> {
    let mut found: Vec<(String, String, String)> = Vec::new();
    let mut open = false;
    let mut cursor: Option<String> = None;
    loop {
        let mut params = json!({ "limit": PAGE });
        if let Some(cursor) = &cursor {
            params["cursor"] = Value::from(cursor.as_str());
        }
        let page = cx.call("Dependencies", "listBlockers", &params)?;
        for blocker in page["items"].as_array().into_iter().flatten() {
            open |= blocker["open"] == Value::Bool(true);
            // A blocker without a status is one whose delete is removing its
            // edge.
            let (Some(schema), Some(id)) = (blocker["schema"].as_str(), blocker["id"].as_str())
            else {
                continue;
            };
            if blocker.get("status").is_some() {
                let key = ref_key(schema, id, "");
                if !found.iter().any(|(known, _, _)| *known == key) {
                    found.push((key, schema.to_owned(), id.to_owned()));
                }
            }
        }
        match page["next"].as_str() {
            Some(next) => cursor = Some(next.to_owned()),
            None => break,
        }
    }
    let status = path_hears(STATUS, None);
    let mut held: Vec<String> = Vec::new();
    for reference in cx.references().list()? {
        if !reference.key.is_empty() {
            continue;
        }
        let key = ref_key(&reference.schema, &reference.id, &reference.key);
        if !found.iter().any(|(known, _, _)| *known == key) {
            cx.references()
                .remove(&reference.schema, &reference.id, &reference.key)?;
            *changes = true;
        } else if reference.hears.as_ref() == Some(&status) {
            held.push(key);
        }
    }
    for (key, schema, id) in &found {
        if !held.contains(key) {
            cx.references().add(schema, id, "", Some(status.clone()))?;
            *changes = true;
        }
    }
    Ok(open)
}

// exclusion_of is until when the instance is out of the candidates, for
// one whose status and blockers make it one, and the values whose moves
// can change that: until a change while Retries shows it exhausted, or
// while a link excludeStale names is stale, with nothing to hear; else
// each such link's target's revision or release, which crosses the one
// past the pinned one when the target moves on; and while Budget's
// checkReserve says a claim's reservation does not fit, until the day that
// alone makes it fit, else until a change does, hearing each value of a
// scope the answer says it turns on. record is the instance as a read
// returns it, with its Retries exhausted and Links targets fields; check
// runs checkReserve on the instance.
fn exclusion_of(
    config: &QueueConfig,
    record: Option<&InstanceRecord>,
    check: &dyn Fn() -> Result<Value>,
) -> Result<Exclusion> {
    if config.retries && field(record, "Retries", "exhausted") == Some(&Value::Bool(true)) {
        return Ok(Exclusion {
            until: EXCLUDED,
            hearing: Vec::new(),
        });
    }
    let mut hearing = Vec::new();
    let links = field(record, "Links", "targets").and_then(Value::as_object);
    for name in &config.exclude_stale {
        // Links gives stale while the target has a revision or a release to
        // compare: a link without it pins nothing, or points at a target
        // being deleted. A list, which parse_config refuses, has none.
        let Some(link) = links
            .and_then(|links| links.get(name))
            .and_then(Value::as_object)
        else {
            continue;
        };
        let pin = if link.get("revision").is_some_and(Value::is_number) {
            Some(("revision", "/behaviors/Revisions/revision"))
        } else if link.get("release").is_some_and(Value::is_number) {
            Some(("release", "/behaviors/Branches/release"))
        } else {
            None
        };
        let (Some((kind, path)), Some(stale), Some(schema), Some(id)) = (
            pin,
            link.get("stale").and_then(Value::as_bool),
            link.get("schema").and_then(Value::as_str),
            link.get("id").and_then(Value::as_str),
        ) else {
            continue;
        };
        if stale {
            return Ok(Exclusion {
                until: EXCLUDED,
                hearing: Vec::new(),
            });
        }
        let pinned = link
            .get(kind)
            .and_then(Value::as_number)
            .map_or(f64::NAN, number_value);
        hearing.push(Hearing {
            schema: schema.to_owned(),
            id: id.to_owned(),
            key: format!("{STALE} {name}"),
            hears: path_hears(path, Some(pinned + 1.0)),
        });
    }
    if !config.budget {
        return Ok(Exclusion { until: 0, hearing });
    }
    let answer = check()?;
    for scope in answer["scopes"].as_array().into_iter().flatten() {
        let (Some(schema), Some(id)) = (scope["schema"].as_str(), scope["id"].as_str()) else {
            continue;
        };
        for value in scope["hears"].as_array().into_iter().flatten() {
            let Some(path) = value["path"].as_str() else {
                continue;
            };
            let crosses = value
                .get("crosses")
                .and_then(Value::as_number)
                .map(number_value);
            let key = match crosses {
                None => format!("{SCOPE} {path}"),
                Some(crosses) => format!("{SCOPE} {path} {}", js_number(crosses)),
            };
            hearing.push(Hearing {
                schema: schema.to_owned(),
                id: id.to_owned(),
                key,
                hears: path_hears(path, crosses),
            });
        }
    }
    let until = if answer["fits"] == Value::Bool(true) {
        0
    } else {
        answer["until"].as_i64().unwrap_or(EXCLUDED)
    };
    Ok(Exclusion { until, hearing })
}

// fields_read is the fields of its own instance Queue reads, beyond its
// own columns, by qualified name.
fn fields_read(config: &QueueConfig) -> Vec<&'static str> {
    let mut fields = vec![WORKFLOW_STATUS, LEASE_EXPIRIES, ASSIGNEE, EXHAUSTED];
    if !config.exclude_stale.is_empty() {
        fields.push(LINKS_TARGETS);
    }
    fields
}

// facts_of reads the facts a claim checks from the instance as it is now,
// read as the caller, and the values whose moves can change its exclusion.
fn facts_of(cx: &InstanceCx<'_, QueueConfig>, changes: &mut bool) -> Result<(Facts, Vec<Hearing>)> {
    let config = cx.config();
    let fields = fields_read(config);
    let record = cx.instances().get(cx.schema(), cx.id(), Some(&fields))?;
    let data = cx.data()?;
    let priority = config
        .priority_field
        .as_ref()
        .and_then(|field| safe_integer(data.get(field)));
    let status = field(record.as_ref(), "Workflow", "status")
        .and_then(Value::as_str)
        .map(str::to_owned);
    let blocked = i64::from(config.dependencies && blockers_of(cx, changes)?);
    // An instance no claim can take for its status or a blocker is no
    // candidate whatever else holds it: nothing to check, and nothing to
    // hear.
    let claimable = status
        .as_ref()
        .is_some_and(|status| config.claim.from.contains(status));
    let exclusion = if claimable && blocked == 0 {
        exclusion_of(config, record.as_ref(), &|| {
            cx.call("Budget", "checkReserve", &json!({}))
        })?
    } else {
        Exclusion {
            until: 0,
            hearing: Vec::new(),
        }
    };
    let assignee = field(record.as_ref(), "Assignment", "assignee")
        .and_then(Value::as_str)
        .map(str::to_owned);
    let expiries = field(record.as_ref(), "Lease", "expiries")
        .and_then(integer)
        .unwrap_or(0);
    Ok((
        Facts {
            status,
            blocked,
            assignee,
            expiries,
            priority,
            excluded_until: exclusion.until,
        },
        exclusion.hearing,
    ))
}

// heard lists the references the instance holds for its exclusion: all but
// its blockers'.
fn heard(references: Vec<Reference>) -> Vec<Reference> {
    references
        .into_iter()
        .filter(|reference| !reference.key.is_empty())
        .collect()
}

// hear keeps the references the exclusion wants, each hearing what it
// says, and no other but the blockers'.
fn hear(cx: &InstanceCx<'_, QueueConfig>, hearing: &[Hearing], changes: &mut bool) -> Result<()> {
    // The last of a key wins, at the first one's place.
    let mut wanted: Vec<Hearing> = Vec::new();
    for one in hearing {
        match wanted
            .iter_mut()
            .find(|known| known.ref_key() == one.ref_key())
        {
            Some(known) => *known = one.clone(),
            None => wanted.push(one.clone()),
        }
    }
    let mut held: Vec<String> = Vec::new();
    for reference in heard(cx.references().list()?) {
        let key = ref_key(&reference.schema, &reference.id, &reference.key);
        match wanted.iter().find(|one| one.ref_key() == key) {
            None => {
                cx.references()
                    .remove(&reference.schema, &reference.id, &reference.key)?;
                *changes = true;
            }
            Some(want) => {
                if reference.hears.as_ref() == Some(&want.hears) {
                    held.push(key);
                }
            }
        }
    }
    for one in &wanted {
        if !held.contains(&one.ref_key()) {
            cx.references()
                .add(&one.schema, &one.id, &one.key, Some(one.hears.clone()))?;
            *changes = true;
        }
    }
    Ok(())
}

// same_column compares a column as the database returns it with the value
// a fact gives it.
fn same_column(stored: Option<&SqlValue>, value: &SqlValue) -> bool {
    let text = |value: &SqlValue| match value {
        SqlValue::Integer(n) => n.to_string(),
        SqlValue::Real(x) => js_number(*x),
        SqlValue::Text(text) => text.clone(),
        other => other.to_string(),
    };
    match stored {
        None | Some(SqlValue::Null) => value.is_null(),
        Some(stored) => !value.is_null() && text(stored) == text(value),
    }
}

// refresh brings Queue's columns, and what it hears, in line with the
// instance, and returns whether it changed any: it writes only a column
// that is not as it should be, so a refresh that finds its copies right
// writes nothing.
fn refresh(cx: &InstanceCx<'_, QueueConfig>) -> Result<bool> {
    let mut changes = false;
    let (facts, hearing) = facts_of(cx, &mut changes)?;
    let columns: Row = cx.columns().get()?;
    let moved: Vec<(&str, SqlValue)> = facts
        .columns()
        .into_iter()
        .filter(|(name, value)| !same_column(columns.get(name), value))
        .collect();
    if !moved.is_empty() {
        cx.columns().set(&moved)?;
        changes = true;
    }
    hear(cx, &hearing, &mut changes)?;
    Ok(changes)
}

// outdated reports whether a change of a value the instance hears leaves
// its exclusion, or what it hears, other than its copies say: read through
// checkReserve, invoked on the instance as the principal who made the
// change, and the Retries exhausted and Links targets fields.
fn outdated(cx: &InstanceView<'_, QueueConfig>) -> Result<bool> {
    let config = cx.config();
    let columns = cx.columns().get()?;
    let status = columns.get("status").and_then(SqlValue::as_str);
    let blocked = columns.get("blocked").and_then(SqlValue::as_i64);
    if !status.is_some_and(|status| config.claim.from.iter().any(|from| from == status))
        || blocked != Some(0)
    {
        // No candidate hears a scope or a link's target: refresh lets go of
        // it.
        return Ok(true);
    }
    let record = if config.retries || !config.exclude_stale.is_empty() {
        cx.instances()
            .get(cx.schema(), cx.id(), Some(&[EXHAUSTED, LINKS_TARGETS]))?
    } else {
        None
    };
    let exclusion = exclusion_of(config, record.as_ref(), &|| {
        cx.instances()
            .invoke(cx.schema(), cx.id(), "checkReserve", &json!({}), None)
    })?;
    let mut now: Vec<String> = heard(cx.references().list()?)
        .iter()
        .map(|reference| {
            format!(
                "{}\u{0}{}",
                ref_key(&reference.schema, &reference.id, &reference.key),
                hears_key(reference.hears.as_ref())
            )
        })
        .collect();
    let mut then: Vec<String> = exclusion
        .hearing
        .iter()
        .map(|one| format!("{}\u{0}{}", one.ref_key(), hears_key(Some(&one.hears))))
        .collect();
    now.sort();
    then.sort();
    Ok(
        columns.get("excluded_until").and_then(SqlValue::as_i64) != Some(exclusion.until)
            || now != then,
    )
}

fn params_error(operation: &str, path: String, message: String) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

// match_values is a match entry's values, as json_extract compares them:
// a string as text, a number as itself, a boolean as 1 or 0.
fn match_values(value: &Value) -> Vec<SqlValue> {
    let listed: Vec<&Value> = match value {
        Value::Array(items) => items.iter().collect(),
        one => vec![one],
    };
    listed
        .into_iter()
        .map(|one| match one {
            Value::Bool(flag) => SqlValue::Integer(i64::from(*flag)),
            Value::String(text) => SqlValue::from(text.as_str()),
            Value::Number(number) => match number.as_i64() {
                Some(exact) => SqlValue::Integer(exact),
                None => SqlValue::Real(number_value(number)),
            },
            _ => SqlValue::Null,
        })
        .collect()
}

fn marks(count: usize) -> String {
    vec!["?"; count].join(", ")
}

// match_condition is the SQL that keeps the rows whose own field holds one
// of the values, as SQLite's json_extract compares them. On Postgres a
// string value matches a JSON string, and a number value a JSON number or
// a boolean as 1 or 0; a value of another kind matches nothing.
fn match_condition(
    dialect: Dialect,
    field: &str,
    values: Vec<SqlValue>,
    params: &mut Vec<SqlValue>,
) -> String {
    match dialect {
        Dialect::Sqlite => {
            params.push(SqlValue::from(format!("$.\"{field}\"")));
            let condition = format!("json_extract(data, ?) IN ({})", marks(values.len()));
            params.extend(values);
            condition
        }
        Dialect::Postgres => {
            let key = sql_string(field);
            let value = format!("((data::jsonb)->{key})");
            let (texts, numbers): (Vec<SqlValue>, Vec<SqlValue>) = values
                .into_iter()
                .filter(|value| !value.is_null())
                .partition(|value| matches!(value, SqlValue::Text(_)));
            let mut alternatives = Vec::new();
            if !texts.is_empty() {
                alternatives.push(format!(
                    "(jsonb_typeof({value}) = 'string' AND ((data::jsonb)->>{key}) IN ({}))",
                    marks(texts.len())
                ));
                params.extend(texts);
            }
            if !numbers.is_empty() {
                alternatives.push(format!(
                    "(CASE jsonb_typeof({value}) WHEN 'number' THEN ((data::jsonb)->>{key})::numeric WHEN 'boolean' THEN (CASE WHEN {value} = 'true'::jsonb THEN 1 ELSE 0 END) END) IN ({})",
                    numbers.iter().map(|_| "?::numeric").collect::<Vec<_>>().join(", ")
                ));
                params.extend(numbers);
            }
            if alternatives.is_empty() {
                "1 = 0".to_owned()
            } else {
                format!("({})", alternatives.join(" OR "))
            }
        }
    }
}

// eligible is the SQL condition of a candidate for the caller, and its
// values: the copies, and the match and assignedOnly of the call.
fn eligible(
    cx: &SchemaCx<'_, QueueConfig>,
    operation: &str,
    params: &Value,
) -> Result<(String, Vec<SqlValue>)> {
    let config = cx.config();
    let empty = Map::new();
    let matched = params
        .get("match")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    for field in js_keys(matched) {
        if !config.match_fields.iter().any(|known| known == field) {
            return Err(params_error(
                operation,
                format!("/match/{field}"),
                format!(
                    "{operation} on {} matches {}, not {field}",
                    cx.schema(),
                    if config.match_fields.is_empty() {
                        "no field".to_owned()
                    } else {
                        config.match_fields.join(", ")
                    }
                ),
            ));
        }
    }
    let assigned_only = params.get("assignedOnly") == Some(&Value::Bool(true));
    if assigned_only && !config.assignment {
        return Err(params_error(
            operation,
            "/assignedOnly".to_owned(),
            format!(
                "{} does not compose Assignment, so none of its instances is assigned",
                cx.schema()
            ),
        ));
    }
    let mut conditions = vec![
        format!("status IN ({})", marks(config.claim.from.len())),
        "blocked = 0".to_owned(),
        "excluded_until <= ?".to_owned(),
        if assigned_only {
            "assignee = ?".to_owned()
        } else {
            "(assignee IS NULL OR assignee = ?)".to_owned()
        },
    ];
    let mut values: Vec<SqlValue> = config
        .claim
        .from
        .iter()
        .map(|from| SqlValue::from(from.as_str()))
        .collect();
    values.push(SqlValue::from(cx.now()));
    values.push(SqlValue::from(cx.principal().subject.as_str()));
    if let Some(max) = config.max_expiries {
        conditions.push("expiries < ?".to_owned());
        values.push(SqlValue::from(max));
    }
    for field in js_keys(matched) {
        let condition = match_condition(
            cx.dialect(),
            field,
            match_values(&matched[field]),
            &mut values,
        );
        conditions.push(condition);
    }
    Ok((conditions.join(" AND "), values))
}

// The operations whose changes leave every fact Queue copies as it was:
// its own refresh, and the lease's control channel.
const UNCHANGING: [(&str, &str); 4] = [
    ("Queue", "refresh"),
    ("Lease", "heartbeat"),
    ("Lease", "direct"),
    ("Lease", "acknowledge"),
];

fn states_of(flow: &Value) -> Option<(Vec<&str>, &Vec<Value>)> {
    let states = flow.get("states")?.as_array()?;
    let transitions = flow.get("transitions")?.as_array()?;
    Some((
        states.iter().filter_map(Value::as_str).collect(),
        transitions,
    ))
}

// check_field holds a field the config names to one of the type's own
// fields whose JSON type is one of types (null aside).
fn check_field(
    target: &ConfigTarget<'_>,
    at: &str,
    field: &str,
    types: &[&str],
    what: &str,
) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "{at} names \"{field}\", which is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    let declared = target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("type"));
    let listed: Vec<&Value> = match declared {
        Some(Value::Array(types)) => types.iter().collect(),
        Some(one) => vec![one],
        None => vec![&Value::Null],
    };
    let listed: Vec<&Value> = listed
        .into_iter()
        .filter(|one| one.as_str() != Some("null"))
        .collect();
    if listed.is_empty()
        || listed
            .iter()
            .any(|one| !one.as_str().is_some_and(|one| types.contains(&one)))
    {
        return Err(Error::config(format!(
            "{at} names \"{field}\", which is not {what} field of {}",
            target.type_name()
        )));
    }
    Ok(())
}

// check_stale holds excludeStale to pinned links of the type's Links config
// that hold one target.
fn check_stale(target: &ConfigTarget<'_>, names: &[String]) -> Result<()> {
    if names.is_empty() {
        return Ok(());
    }
    if !target.behaviors().iter().any(|name| name == "Links") {
        return Err(Error::config(
            "excludeStale names links of Links, which the type does not list",
        ));
    }
    // A Links config of the wrong shape is Links' to refuse.
    let Some(links) = target
        .configs()
        .get("Links")
        .and_then(|config| config.get("links"))
        .and_then(Value::as_object)
    else {
        return Ok(());
    };
    for name in names {
        let Some(link) = links.get(name) else {
            return Err(Error::config(format!(
                "excludeStale names \"{name}\", which is not a link of the type's Links config (its links: {})",
                js_keys(links).join(", ")
            )));
        };
        if link_pin(link).is_none() {
            return Err(Error::config(format!(
                "excludeStale names \"{name}\", a link that is not pinned, so it is never stale"
            )));
        }
        if link_many(link) {
            return Err(Error::config(format!(
                "excludeStale names \"{name}\", a link that holds a list of targets; it reads links that hold one"
            )));
        }
    }
    Ok(())
}

// stale_of lists the links excludeStale names that are pinned to a
// revision or a release their targets have moved past, read through Links'
// targets field.
fn stale_of<'c>(config: &'c QueueConfig, links: Option<&Map<String, Value>>) -> Vec<&'c str> {
    config
        .exclude_stale
        .iter()
        .filter(|name| {
            links
                .and_then(|links| links.get(name.as_str()))
                .and_then(|link| link.get("stale"))
                == Some(&Value::Bool(true))
        })
        .map(String::as_str)
        .collect()
}

fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

// js_text is String(value): a string's text, undefined for none.
fn js_text(value: Option<&Value>) -> String {
    match value {
        None => "undefined".to_owned(),
        Some(Value::String(text)) => text.clone(),
        Some(other) => superschematic_engine::json::stringify(other),
    }
}

/// Queue: claimable work.
#[derive(Clone, Copy, Debug, Default)]
pub struct Queue;

impl Behavior for Queue {
    type Config = QueueConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Queue.behavior.json")
    }

    // The configSchema holds the shape; this holds the claim to the type's
    // Workflow, the field names to its fields and excludeStale to its
    // pinned links, and records what a claim needs to know of the type's
    // other behaviors.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<QueueConfig> {
        let claim = QueueClaim {
            from: strings(raw.get("claim").and_then(|claim| claim.get("from"))),
            to: raw
                .get("claim")
                .and_then(|claim| claim.get("to"))
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        };
        // A Workflow config its own checks refuse, or none, is reported
        // there.
        if let Some((states, transitions)) = target.configs().get("Workflow").and_then(states_of) {
            let listed = states.join(", ");
            if !states.contains(&claim.to.as_str()) {
                return Err(Error::config(format!(
                    "claim.to \"{}\" is not a state of the type's Workflow ({listed})",
                    claim.to
                )));
            }
            for from in &claim.from {
                if !states.contains(&from.as_str()) {
                    return Err(Error::config(format!(
                        "claim.from names \"{from}\", which is not a state of the type's Workflow ({listed})"
                    )));
                }
                if *from == claim.to {
                    return Err(Error::config(format!(
                        "claim.from names \"{from}\", the state a claim moves the instance to"
                    )));
                }
                let leads = transitions.iter().any(|transition| {
                    transition.get("from").and_then(Value::as_str) == Some(from.as_str())
                        && transition.get("to").and_then(Value::as_str) == Some(claim.to.as_str())
                });
                if !leads {
                    return Err(Error::config(format!(
                        "claim: no transition of the type's Workflow leads from \"{from}\" to \"{}\"",
                        claim.to
                    )));
                }
            }
        }
        let priority_field = raw
            .get("priorityField")
            .and_then(Value::as_str)
            .map(str::to_owned);
        if let Some(field) = &priority_field {
            check_field(target, "priorityField", field, &["integer"], "an integer")?;
        }
        let match_fields = strings(raw.get("match"));
        for field in &match_fields {
            check_field(
                target,
                "match",
                field,
                &["string", "number", "integer", "boolean"],
                "a scalar",
            )?;
        }
        let exclude_stale = strings(raw.get("excludeStale"));
        check_stale(target, &exclude_stale)?;
        let composes = |name: &str| target.behaviors().iter().any(|known| known == name);
        let max_expiries = target
            .configs()
            .get("Lease")
            .and_then(|lease| lease.get("maxExpiries"))
            .and_then(integer);
        Ok(QueueConfig {
            claim,
            priority_field,
            match_fields,
            max_candidates: raw
                .get("maxCandidates")
                .and_then(integer)
                .unwrap_or(DEFAULT_MAX_CANDIDATES),
            exclude_stale,
            dependencies: composes("Dependencies"),
            budget: composes("Budget"),
            retries: composes("Retries"),
            assignment: composes("Assignment"),
            max_expiries,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guidance: Some(|_, config, target| crate::guidance::queue::guidance(config, target)),
            config_change: Some(|_, before, after, _| {
                if before.is_none() {
                    return Ok(Some(
                        "the instances that exist have no claim facts for claimNext to find them by; compose Queue before a schema has instances"
                            .to_owned(),
                    ));
                }
                if after.is_none() {
                    return Ok(Some(
                        "the claim facts and blocker references its instances hold would stay behind"
                            .to_owned(),
                    ));
                }
                Ok(None)
            }),
            migrations: vec![
                BehaviorMigration::new(1, "queue")
                    .column("status", ColumnSpec::text())
                    .column("blocked", ColumnSpec::integer().not_null().default(0))
                    .column("assignee", ColumnSpec::text())
                    .column("expiries", ColumnSpec::integer().not_null().default(0))
                    .column("priority", ColumnSpec::integer())
                    .index("claimable", &["status", "blocked", "priority"]),
                // Until when the instance is out of the candidates: 0 for
                // not, EXCLUDED until a change; claimNext compares it with
                // the time.
                BehaviorMigration::new(2, "excluded")
                    .column(
                        "excluded_until",
                        ColumnSpec::integer().not_null().default(0),
                    )
                    .index("eligible", &["status", "blocked", "excluded_until"]),
            ],
            // The lease of a claimable instance is taken by claiming it.
            guard: Some(|_, _, request| {
                Ok(match request {
                    GuardRequest::Operation {
                        behavior: "Lease",
                        operation: "acquire",
                        caller,
                        ..
                    } if *caller != Some(NAME) => Some(Veto {
                        reason: format!(
                            "its lease is taken by claiming it ({NAME}'s claim), which checks that it can be claimed"
                        ),
                        code: Some("claim_required".to_owned()),
                        details: None,
                    }),
                    _ => None,
                })
            }),
            operations: vec![
                ("claim", claim),
                // A refresh that finds the copies right changes nothing, and
                // says so: a reference's hook asks for one on every move it
                // hears.
                ("refresh", |_, cx, _| {
                    if !refresh(cx)? {
                        cx.unchanged();
                    }
                    Ok(json!({}))
                }),
            ],
            schema_operations: vec![
                ("claimNext", claim_next),
                ("countClaimable", |_, cx, params| {
                    let (condition, values) = eligible(cx, "countClaimable", params)?;
                    let relation = cx.sql().instances()?;
                    let row = cx.sql().get(
                        format!("SELECT COUNT(*) AS count FROM {relation} WHERE {condition}"),
                        &values,
                    )?;
                    let count = match row {
                        Some(row) => row.i64("count")?,
                        None => 0,
                    };
                    Ok(json!({ "count": count }))
                }),
            ],
            after_change: Some(|_, cx, change| {
                let unchanging = match change {
                    InstanceChange::Delete => true,
                    InstanceChange::Operation {
                        behavior,
                        operation,
                        ..
                    } => UNCHANGING.contains(&(*behavior, *operation)),
                    _ => false,
                };
                if !unchanging {
                    refresh(cx)?;
                }
                Ok(())
            }),
            // A blocker's status moved, or it went: the instance's copies
            // follow, through its own refresh, as the principal who made the
            // change. A value its exclusion hears moved: it refreshes only
            // when its exclusion, or what it hears, moved with it. A change
            // the instance's own write made is that write's to settle.
            after_reference_change: Some(|_, cx, reference, change| {
                if cx.writing() {
                    return Ok(());
                }
                if !reference.key.is_empty()
                    && !matches!(change, InstanceChange::Delete)
                    && !outdated(cx)?
                {
                    return Ok(());
                }
                cx.instances()
                    .invoke(cx.schema(), cx.id(), "refresh", &json!({}), None)?;
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn claim(
    _: &Queue,
    cx: &superschematic_engine::OperationCx<'_, QueueConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    let lease = cx
        .instances()
        .get(cx.schema(), cx.id(), Some(&[LEASE_HOLDER, LEASE_ACTIVE]))?;
    if field(lease.as_ref(), "Lease", "holder").is_some()
        && field(lease.as_ref(), "Lease", "active") != Some(&Value::Bool(true))
    {
        cx.call("Lease", "expire", &json!({}))?;
    }
    let mut fields = vec![WORKFLOW_STATUS, BLOCKED];
    if !config.exclude_stale.is_empty() {
        fields.push(LINKS_TARGETS);
    }
    let now = cx.instances().get(cx.schema(), cx.id(), Some(&fields))?;
    let status = field(now.as_ref(), "Workflow", "status");
    let claimable = status
        .and_then(Value::as_str)
        .is_some_and(|status| config.claim.from.iter().any(|from| from == status));
    if !claimable {
        return Err(vetoed(
            cx,
            "claim",
            format!(
                "it is {}, and it is claimed from {}",
                js_text(status),
                config.claim.from.join(", ")
            ),
            "not_claimable",
            Some(json!({
                "status": status.and_then(Value::as_str),
                "from": config.claim.from,
            })),
        ));
    }
    if config.dependencies
        && field(now.as_ref(), "Dependencies", "blocked") == Some(&Value::Bool(true))
    {
        return Err(vetoed(
            cx,
            "claim",
            "a blocker holds it up".to_owned(),
            "blocked",
            None,
        ));
    }
    let links = field(now.as_ref(), "Links", "targets").and_then(Value::as_object);
    let stale = stale_of(config, links);
    if !stale.is_empty() {
        let mut pins: Vec<&str> = Vec::new();
        for name in &stale {
            let release = links
                .and_then(|links| links.get(*name))
                .and_then(|link| link.get("release"))
                .is_some_and(Value::is_number);
            let pin = if release { "release" } else { "revision" };
            if !pins.contains(&pin) {
                pins.push(pin);
            }
        }
        let reason = if stale.len() == 1 {
            format!(
                "its link {} is pinned to a {} its target has moved past",
                stale[0], pins[0]
            )
        } else {
            format!(
                "its links {} are pinned to {} their targets have moved past",
                stale.join(", "),
                pins.iter()
                    .map(|pin| format!("{pin}s"))
                    .collect::<Vec<_>>()
                    .join(" or ")
            )
        };
        return Err(vetoed(
            cx,
            "claim",
            reason,
            "stale_link",
            Some(json!({ "links": stale })),
        ));
    }
    let lease_params = match params.get("ttlMs") {
        Some(ttl) => json!({ "ttlMs": ttl }),
        None => json!({}),
    };
    let taken = cx.call("Lease", "acquire", &lease_params)?;
    // Budget's contract: reserve() with no meter reserves every meter's
    // claim amount, and a veto refuses a reservation that does not fit,
    // which refuses the claim and with it the lease. Budget settles its
    // reservations itself when the lease is released or expires.
    if config.budget {
        cx.call("Budget", "reserve", &json!({}))?;
    }
    cx.call("Workflow", "transition", &json!({ "to": config.claim.to }))?;
    Ok(json!({
        "id": cx.id(),
        "token": taken.get("token").cloned().unwrap_or(Value::Null),
        "expiresAt": taken.get("expiresAt").cloned().unwrap_or(Value::Null),
        "heartbeatMs": taken.get("heartbeatMs").cloned().unwrap_or(Value::Null),
    }))
}

fn claim_next(_: &Queue, cx: &SchemaCx<'_, QueueConfig>, params: &Value) -> Result<Value> {
    let (condition, mut values) = eligible(cx, "claimNext", params)?;
    let relation = cx.sql().instances()?;
    let select = format!("SELECT id FROM {relation} WHERE {condition}");
    values.push(SqlValue::from(cx.config().max_candidates));
    let candidates = cx.sql().all(
        Sql::per(
            format!("{select} ORDER BY priority IS NULL, priority DESC, created_at, id LIMIT ?"),
            format!(
                "{select} ORDER BY priority IS NULL, priority DESC, created_at, id COLLATE \"C\" LIMIT ?"
            ),
        ),
        &values,
    )?;
    let claim_params = match params.get("ttlMs") {
        Some(ttl) => json!({ "ttlMs": ttl }),
        None => json!({}),
    };
    let mut forbidden: Option<Error> = None;
    let mut other = false;
    for candidate in &candidates {
        let id = candidate.text("id")?;
        match cx
            .instances()
            .invoke(cx.schema(), id, "claim", &claim_params, None)
        {
            Ok(claimed) => return Ok(json!({ "claimed": claimed })),
            // A veto, a conflict or a refusal of this caller: this one cannot
            // be claimed by it now; the next may.
            Err(error) => match error.code() {
                Some(EngineErrorCode::Forbidden) => {
                    if forbidden.is_none() {
                        forbidden = Some(error);
                    }
                }
                Some(EngineErrorCode::Vetoed | EngineErrorCode::Conflict) => other = true,
                _ => return Err(error),
            },
        }
    }
    // The caller may claim none of them: say why, rather than that there is
    // no work.
    if let (Some(error), false) = (forbidden, other) {
        return Err(error);
    }
    Ok(json!({ "claimed": null }))
}
