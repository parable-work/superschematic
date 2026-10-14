//! Retries, logical retries per failure class (D16), the port of
//! `typescript/src/retries.ts`, apart from a lease's expiries: a worker
//! that ran and failed records the attempt, and the instance's caps decide
//! whether it may run again. Each attempt is a call of `recordAttempt`: a
//! success, with no failure, or a failure of one of the config's classes.
//! A class has its own cap of failures and an optional hint, which
//! recordAttempt returns for a failure of the class to steer the worker's
//! next attempt, or is terminal, and every class counts against
//! `totalAttempts`. The instance's `limitsField` may narrow or widen the
//! caps for it; an unknown or terminal class, and a value that is not a
//! valid cap, is ignored. Once the instance exists, limitsField changes
//! only with `limitsPermission`, and never by the holder of its active
//! lease, read through Lease's holder and active fields: a worker does not
//! raise its own caps. An attempt's detail, any JSON object, is kept in its
//! operation's event, the one place it is recorded.
//!
//! The rules for a failure, in order:
//!
//! - a terminal class counts the failure and exhausts the instance;
//! - a failure of a class with no room left, its own cap or the total, is
//!   not counted and exhausts the instance;
//! - otherwise it counts, and exhausts the instance when the total reaches
//!   its cap, or when its class reaches its cap and no other class has
//!   room.
//!
//! Then the result. A success's result is kept. A failure's is kept only
//! with `keepBest`: when it has a score above the best kept score, by at
//! least `minDelta`, and no `neverRegress` predicate that held for the last
//! kept attempt fails for it (a predicate it does not report fails); the
//! first scored failure is kept when no score is. A kept result is written
//! to `resultField` through `update()`, with the checks of an update: a
//! result the live version refuses fails the attempt.
//!
//! Then stuck detection, with `stuckAfter`: a failure that is not kept and
//! carries a signature extends the streak when the signature is the last
//! attempt's, and starts a new one otherwise; a streak of stuckAfter
//! exhausts the instance as stuck. A kept attempt, a success and a failure
//! without a signature end the streak.
//!
//! On exhaustion the status moves to `exhaustedState` through Workflow's
//! transition, as the caller, only from the config's from states (every
//! state but the terminal ones and exhaustedState when it names none). A
//! transition a guard vetoes leaves the status as it is and the instance
//! exhausted all the same. Once exhausted, the guard refuses every
//! transition but into exhaustedState, Lease's acquire and Queue's claim,
//! and recordAttempt refuses further attempts. Nothing resets it.
//!
//! The counts and the attempt history are Retries' own storage: columns on
//! the instance for the total, the flags, the streak, the last signature
//! and the best score, and tables for the counts by class and the
//! attempts, whose last kept one the keep decision reads. A list filters on
//! exhausted (`where: { "Retries.exhausted": true }`) through an index on
//! its column. Every statement is the same on SQLite and Postgres (D58).
//!
//! Every refusal is a veto with a code the declaration lists (`exhausted`,
//! `limits_fixed`, `not_configured`), but a permission the caller lacks,
//! which is forbidden.
//!
//! configChange: any config may change. Retries can be added to a schema
//! that has instances, which start with no attempts, and cannot be removed
//! from one: the attempts its instances recorded would stay behind.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, InstanceView, OperationCx, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, js_number, number_value, stringify};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, Result, SchemaIssue,
};
use superschematic_engine_behaviors::is_terminal_state;

use crate::moves::{self, StatusMove};

const NAME: &str = "Retries";

/// A failure class: a cap of failures with an optional hint for the next
/// attempt, or terminal.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RetryClass {
    Capped { attempts: i64, hint: Option<String> },
    Terminal,
}

/// keepBest, parsed: minDelta filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct RetriesKeepBest {
    pub min_delta: f64,
    pub never_regress: Vec<String>,
}

