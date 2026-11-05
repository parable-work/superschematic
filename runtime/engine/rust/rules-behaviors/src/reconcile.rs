//! Reconcile, the state a row's provider last reported (D104), beside the
//! Workflow field it moves: `observed: Reconcile<{ status, unreachable?,
//! staleAfterMs?, observePermission }>`. Like Seats, only the core
//! implements it: no engine registers it, and its declaration is the file
//! `make behaviors` writes for the package `@superschematic/rules`
//! (`rules-behaviors/declarations`), read in place.
//!
//! Reconcile is held to D103's test of a behavior and passes it. Its state
//! is its own: what the provider last reported and when (`observed`, the
//! field's value, and `observedAt`), why the status differs from it
//! (`drift`), when they last agreed (`reconciledAt`), and its stale
//! deadline. Its rules say which observations count, how the status
//! follows one along the transitions its Workflow allows with every guard
//! holding each step, what a refused step records, and when silence moves
//! the row to unreachable. It recurs: a deployment's status at its
//! platform, a device's at its fleet manager, a payment's at its processor,
//! a sandbox's at its provider. It is not Derived (D73): Derived's model
//! fills own fields and moves no status.
//!
//! `observe({ state, at })` records an observation: `at` is when the
//! provider was seen in `state`, in epoch milliseconds, as the core reads
//! a date-time. One no newer than `observedAt`, an equal one included,
//! answers `unchanged: true` and writes nothing, before any guard
//! ([`Behavior::settle`]); so does a repeat. A newer one sets `observed`
//! and `observedAt`, then moves the Workflow `status` names to the observed
//! state along the fewest transitions its config allows ([`crate::flow`]'s
//! path: breadth first, each state's transitions in the config's order),
//! each step a call of Workflow's `transition` made as the observer's own
//! request (`Cx::call_as_caller`), so every use's guard holds each step as
//! it holds the observer's own move: Workflow's `by`, permission and
//! `overridePermission`, StateRules' `missing_fields`, Approvals'
//! `approvals_missing`, Retries' `backing_off`, and Lease's
//! `held_by_another`, which holds a step as the observer's whatever its
//! `exempt` says of `observe`. A step refused as a veto or as `forbidden`
//! stops the path there and records `drift` (`{ status, observed, code }`,
//! the code the veto's, `forbidden`, `no_path` when no transitions lead to
//! the state, or `no_status` for a row with no status); any other refusal
//! fails the observe. When the status ends at the observed state, `drift`
//! clears and `reconciledAt` is the command's time; `drift` and
//! `reconciledAt` move through `observe` and the stale deadline alone, so a
//! move another caller makes to the observed state leaves drift until the
//! next observation. An `at` later than the command's time, and a state the
//! Workflow lacks, are bad parameters; observe needs `observePermission`,
//! checked before the repeat, so a caller who may not observe learns
//! nothing of the row from it.
//!
//! `staleAfterMs` arms the deadline `stale` at `observedAt + staleAfterMs`
//! from each recorded observation while the status it leaves is neither
//! terminal nor `unreachable`, and clears it otherwise. When it falls due
//! (the API's behavior worker runs it, D103), a row whose `observedAt` has
//! moved past it (a raw write, a longer `staleAfterMs`) arms it again; a
//! row whose status is terminal or unreachable is left as it is; any other
//! row gets `observed` set to `unreachable` (`observedAt` stays, so any
//! observation since is still newer) and moves there along the fewest
//! transitions, each a `Cx::call`, so the worker's request (`fired`), a
//! refused step recording drift. A fire is spent: a drifted stale move
//! waits for the next observation.
//!
//! The config is checked at define: first as the declaration's
//! configSchema holds it, in the schema loader's words
//! ([`ConfigError::Schema`]), then in Reconcile's own
//! ([`ConfigError::Rule`]): `status` names a Workflow of the type, in
//! [`crate::flow`]'s words; `unreachable` and `staleAfterMs` come together;
//! `unreachable` is a state of that Workflow, which every state but a
//! terminal one reaches and from which every state is reached, each read
//! along transitions, as the stale move and an observation each take the
//! fewest. The core runs no JSON Schema, so the schema's checks are written
//! here, held to the declaration file by this crate's tests.
//!
//! configChange: any change is allowed, and Reconcile joins and leaves a
//! type with rows: every owned field is optional, a row starts unobserved,
//! and a removed use loses a cache of what a provider says. A write around
//! the core (raw SQL of `observed_*` or of the status) runs none of these
//! rules and re-arms nothing until the deadline falls due.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Hook, Refusal,
    SchemaIssue, Stop, View,
};