/// Retries' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct RetriesConfig {
    /// The classes, by name, in the config's order.
    pub classes: Vec<(String, RetryClass)>,
    pub total_attempts: i64,
    pub limits_field: Option<String>,
    pub keep_best: Option<RetriesKeepBest>,
    pub stuck_after: Option<i64>,
    pub result_field: Option<String>,
    pub exhausted_state: String,
    /// The states exhaustion moves the status from.
    pub from: Vec<String>,
    pub permission: Option<String>,
    /// The permission that changes limitsField once the instance exists.
    pub limits_permission: Option<String>,
    /// Whether the type composes Lease, whose holder does not change
    /// limitsField.
    pub leased: bool,
}

impl RetriesConfig {
    /// A class by name.
    pub fn class(&self, name: &str) -> Option<&RetryClass> {
        self.classes
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, class)| class)
    }
}

/// The operations an exhausted instance refuses besides a transition:
/// taking it again.
const TAKES: [(&str, &str); 2] = [("Lease", "acquire"), ("Queue", "claim")];

/// The instance's own columns.
struct State {
    total: i64,
    exhausted: bool,
    stuck: bool,
    streak: i64,
    last_signature: Option<String>,
    best_score: Option<f64>,
    attempts: i64,
}

fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn state_of(view: &InstanceView<'_, RetriesConfig>) -> Result<State> {
    let columns = view.columns().get()?;
    Ok(State {
        total: columns.i64("total")?,
        exhausted: columns.i64("exhausted")? == 1,
        stuck: columns.i64("stuck")? == 1,
        streak: columns.i64("streak")?,
        last_signature: columns.opt_text("last_signature")?.map(str::to_owned),
        best_score: columns.opt_f64("best_score")?,
        attempts: columns.i64("attempts")?,
    })
}

// counts reads the failures counted by class, for every class of the
// config, in its order.
fn counts(view: &InstanceView<'_, RetriesConfig>) -> Result<Vec<(String, i64)>> {
    let mut out: Vec<(String, i64)> = view
        .config()
        .classes
        .iter()
        .map(|(name, _)| (name.clone(), 0))
        .collect();
    let rows = view.sql().all(
        format!(
            "SELECT class, attempts FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
            view.sql().table("classes")?
        ),
        &key(view),
    )?;
    for row in &rows {
        let class = row.text("class")?;
        if let Some(entry) = out.iter_mut().find(|(name, _)| name == class) {
            entry.1 = row.i64("attempts")?;
        }
    }
    Ok(out)
}

fn counted_of(counted: &[(String, i64)], name: &str) -> i64 {
    counted
        .iter()
        .find(|(known, _)| known == name)
        .map_or(0, |(_, count)| *count)
}

fn counts_json(counted: &[(String, i64)]) -> Value {
    Value::Object(
        counted
            .iter()
            .map(|(name, count)| (name.clone(), Value::from(*count)))
            .collect(),
    )
}

/// The caps an instance's attempts count against: the config's, narrowed
/// or widened by the instance's limitsField.
struct Caps<'c> {
    config: &'c RetriesConfig,
    limits: Option<Map<String, Value>>,
    total: i64,
}

// cap reads a cap of at least `least`: a safe integer; `None` for any
// other value.
fn cap(value: Option<&Value>, least: i64) -> Option<i64> {
    let Some(Value::Number(number)) = value else {
        return None;
    };
    let value = number_value(number);
    (value.fract() == 0.0 && value.abs() <= 9_007_199_254_740_991.0 && value >= least as f64)
        .then_some(value as i64)
}

impl Caps<'_> {
    // of is a non-terminal class's cap.
    fn of(&self, failure: &str) -> i64 {
        let configured = match self.config.class(failure) {
            Some(RetryClass::Capped { attempts, .. }) => *attempts,
            _ => 0,
        };
        cap(
            self.limits.as_ref().and_then(|limits| limits.get(failure)),
            0,
        )
        .unwrap_or(configured)
    }
}