use crate::declared;
use crate::flow::{self, flow_of};
use crate::schema::{self, Issues};
use crate::values::date_time_text;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Reconcile";

/// Reconcile's declaration, the file `make behaviors` writes for the core
/// alone (no engine implements it), read in place.
pub const DECLARATION: &str = include_str!("../declarations/Reconcile.behavior.json");

/// The value field: the state the last observation reported.
pub const OBSERVED: &str = "observed";

/// When the last observation was made, in epoch milliseconds.
pub const OBSERVED_AT: &str = "observedAt";

/// Why the status is not the observed state.
pub const DRIFT: &str = "drift";

/// When the status last agreed with the observed state.
pub const RECONCILED_AT: &str = "reconciledAt";

/// The operation that records an observation.
pub const OBSERVE: &str = "observe";

/// The deadline that moves a row no observation reached for staleAfterMs.
pub const STALE: &str = "stale";

// The members the configSchema's closed object and observe's closed
// paramsSchema take.
const CONFIG_KEYS: [&str; 4] = ["observePermission", "staleAfterMs", "status", "unreachable"];
const PARAMS_KEYS: [&str; 2] = ["at", "state"];

// The least staleAfterMs the configSchema takes.
const MIN_STALE_AFTER_MS: i64 = 1000;

/// Reconcile's config, parsed: the Workflow it moves, as the type holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReconcileConfig {
    /// The Workflow use observations move, by its id.
    pub status: String,
    pub unreachable: Option<String>,
    pub stale_after_ms: Option<i64>,
    pub observe_permission: String,
    /// That Workflow's states and transitions (`from`, `to`), in its
    /// config's order.
    pub states: Vec<String>,
    pub transitions: Vec<(String, String)>,
    /// The field that Workflow is anchored on; None for one anchored on the
    /// type: what a refusal names its states by (`Sandbox.status`).
    pub status_field: Option<String>,
}

impl ReconcileConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the Workflow it moves and the unreachable state against
    /// it, a rule's reason which the core prefixes with the type, the field
    /// and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ReconcileConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let text = |key: &str| raw.get(key).and_then(Value::as_str).map(str::to_owned);
        let status = text("status").unwrap_or_default();
        let flow = flow_of(target, Some(&status))?;
        let unreachable = text("unreachable");
        let stale_after_ms = raw.get("staleAfterMs").and_then(whole);
        match (&unreachable, stale_after_ms) {
            (Some(_), None) => {
                return Err("unreachable names the state the stale deadline moves a row to, and the config gives no staleAfterMs to arm it".into())
            }
            (None, Some(_)) => {
                return Err("staleAfterMs arms a deadline that moves a row to unreachable, and the config names no unreachable state".into())
            }
            _ => {}
        }
        if let Some(unreachable) = &unreachable {
            if flow.lacks(unreachable) {
                return Err(format!(
                    "unreachable \"{unreachable}\" is not a state of the type's Workflow ({})",
                    flow.listed()
                )
                .into());
            }
            let terminal = |state: &str| !flow.transitions.iter().any(|(from, _)| from == state);
            for state in &flow.states {
                if state == unreachable || terminal(state) {
                    continue;
                }
                if !flow::reachable(&flow.transitions, state).contains(unreachable) {
                    return Err(format!(
                        "unreachable \"{unreachable}\" cannot be reached from \"{state}\", which is not a terminal state: the stale deadline moves a row there from any state but a terminal one"
                    )
                    .into());
                }
            }
            let from = flow::reachable(&flow.transitions, unreachable);
            if let Some(state) = flow.states.iter().find(|state| !from.contains(state)) {
                return Err(format!(
                    "\"{state}\" cannot be reached from unreachable \"{unreachable}\": an observation moves a row from it to any state"
                )
                .into());
            }
        }
        let status_field = target
            .uses()
            .iter()
            .find(|entry| entry.behavior == workflow::NAME && entry.id() == flow.use_id)
            .and_then(|entry| entry.field())
            .map(str::to_owned);
        Ok(ReconcileConfig {
            status: flow.use_id,
            unreachable,
            stale_after_ms,
            observe_permission: text("observePermission").unwrap_or_default(),
            states: flow.states,
            transitions: flow.transitions,
            status_field,
        })
    }

    /// Whether a state is one of the Workflow's.
    pub fn has_state(&self, state: &str) -> bool {
        self.states.iter().any(|known| known == state)
    }

    /// Whether a state is terminal in the Workflow: one of its states that
    /// no transition leaves.
    pub fn is_terminal(&self, state: &str) -> bool {
        self.has_state(state) && !self.transitions.iter().any(|(from, _)| from == state)
    }
}

/// Reconcile: the state a row's provider last reported, which moves its
/// Workflow there along the transitions it allows.
#[derive(Clone, Copy, Debug, Default)]
pub struct Reconcile;

impl Behavior for Reconcile {
    type Config = ReconcileConfig;