// caps_of reads the caps. An unknown or terminal class, and a value that
// is not a valid cap, is ignored.
fn caps_of<'c>(view: &InstanceView<'c, RetriesConfig>) -> Result<Caps<'c>> {
    let config = view.config();
    let limits = match &config.limits_field {
        Some(field) => match view.data()?.get(field) {
            Some(Value::Object(limits)) => Some(limits.clone()),
            _ => None,
        },
        None => None,
    };
    let total = cap(
        limits
            .as_ref()
            .and_then(|limits| limits.get("totalAttempts")),
        1,
    )
    .unwrap_or(config.total_attempts);
    Ok(Caps {
        config,
        limits,
        total,
    })
}

// room reports whether a class other than failure may still fail once
// more.
fn room(config: &RetriesConfig, caps: &Caps<'_>, counted: &[(String, i64)], failure: &str) -> bool {
    config.classes.iter().any(|(name, class)| {
        name != failure
            && *class != RetryClass::Terminal
            && counted_of(counted, name) < caps.of(name)
    })
}

// last_kept reads the predicates of the last kept attempt, which a kept
// result never loses one of.
fn last_kept(view: &InstanceView<'_, RetriesConfig>) -> Result<Option<Map<String, Value>>> {
    let row = view.sql().get(
        format!(
            "SELECT predicates FROM {} WHERE namespace = ? AND schema = ? AND id = ? AND kept = 1 ORDER BY attempt DESC LIMIT 1",
            view.sql().table("attempts")?
        ),
        &key(view),
    )?;
    let Some(text) = row
        .as_ref()
        .map(|row| row.opt_text("predicates"))
        .transpose()?
        .flatten()
    else {
        return Ok(None);
    };
    match serde_json::from_str(text) {
        Ok(Value::Object(predicates)) => Ok(Some(predicates)),
        _ => Err(Error::behavior(
            NAME,
            "an attempt's predicates are no JSON object",
        )),
    }
}

// keeps decides whether a failed attempt's result is kept: only with
// keepBest, and only one that beats the best kept score by minDelta and
// loses no neverRegress predicate.
fn keeps(
    view: &InstanceView<'_, RetriesConfig>,
    state: &State,
    score: Option<f64>,
    predicates: Option<&Map<String, Value>>,
) -> Result<bool> {
    let (Some(keep_best), Some(score)) = (&view.config().keep_best, score) else {
        return Ok(false);
    };
    if let Some(best) = state.best_score {
        if !(score > best && score - best >= keep_best.min_delta) {
            return Ok(false);
        }
    }
    if !keep_best.never_regress.is_empty() {
        if let Some(baseline) = last_kept(view)? {
            let held = |predicates: Option<&Map<String, Value>>, name: &str| {
                predicates.and_then(|predicates| predicates.get(name)) == Some(&Value::Bool(true))
            };
            if keep_best
                .never_regress
                .iter()
                .any(|name| held(Some(&baseline), name) && !held(predicates, name))
            {
                return Ok(false);
            }
        }
    }
    Ok(true)
}

fn exhausted_veto(reason: String) -> Veto {
    Veto {
        reason,
        code: Some("exhausted".to_owned()),
        details: None,
    }
}