    // The declaration file names the value field, observed (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ReconcileConfig, ConfigError> {
        ReconcileConfig::parse(raw, target)
    }

    // Any change, an added and a removed use included: every owned field is
    // optional, a row added to starts unobserved, and a removed use loses a
    // cache of what a provider says.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&ReconcileConfig>,
        _after: Option<&ReconcileConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &ReconcileConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != OBSERVE {
            return Vec::new();
        }
        params_issues(params).into_issues()
    }

    // settle answers observe before any use's guard is asked (D104): an at
    // after the command's time and a state the Workflow lacks are bad
    // parameters, together; a caller without observePermission is
    // forbidden, before the repeat, so it learns nothing of the row; and an
    // observation no newer than the last answers unchanged, writing nothing.
    fn settle(
        &self,
        view: &View<'_, ReconcileConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        if operation != OBSERVE {
            return Ok(None);
        }
        let config = view.config();
        let state = params
            .get("state")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let at = params.get("at").and_then(whole).unwrap_or_default();
        let mut issues = Vec::new();
        if at > view.now() {
            issues.push(SchemaIssue::new(
                "/at",
                format!(
                    "{} is after the command's time, {}: an observation reports a state already seen",
                    instant(at),
                    instant(view.now())
                ),
            ));
        }
        if !config.has_state(state) {
            let of = match &config.status_field {
                Some(field) => format!("{}.{field}", view.type_name()),
                None => view.type_name().to_owned(),
            };
            issues.push(SchemaIssue::new(
                "/state",
                format!(
                    "\"{state}\" is not a state of {of} ({})",
                    config.states.join(", ")
                ),
            ));
        }
        if !issues.is_empty() {
            return Err(
                Refusal::operation_params(view.behavior(), view.use_id(), OBSERVE, issues).into(),
            );
        }
        let permission = &config.observe_permission;
        if !view.can(permission)? {
            let who = match view.caller().subject.as_str() {
                "" => "a caller with no subject",
                subject => subject,
            };
            return Err(view.forbidden(format!(
                "{who} may not observe {}: observe needs permission {permission}",
                target(view)
            )));
        }
        let (Some(observed), Some(observed_at)) = (
            view.get(OBSERVED),
            view.get(OBSERVED_AT).as_ref().and_then(whole),
        ) else {
            return Ok(None);
        };
        if at > observed_at {
            return Ok(None);
        }
        let mut result = Map::new();
        result.insert("observed".into(), observed);
        result.insert("observedAt".into(), Value::from(observed_at));
        result.insert(
            "status".into(),
            status(view).map_or(Value::Null, Value::from),
        );
        result.insert("moves".into(), json!([]));
        if let Some(drift) = view.get(DRIFT) {
            result.insert("drift".into(), drift);
        }
        result.insert("unchanged".into(), Value::Bool(true));
        Ok(Some(Value::Object(result)))
    }

    fn operation(&self, cx: &Cx<'_, ReconcileConfig>, name: &str, params: &Value) -> Hook<Value> {
        if name != OBSERVE {
            return Err(cx.defect(format!("it declares no operation {name}")));
        }
        let config = cx.config();
        let state = params
            .get("state")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let at = params.get("at").and_then(whole).unwrap_or_default();
        cx.set(OBSERVED, state)?;
        cx.set(OBSERVED_AT, at)?;
        let (moves, after) = follow(cx, state, true)?;
        if let Some(stale_after_ms) = config.stale_after_ms {
            let settled = after.as_deref().is_some_and(|status| {
                config.is_terminal(status) || config.unreachable.as_deref() == Some(status)
            });
            if settled {
                cx.clear(STALE)?;
            } else {
                cx.arm(STALE, at.saturating_add(stale_after_ms), json!({}))?;
            }
        }
        let mut result = Map::new();
        result.insert("observed".into(), Value::from(state));
        result.insert("observedAt".into(), Value::from(at));
        result.insert(
            "status".into(),
            after.as_deref().map_or(Value::Null, Value::from),
        );
        result.insert("moves".into(), Value::Array(moves));
        if let Some(drift) = cx.get(DRIFT) {
            result.insert("drift".into(), drift);
        }
        Ok(Value::Object(result))
    }

    // The stale deadline: see the module's header.
    fn deadline(&self, cx: &Cx<'_, ReconcileConfig>, name: &str, _payload: &Value) -> Hook<()> {
        if name != STALE {
            return Err(cx.defect(format!(
                "deadline {name} fell due, and Reconcile arms only {STALE}"
            )));
        }
        let config = cx.config();
        let (Some(stale_after_ms), Some(unreachable)) =
            (config.stale_after_ms, config.unreachable.as_deref())
        else {
            return Ok(());
        };
        let Some(observed_at) = cx.get(OBSERVED_AT).as_ref().and_then(whole) else {
            return Ok(());
        };
        let due = observed_at.saturating_add(stale_after_ms);
        if due > cx.now() {
            return cx.arm(STALE, due, json!({}));
        }
        if status(cx).is_some_and(|status| config.is_terminal(&status) || status == unreachable) {
            return Ok(());
        }
        cx.set(OBSERVED, unreachable)?;
        follow(cx, unreachable, false)?;
        Ok(())
    }
}