// limits_guard holds a change of limitsField to limitsPermission, and
// keeps it from the holder of the instance's active lease, read through
// Lease's holder and active fields. An update that leaves the field as it
// was passes.
fn limits_guard(
    view: &InstanceView<'_, RetriesConfig>,
    after: &Map<String, Value>,
) -> Result<Option<Veto>> {
    let config = view.config();
    let Some(field) = &config.limits_field else {
        return Ok(None);
    };
    let data = view.data()?;
    let text = |value: Option<&Value>| stringify(value.unwrap_or(&Value::Null));
    if text(data.get(field)) == text(after.get(field)) {
        return Ok(None);
    }
    let Some(permission) = &config.limits_permission else {
        return Ok(Some(Veto {
            reason: format!("{field} holds the instance's caps, which change only with limitsPermission, and the config names none"),
            code: Some("not_configured".to_owned()),
            details: None,
        }));
    };
    if !view.can(permission)? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not change {field}, the caps of {} {}: it needs permission {permission}",
                view.principal().subject,
                view.schema(),
                view.id()
            ),
        )
        .into());
    }
    if config.leased {
        let lease = view.instances().get(
            view.schema(),
            view.id(),
            Some(&["Lease.holder", "Lease.active"]),
        )?;
        let field_of = |name: &str| {
            lease
                .as_ref()
                .and_then(|record| record.behavior_field("Lease", name))
                .cloned()
        };
        if field_of("active") == Some(Value::Bool(true))
            && field_of("holder").as_ref().and_then(Value::as_str)
                == Some(view.principal().subject.as_str())
        {
            return Ok(Some(Veto {
                reason: format!("{field} holds the caps its holder's attempts count against, so the holder of its lease does not change it"),
                code: Some("limits_fixed".to_owned()),
                details: None,
            }));
        }
    }
    Ok(None)
}

/// Retries: logical retries per failure class on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Retries;

impl Behavior for Retries {
    type Config = RetriesConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Retries.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its fields and its Workflow's states.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<RetriesConfig> {
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        let field_of = |at: &str, field: &str| -> Result<Vec<Value>> {
            if !target.fields().iter().any(|known| known == field) {
                return Err(Error::config(format!(
                    "{at} \"{field}\" is not a field of {} (its fields: {})",
                    target.type_name(),
                    target.fields().join(", ")
                )));
            }
            Ok(moves::field_types(target, field))
        };
        let limits_field = text("limitsField");
        if let Some(field) = &limits_field {
            if !field_of("limitsField", field)?
                .iter()
                .any(|kind| kind.as_str() == Some("object"))
            {
                return Err(Error::config(format!(
                    "limitsField \"{field}\" is not an object field of {}",
                    target.type_name()
                )));
            }
        }
        let result_field = text("resultField");
        if let Some(field) = &result_field {
            field_of("resultField", field)?;
        }
        let exhausted_state = text("exhaustedState").unwrap_or_default();
        let mut from: Vec<String> = Vec::new();
        // A Workflow config of the wrong shape is Workflow's to refuse.
        if let (Some(flow), Some(raw_flow)) =
            (moves::flow_of(target), target.configs().get("Workflow"))
        {
            let states = flow.states.join(", ");
            let to = exhausted_state.as_str();
            if !flow.states.iter().any(|state| state == to) {
                return Err(Error::config(format!(
                    "exhaustedState \"{to}\" is not a state of the type's Workflow ({states})"
                )));
            }
            match raw.get("from") {
                None => {
                    from = flow
                        .states
                        .iter()
                        .filter(|state| *state != to && !is_terminal_state(raw_flow, state))
                        .cloned()
                        .collect();
                }
                Some(given) => {
                    let given = moves::strings(Some(given));
                    for state in &given {
                        if !flow.states.contains(state) {
                            return Err(Error::config(format!(
                                "from names \"{state}\", which is not a state of the type's Workflow ({states})"
                            )));
                        }
                        if state == to {
                            return Err(Error::config(format!(
                                "from names exhaustedState \"{to}\", which exhaustion cannot move the status from"
                            )));
                        }
                        if !flow
                            .transitions
                            .iter()
                            .any(|(source, target)| source == state && target == to)
                        {
                            return Err(Error::config(format!(
                                "from: no transition of the type's Workflow leads from \"{state}\" to \"{to}\""
                            )));
                        }
                    }
                    from = given;
                }
            }
        }
        let empty = Map::new();
        let raw_classes = raw
            .get("classes")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let classes = js_keys(raw_classes)
            .into_iter()
            .map(|name| {
                let class = match &raw_classes[name] {
                    Value::String(terminal) if terminal == "terminal" => RetryClass::Terminal,
                    spec => RetryClass::Capped {
                        attempts: match spec.get("attempts") {
                            Some(Value::Number(attempts)) => number_value(attempts) as i64,
                            _ => 0,
                        },
                        hint: spec.get("hint").and_then(Value::as_str).map(str::to_owned),
                    },
                };
                (name.to_owned(), class)
            })
            .collect();
        let integer = |name: &str| match raw.get(name) {
            Some(Value::Number(number)) => Some(number_value(number) as i64),
            _ => None,
        };
        Ok(RetriesConfig {
            classes,
            total_attempts: integer("totalAttempts").unwrap_or_default(),
            limits_field,
            keep_best: raw.get("keepBest").map(|keep| RetriesKeepBest {
                min_delta: match keep.get("minDelta") {
                    Some(Value::Number(delta)) => number_value(delta),
                    _ => 0.0,
                },
                never_regress: moves::strings(keep.get("neverRegress")),
            }),
            stuck_after: integer("stuckAfter"),
            result_field,
            exhausted_state,
            from,
            permission: text("permission"),
            limits_permission: text("limitsPermission"),
            leased: target.behaviors().iter().any(|name| name == "Lease"),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none())
                    .then(|| "the attempts its instances recorded would stay behind".to_owned()))
            }),
            guidance: Some(|_, config, target| crate::guidance::retries::guidance(config, target)),
            migrations: migrations(),
            filters: vec![(
                "exhausted",
                BehaviorFilter::new("exhausted", "boolean")
                    .described("Whether its retries are exhausted."),
            )],
            guard: Some(guard),
            operations: vec![("recordAttempt", record_attempt)],
            // Each field reads Retries' columns, but classAttempts, its
            // table of counts by class; no kept score leaves bestScore
            // absent.
            fields: vec![
                ("total", |_, view| {
                    Ok(Some(Value::from(state_of(view)?.total)))
                }),
                ("classAttempts", |_, view| {
                    Ok(Some(counts_json(&counts(view)?)))
                }),
                ("bestScore", |_, view| {
                    Ok(state_of(view)?.best_score.map(js_value))
                }),
                ("exhausted", |_, view| {
                    Ok(Some(Value::Bool(state_of(view)?.exhausted)))
                }),
                ("stuck", |_, view| {
                    Ok(Some(Value::Bool(state_of(view)?.stuck)))
                }),
            ],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    for table in ["classes", "attempts"] {
                        cx.sql().run(
                            format!(
                                "DELETE FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
                                cx.sql().table(table)?
                            ),
                            &key(cx),
                        )?;
                    }
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// js_value is a double as JavaScript writes it.
fn js_value(value: f64) -> Value {
    serde_json::from_str(&js_number(value)).unwrap_or(Value::Null)
}