// follow moves the Workflow from its status to a state along the fewest
// transitions, each step as the caller's own request (an observation) or as
// the hook's own call (a deadline's, the worker's), stopping at a step
// refused as a veto or as forbidden; then records drift, or clears it and
// sets reconciledAt when the status ends at the state. It answers the
// moves made, each `{ from, to }` as Workflow's transition answered it, and
// the status after them.
fn follow(
    cx: &Cx<'_, ReconcileConfig>,
    state: &str,
    as_caller: bool,
) -> Hook<(Vec<Value>, Option<String>)> {
    let config = cx.config();
    let mut moves = Vec::new();
    let mut stopped: Option<String> = None;
    match status(cx) {
        None => stopped = Some("no_status".to_owned()),
        Some(from) => match flow::path(&config.transitions, &from, state) {
            None => stopped = Some("no_path".to_owned()),
            Some(steps) => {
                for to in steps {
                    let params = json!({ "to": to });
                    let moved = if as_caller {
                        cx.call_as_caller(&config.status, workflow::TRANSITION, &params)
                    } else {
                        cx.call(&config.status, workflow::TRANSITION, &params)
                    };
                    match moved {
                        Ok(result) => moves.push(json!({
                            "from": result.get("from").cloned().unwrap_or(Value::Null),
                            "to": result.get("to").cloned().unwrap_or(Value::Null),
                        })),
                        Err(stop) => {
                            stopped = Some(refused_code(&stop).ok_or(stop)?);
                            break;
                        }
                    }
                }
            }
        },
    }
    let after = status(cx);
    if after.as_deref() == Some(state) {
        cx.set(DRIFT, Value::Null)?;
        cx.set(RECONCILED_AT, cx.now())?;
    } else {
        let mut drift = Map::new();
        if let Some(status) = &after {
            drift.insert("status".into(), Value::from(status.as_str()));
        }
        drift.insert("observed".into(), Value::from(state));
        drift.insert(
            "code".into(),
            Value::from(stopped.unwrap_or_else(|| "no_path".to_owned())),
        );
        cx.set(DRIFT, Value::Object(drift))?;
    }
    Ok((moves, after))
}

// refused_code is the drift code of a step's refusal that stops the path:
// a veto's code (`vetoed` for a veto with none), or `forbidden`; None for
// any other stop, which fails the command.
fn refused_code(stop: &Stop) -> Option<String> {
    let refusal = stop.refusal()?;
    match refusal.code {
        Code::Vetoed => Some(
            refusal
                .details
                .as_deref()
                .and_then(|details| details.get("vetoCode"))
                .and_then(Value::as_str)
                .unwrap_or("vetoed")
                .to_owned(),
        ),
        Code::Forbidden => Some("forbidden".to_owned()),
        _ => None,
    }
}

// status is the status of the Workflow the config moves, as the run has
// it; None when the row has none.
fn status(view: &View<'_, ReconcileConfig>) -> Option<String> {
    view.get_of(&view.config().status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// target names the row a refusal is about: its type and its key, and on a
// field anchor the field (`Sandbox s1's observed`).
fn target<C>(view: &View<'_, C>) -> String {
    let row = match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    };
    match view.field() {
        Some(field) => format!("{row}'s {field}"),
        None => row,
    }
}

// instant is a time in epoch milliseconds as ISO 8601 text, or its digits
// past the range toISOString writes.
fn instant(ms: i64) -> String {
    date_time_text(ms).unwrap_or_else(|| ms.to_string())
}

// whole reads a whole number a schema let through: 2 or 2.0.
fn whole(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    number.as_i64().or_else(|| {
        number
            .as_f64()
            .filter(|n| n.is_finite() && n.fract() == 0.0)
            .map(|n| n as i64)
    })
}

// ---- the configSchema and the paramsSchema, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    for key in ["status", "observePermission"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    for key in ["status", "unreachable", "observePermission"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    if let Some(stale_after_ms) = config.get("staleAfterMs") {
        schema::integer_issues(
            "/staleAfterMs",
            stale_after_ms,
            Some(MIN_STALE_AFTER_MS),
            None,
            &mut issues,
        );
    }
    issues
}

// params_issues lists what observe's parameters break of its closed
// paramsSchema, in the behavior registry's words.
fn params_issues(params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given
        .keys()
        .filter(|key| !PARAMS_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    for key in ["state", "at"] {
        if !given.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(state) = given.get("state") {
        schema::text_issues("/state", state, &mut issues);
    }
    if let Some(at) = given.get("at") {
        schema::integer_issues("/at", at, Some(0), None, &mut issues);
    }
    issues
}