fn migrations() -> Vec<BehaviorMigration> {
    let text = || ColumnSpec::text().not_null();
    let integer = || ColumnSpec::integer().not_null();
    vec![
        BehaviorMigration::new(1, "retries")
            .column("total", integer().default(0))
            .column("exhausted", integer().default(0))
            .column("stuck", integer().default(0))
            .column("streak", integer().default(0))
            .column("last_signature", ColumnSpec::text())
            .column("best_score", ColumnSpec::real())
            .column("attempts", integer().default(0))
            .table(
                TableSpec::new("classes")
                    .column("namespace", text())
                    .column("schema", text())
                    .column("id", text())
                    .column("class", text())
                    .column("attempts", integer())
                    .primary_key(&["namespace", "schema", "id", "class"]),
            )
            .table(
                TableSpec::new("attempts")
                    .column("namespace", text())
                    .column("schema", text())
                    .column("id", text())
                    .column("attempt", integer())
                    .column("failure", ColumnSpec::text())
                    .column("score", ColumnSpec::real())
                    .column("kept", integer())
                    .column("signature", ColumnSpec::text())
                    .column("predicates", ColumnSpec::text())
                    .column("created_by", text())
                    .column("created_at", integer())
                    .primary_key(&["namespace", "schema", "id", "attempt"]),
            ),
        // The index lets a list that filters on Retries.exhausted read only
        // the exhausted instances, or the others, in creation order.
        BehaviorMigration::new(2, "exhausted index").index("exhausted", &["exhausted"]),
    ]
}

// guard: once exhausted, the instance moves only to exhaustedState and is
// not taken again; limitsField changes only as a limit does. A create sets
// the caps it is created with, which no permission guards.
fn guard(
    _: &Retries,
    view: &InstanceView<'_, RetriesConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    match request {
        GuardRequest::Update { after, .. } => limits_guard(view, after),
        GuardRequest::Operation {
            behavior,
            operation,
            params,
            ..
        } => {
            let transition = (*behavior, *operation) == ("Workflow", "transition");
            if !transition && !TAKES.contains(&(*behavior, *operation)) {
                return Ok(None);
            }
            if !state_of(view)?.exhausted {
                return Ok(None);
            }
            let to = &view.config().exhausted_state;
            if transition {
                return Ok(
                    (params.get("to").and_then(Value::as_str) != Some(to.as_str())).then(|| {
                        exhausted_veto(format!(
                            "its retries are exhausted, so its status moves only to {to}"
                        ))
                    }),
                );
            }
            Ok(Some(exhausted_veto(
                "its retries are exhausted, so it is not taken again".to_owned(),
            )))
        }
        GuardRequest::Create { .. } | GuardRequest::Delete { .. } => Ok(None),
    }
}

fn params_refusal(path: &str, message: String) -> Error {
    EngineError::operation_params(NAME, "recordAttempt", vec![SchemaIssue::new(path, message)])
        .into()
}

fn record_attempt(
    _: &Retries,
    cx: &OperationCx<'_, RetriesConfig>,
    params: &Value,
) -> Result<Value> {
    let config = cx.config();
    if let Some(permission) = &config.permission {
        if !cx.can(permission)? {
            return Err(EngineError::new(
                EngineErrorCode::Forbidden,
                format!(
                    "{} may not record an attempt of {} {}: it needs permission {permission}",
                    cx.principal().subject,
                    cx.schema(),
                    cx.id()
                ),
            )
            .into());
        }
    }
    let state = state_of(cx)?;
    if state.exhausted {
        return Err(Error::veto(
            NAME,
            "recordAttempt",
            cx.schema(),
            cx.id(),
            exhausted_veto(format!(
                "its retries are exhausted{}",
                if state.stuck {
                    ", stuck on one failure"
                } else {
                    ""
                }
            )),
        ));
    }
    let failure = params.get("failure").and_then(Value::as_str);
    let score_param = params.get("score").filter(|score| score.is_number());
    let score = score_param.and_then(|score| match score {
        Value::Number(score) => Some(number_value(score)),
        _ => None,
    });
    let signature = params.get("signature").and_then(Value::as_str);
    let predicates = params.get("predicates").and_then(Value::as_object);
    let result = params.get("result");
    if result.is_some() && config.result_field.is_none() {
        return Err(params_refusal(
            "/result",
            "the config names no resultField to keep a result in".to_owned(),
        ));
    }
    if let Some(failure) = failure {
        if config.class(failure).is_none() {
            let names: Vec<&str> = config
                .classes
                .iter()
                .map(|(name, _)| name.as_str())
                .collect();
            return Err(params_refusal(
                "/failure",
                format!(
                    "{failure} is not a failure class of {} (its classes: {})",
                    cx.schema(),
                    names.join(", ")
                ),
            ));
        }
    }

    let mut counted = counts(cx)?;
    let mut total = state.total;
    let mut exhausted = false;
    if let Some(failure) = failure {
        let caps = caps_of(cx)?;
        let bump = |counted: &mut Vec<(String, i64)>| {
            if let Some(entry) = counted.iter_mut().find(|(name, _)| name == failure) {
                entry.1 += 1;
            }
        };
        if config.class(failure) == Some(&RetryClass::Terminal) {
            if total < caps.total {
                total += 1;
                bump(&mut counted);
            }
            exhausted = true;
        } else if total >= caps.total || counted_of(&counted, failure) >= caps.of(failure) {
            exhausted = true;
        } else {
            total += 1;
            bump(&mut counted);
            exhausted = total >= caps.total
                || (counted_of(&counted, failure) >= caps.of(failure)
                    && !room(config, &caps, &counted, failure));
        }
    }

    let kept = failure.is_none() || keeps(cx, &state, score, predicates)?;
    let mut streak = 0;
    let mut stuck = false;
    if let (Some(_), false, Some(signature)) = (failure, kept, signature) {
        streak = if Some(signature) == state.last_signature.as_deref() {
            state.streak + 1
        } else {
            1
        };
        stuck = config.stuck_after.is_some_and(|after| streak >= after);
    }
    exhausted |= stuck;

    if let (true, Some(result), Some(field)) = (kept, result, &config.result_field) {
        let mut patch = Map::new();
        patch.insert(field.clone(), result.clone());
        cx.update(&Value::Object(patch))?;
    }
    let attempt = state.attempts + 1;
    let best_score = match (kept, score) {
        (true, Some(score)) => Some(score),
        _ => state.best_score,
    };
    cx.columns().set(&[
        ("total", SqlValue::Integer(total)),
        ("exhausted", SqlValue::Integer(i64::from(exhausted))),
        ("stuck", SqlValue::Integer(i64::from(stuck))),
        ("streak", SqlValue::Integer(streak)),
        (
            "last_signature",
            match failure {
                None => SqlValue::Null,
                Some(_) => signature.map_or(SqlValue::Null, SqlValue::from),
            },
        ),
        (
            "best_score",
            best_score.map_or(SqlValue::Null, SqlValue::Real),
        ),
        ("attempts", SqlValue::Integer(attempt)),
    ])?;
    // A failure that counted moved the total and its class's count
    // together.
    if let Some(failure) = failure.filter(|_| total != state.total) {
        let mut values = key(cx);
        values.extend([
            SqlValue::from(failure),
            SqlValue::Integer(counted_of(&counted, failure)),
        ]);
        cx.sql().run(
            format!(
                "INSERT INTO {} (namespace, schema, id, class, attempts) VALUES (?, ?, ?, ?, ?)
                 ON CONFLICT (namespace, schema, id, class) DO UPDATE SET attempts = excluded.attempts",
                cx.sql().table("classes")?
            ),
            &values,
        )?;
    }
    let mut values = key(cx);
    values.extend([
        SqlValue::Integer(attempt),
        failure.map_or(SqlValue::Null, SqlValue::from),
        score.map_or(SqlValue::Null, SqlValue::Real),
        SqlValue::Integer(i64::from(kept)),
        signature.map_or(SqlValue::Null, SqlValue::from),
        predicates.map_or(SqlValue::Null, |predicates| {
            SqlValue::from(stringify(&Value::Object(predicates.clone())))
        }),
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::Integer(cx.now()),
    ]);
    cx.sql().run(
        format!(
            "INSERT INTO {} (namespace, schema, id, attempt, failure, score, kept, signature, predicates, created_by, created_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            cx.sql().table("attempts")?
        ),
        &values,
    )?;
    if exhausted {
        let rule = StatusMove {
            transition: config.exhausted_state.clone(),
            from: config.from.clone(),
        };
        let status = moves::status_of(cx)?;
        moves::apply(cx, Some(&rule), status.as_deref())?;
    }
    let hint = match failure.and_then(|failure| config.class(failure)) {
        Some(RetryClass::Capped {
            hint: Some(hint), ..
        }) => Value::from(hint.as_str()),
        _ => Value::Null,
    };
    Ok(json!({
        "failure": failure,
        "score": score_param.cloned().unwrap_or(Value::Null),
        "kept": kept,
        "total": total,
        "classAttempts": counts_json(&counted),
        "exhausted": exhausted,
        "stuck": stuck,
        "hint": hint,
    }))
}
