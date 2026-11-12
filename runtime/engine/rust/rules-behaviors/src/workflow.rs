//! Workflow, the core's state machine (D16), ported onto the pure core
//! (D103) from the engine's (`../behaviors/src/workflow.rs`): a status that
//! starts in the config's initial state and changes only through
//! `transition`, along a transition the config lists, which may name a
//! permission the caller must hold. A state that no transition leaves is
//! terminal.
//!
//! A terminal state has an outcome: success, failure or neutral. The
//! config's `outcomes` names it for the terminal states it lists, and every
//! other one is a success, so a config without outcomes means what it did
//! before outcomes existed. Workflow itself reads no outcome: Dependencies,
//! Rollups and Reactions read it from another type's config, through
//! [`state_outcome`], to tell a blocker or a child that finished well from
//! one that failed.
//!
//! A state a transition leaves may count as finished too (D113): the
//! config's `finished` names its outcome, success, failure or neutral, as a
//! task that is done and may be reopened still closes its parent.
//! [`state_outcome`] answers for it as for a terminal state, so what reads
//! an outcome counts it: Reactions' `allTerminal` and `anyTerminal`. It is
//! not terminal, since a transition leaves it: the guard's `terminal_state`
//! never names it, and what asks whether a transition leaves a state
//! (Lease's expiry count, Retries' default `from`, Reconcile's settled
//! rows) is answered as before. `finished` names only a state a transition
//! leaves, and `outcomes` only a terminal one, whose rule stays as it was.
//!
//! Workflow owns two fields: `status`, the state the row is in and the
//! value a field anchor's field is, and `enteredAt`, when the status last
//! changed, the create's time and then each transition's, in epoch
//! milliseconds (D56). A use anchored on the type holds them under the use
//! id `Workflow`; a use anchored on a field (`status: Workflow<{ ... }>`)
//! holds them under the field's name, and guards and moves only its own
//! status, so a type may carry several. Where they live is the host's
//! layout, by the rule [`Declaration`] states: on a build-time project's
//! table, a field anchor's are the field's column and one it prefixes
//! (`status`, `status_entered_at`), and a type anchor's are prefixed with
//! the behavior's storage key (`workflow_status`, `workflow_entered_at`), so
//! an own field named `status` sits beside them. A row written before
//! `enteredAt` was kept has none until its next transition.
//!
//! Nothing else moves the status. A create or an update writes own fields
//! only, and the core refuses one that names a field anchor's field, which
//! is the status; another use writes only the fields it declares (the core
//! holds it to its declaration), so it moves the status by calling
//! `transition`, whose parameter check, guard and permission gate run for a
//! call as for a caller. `transition`'s parameters are `to` and, optionally,
//! `from`, checked as its closed paramsSchema has them, so no alias reaches
//! the guard or the handler. On a field anchor, a refusal names the field
//! (`"packed" is not a state of Order.status`, `Order o1's fulfilment is
//! already packed`).
//!
//! A transition may name who makes it (D104): `by` lists fields of the row
//! that hold a subject (a `string`, `Identity.UUID` or `Identity.UserID`
//! field, the audit field `createdBy` among them), and only a caller who is
//! the subject one of them holds makes it, holding the transition's
//! permission too when it names one; any other caller is vetoed
//! (`not_principal`, details `{ from, to, by }`). A field with no value
//! names no one, and a UUID field names its subject in either form
//! ([`crate::subjects`]). A caller who holds the config's
//! `overridePermission` passes every transition's `by`, and still needs its
//! permission. `by` and `overridePermission` hold a `call()` and a
//! `call_on()` as the permission does: the command's caller is the one
//! checked. A `seat:<name>` entry names a seat of the type's Seats (D104): a
//! caller seated there, through an occupant row or the seat's fromField,
//! makes the transition, read through Seats' own rule
//! (`seats::holds_seat`). The field entries are read first, so a field that
//! names the caller ends the check before any seat's occupants are read; a
//! refusal names who the transition is made by, its fields and then its
//! seats (`the subject agent holds or a subject seated in accountable`),
//! and its details' `by` lists the entries as the config does. At define,
//! a seat entry names a seat of the type's Seats, and a type with none is
//! refused for it.
//!
//! A move to the state the row is in is the veto `already_in_state` (D16's
//! amendment), unless the config sets `repeat: "unchanged"`: it then
//! answers `{ from, to, unchanged: true }` and writes nothing, before any
//! use's guard is asked ([`Behavior::settle`]), so no other behavior's gate
//! refuses a move that is no move. A repeat checks no transition's `by` or
//! permission, since no transition leads from a state to itself.
//! `transition`'s `from` is the state the caller read: one that is not the
//! row's state is the veto `stale` (details `{ from, status }`), checked
//! first, before the repeat and `already_in_state`, so a retry that names
//! `from` and finds its move made is stale with `status` equal to its `to`:
//! the retry landed.
//!
//! A transition's change set is the row's update with the use's two owned
//! fields set, and its result is `{ from, to }`. Workflow emits no outbox
//! event of its own: the event a host records for the command, whose patch
//! carries the new status and when it was entered, is the row write's, as
//! D103's outbox trigger writes one per change, whoever makes it, and as
//! the engine wrote one per command.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then as a state
//! machine, in Workflow's own ([`ConfigError::Rule`]). The core runs no JSON
//! Schema, so the schema's checks are written here, held to the
//! declaration file by this module's tests. A use that gives no config is
//! checked as `{}`, whose shape the schema refuses, so the core refuses it
//! for the config it lacks. Workflow takes no create parameters: its
//! declaration has no createParamsSchema, and the core refuses an entry a
//! create gives it.
//!
//! configChange: while the type has rows, a new version keeps every state
//! of the old config, since a row may be in any of them; with none it may
//! drop any. A changed config that drops a state reads whether the type has
//! rows. Transitions, their permissions, the initial state, outcomes and
//! finished may change; outcomes and finished, like transitions, are read
//! when they are needed, so a new version's apply at the next read.
//! Workflow cannot be added to or removed from a type that has rows: they
//! would have no status, or lose it. The core asks about an added or a
//! removed use only when the type has rows.
//!
//! What the engine kept beside the behavior is the host's now: a row's
//! sequence, version and timestamps, the filter on the status and its
//! index (the layout's), and the guidance a describe gives, which the core
//! has no surface for.

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook, Refusal,
    Row, SchemaIssue, Stop, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::seats::{self, SeatRef};
use crate::subjects::{holds_subject, names_subject};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Workflow";

/// The owned field that holds the state a row is in.
pub const STATUS: &str = "status";

/// The owned field that holds when the status last changed, in epoch
/// milliseconds.
pub const ENTERED_AT: &str = "enteredAt";

/// The operation that moves the status.
pub const TRANSITION: &str = "transition";

/// Workflow's declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Workflow.behavior.json");

// What the configSchema holds a state's name to, in its states and in the
// keys of its outcomes and its finished: the rule every declared name
// follows.
#[cfg(test)]
const STATE_PATTERN: &str = schema::NAME_PATTERN;
#[cfg(test)]
const STATE_MAX_LENGTH: usize = schema::NAME_MAX_LENGTH;

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 7] = [
    "finished",
    "initial",
    "outcomes",
    "overridePermission",
    "repeat",
    "states",
    "transitions",
];
const TRANSITION_KEYS: [&str; 4] = ["by", "from", "permission", "to"];

// The members transition's closed paramsSchema takes.
const PARAMS_KEYS: [&str; 2] = ["from", "to"];

// The value of repeat the configSchema's enum takes.
const REPEAT_UNCHANGED: &str = "unchanged";

// The outcomes, in the order the configSchema's enum lists them.
const OUTCOMES: [&str; 3] = ["success", "failure", "neutral"];

/// One transition of a Workflow config.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkflowTransition {
    pub from: String,
    pub to: String,
    /// The permission a caller needs to make it; absent, anyone who may
    /// write the row can.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub permission: Option<String>,
    /// The own fields that hold a subject who may make it (D104); empty
    /// when it names none, and any caller may.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub by: Vec<String>,
}

/// What a transition to the state a row is in answers (D104).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum WorkflowRepeat {
    /// The veto already_in_state, the default (D16's amendment).
    #[default]
    Veto,
    /// `repeat: "unchanged"`: `{ from, to, unchanged: true }`, nothing written.
    Unchanged,
}

/// The outcome of a terminal state, or of a state finished names (D113):
/// how the work it ends went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum WorkflowOutcome {
    Success,
    Failure,
    Neutral,
}

impl WorkflowOutcome {
    /// The outcome as a config writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            WorkflowOutcome::Success => "success",
            WorkflowOutcome::Failure => "failure",
            WorkflowOutcome::Neutral => "neutral",
        }
    }

    /// The outcome a config's text names; `None` for another text.
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "success" => Some(WorkflowOutcome::Success),
            "failure" => Some(WorkflowOutcome::Failure),
            "neutral" => Some(WorkflowOutcome::Neutral),
            _ => None,
        }
    }
}

/// Workflow's config, parsed: the initial state filled in, every terminal
/// state's outcome, the outcome of each state `finished` names, and the
/// type of each field a transition's `by` names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkflowConfig {
    pub states: Vec<String>,
    pub initial: String,
    pub transitions: Vec<WorkflowTransition>,
    /// The outcome of each terminal state, in the order of `states`.
    pub outcomes: Vec<(String, WorkflowOutcome)>,
    /// The outcome of each state finished names, a state a transition
    /// leaves (D113), in the order of `states`.
    pub finished: Vec<(String, WorkflowOutcome)>,
    /// The permission that moves a caller past every transition's by (D104).
    pub override_permission: Option<String>,
    pub repeat: WorkflowRepeat,
    /// Each field a transition's by names, once, in the order the config
    /// first names it, with its IR type as the type's spec gives it: what
    /// decides how its value names a subject.
    pub subject_types: Vec<(String, String)>,
    /// Each seat a transition's by names (`seat:<name>`, D104), once, in the
    /// order the config first names it: what the guard asks Seats' rule
    /// whether the caller is seated in.
    pub seats: Vec<SeatRef>,
}

// What the configSchema lets through.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RawConfig {
    states: Vec<String>,
    #[serde(default)]
    initial: Option<String>,
    transitions: Vec<WorkflowTransition>,
    #[serde(default)]
    outcomes: Option<Map<String, Value>>,
    #[serde(default)]
    finished: Option<Map<String, Value>>,
    #[serde(default, rename = "overridePermission")]
    override_permission: Option<String>,
    #[serde(default)]
    repeat: Option<String>,
}

impl WorkflowConfig {
    /// Parses and checks a config as a type gives it, on the type the
    /// target names: as the declaration's configSchema holds it, each issue
    /// at its path in the schema loader's words, then as a state machine, a
    /// rule's reason in the engine's words, which the core prefixes with
    /// the type and the behavior, then `finished` against its states
    /// (D113), and then each transition's `by` against the type's fields
    /// (D104).
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<WorkflowConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let raw: RawConfig =
            serde_json::from_value(raw.clone()).map_err(|error| error.to_string())?;
        let states = raw.states;
        let has = |state: &str| states.iter().any(|known| known == state);
        let all = states.join(", ");
        let initial = raw
            .initial
            .unwrap_or_else(|| states.first().cloned().unwrap_or_default());
        if !has(&initial) {
            return Err(format!(
                "the initial state \"{initial}\" is not one of its states ({all})"
            )
            .into());
        }
        let mut seen: Vec<(&str, &str)> = Vec::new();
        for WorkflowTransition { from, to, .. } in &raw.transitions {
            for state in [from, to] {
                if !has(state) {
                    return Err(format!(
                        "the transition from \"{from}\" to \"{to}\" names \"{state}\", which is not one of its states ({all})"
                    ).into());
                }
            }
            if from == to {
                return Err(
                    format!("a transition from \"{from}\" to itself changes nothing").into(),
                );
            }
            if seen.contains(&(from.as_str(), to.as_str())) {
                return Err(
                    format!("the transition from \"{from}\" to \"{to}\" is listed twice").into(),
                );
            }
            seen.push((from, to));
        }
        let mut config = WorkflowConfig {
            states: states.clone(),
            initial,
            transitions: raw.transitions,
            outcomes: Vec::new(),
            finished: Vec::new(),
            override_permission: raw.override_permission,
            repeat: match raw.repeat.as_deref() {
                Some(REPEAT_UNCHANGED) => WorkflowRepeat::Unchanged,
                _ => WorkflowRepeat::Veto,
            },
            subject_types: Vec::new(),
            seats: Vec::new(),
        };
        let declared = raw.outcomes.unwrap_or_default();
        for state in declared.keys() {
            if !has(state) {
                return Err(format!(
                    "outcomes names \"{state}\", which is not one of its states ({all})"
                )
                .into());
            }
            if !config.is_terminal(state) {
                return Err(format!(
                    "outcomes names \"{state}\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome"
                ).into());
            }
        }
        // A state no transition reaches stays allowed: a new version keeps
        // every state a row may be in, including one it no longer enters.
        config.outcomes = states
            .iter()
            .filter(|state| config.is_terminal(state))
            .map(|state| {
                let named = declared
                    .get(state.as_str())
                    .and_then(Value::as_str)
                    .and_then(WorkflowOutcome::parse);
                (state.clone(), named.unwrap_or(WorkflowOutcome::Success))
            })
            .collect();
        // finished names a state a transition leaves (D113): a terminal
        // state's outcome is outcomes'.
        let named = raw.finished.unwrap_or_default();
        for state in named.keys() {
            if !has(state) {
                return Err(format!(
                    "finished names \"{state}\", which is not one of its states ({all})"
                )
                .into());
            }
            if config.is_terminal(state) {
                return Err(format!(
                    "finished names \"{state}\", which is a terminal state: no transition leaves it, and outcomes names a terminal state's outcome"
                ).into());
            }
        }
        config.finished = states
            .iter()
            .filter_map(|state| {
                let outcome = named
                    .get(state.as_str())
                    .and_then(Value::as_str)
                    .and_then(WorkflowOutcome::parse)?;
                Some((state.clone(), outcome))
            })
            .collect();
        (config.subject_types, config.seats) = by_entries(&config.transitions, target)?;
        Ok(config)
    }

    /// The IR type of a field a transition's by names, as the type's spec
    /// gives it; `None` for a field no by names.
    pub fn subject_type(&self, field: &str) -> Option<&str> {
        self.subject_types
            .iter()
            .find(|(name, _)| name == field)
            .map(|(_, type_name)| type_name.as_str())
    }

    /// The seat a transition's by names, by its name; `None` for a seat no
    /// by names.
    pub fn seat(&self, name: &str) -> Option<&SeatRef> {
        self.seats.iter().find(|seat| seat.seat == name)
    }

    /// Whether a state is one of the config's.
    pub fn has_state(&self, state: &str) -> bool {
        self.states.iter().any(|known| known == state)
    }

    /// Whether a state is terminal: one of its states that no transition
    /// leaves. A state the config does not list is not terminal.
    pub fn is_terminal(&self, state: &str) -> bool {
        self.has_state(state)
            && !self
                .transitions
                .iter()
                .any(|transition| transition.from == state)
    }

    /// Whether a state counts as finished: a terminal state, or one
    /// finished names, which a transition leaves (D113).
    pub fn is_finished(&self, state: &str) -> bool {
        self.is_terminal(state) || self.finished.iter().any(|(named, _)| named == state)
    }

    /// The outcome of a terminal state, or of a state finished names
    /// (D113); `None` for any other state.
    pub fn outcome(&self, state: &str) -> Option<WorkflowOutcome> {
        self.outcomes
            .iter()
            .chain(&self.finished)
            .find(|(named, _)| named == state)
            .map(|(_, outcome)| *outcome)
    }

    /// The transitions that leave a state, in the config's order.
    pub fn moves(&self, from: &str) -> Vec<&WorkflowTransition> {
        self.transitions
            .iter()
            .filter(|transition| transition.from == from)
            .collect()
    }

    // targets lists the states a transition leads to from one.
    fn targets(&self, from: &str) -> Vec<&str> {
        self.moves(from)
            .into_iter()
            .map(|transition| transition.to.as_str())
            .collect()
    }
}

// What a config's by entries come to: each field with its IR type, and each
// seat.
type ByEntries = (Vec<(String, String)>, Vec<SeatRef>);

// by_entries holds each transition's by to the type, in config order, each
// entry in order, refusing the first that breaks: a seat the type's Seats
// lacks, or of a type that implements none (D104), a field the type lacks,
// and a field of a type that holds no subject. It is each field a by names,
// once, in the order the config first names it, with its IR type, and each
// seat so.
fn by_entries(
    transitions: &[WorkflowTransition],
    target: &ConfigTarget<'_>,
) -> Result<ByEntries, ConfigError> {
    let fields = target.fields();
    let type_name = target.type_name();
    let mut found: Vec<(String, String)> = Vec::new();
    let mut seated: Vec<SeatRef> = Vec::new();
    for WorkflowTransition { from, to, by, .. } in transitions {
        for entry in by {
            let named =
                format!("the transition from \"{from}\" to \"{to}\" names \"{entry}\" in by");
            if let Some(seat) = seats::seat_name(entry) {
                let found = seats::seat_of(target, seat)
                    .map_err(|missing| seats::missing_reason(&named, type_name, &missing))?;
                if !seated.iter().any(|known| known.seat == found.seat) {
                    seated.push(found);
                }
                continue;
            }
            let Some(field) = fields.iter().find(|field| field.name == *entry) else {
                let names: Vec<&str> = fields.iter().map(|field| field.name.as_str()).collect();
                let names = if names.is_empty() {
                    "none".to_owned()
                } else {
                    names.join(", ")
                };
                return Err(format!(
                    "{named}, which is not a field of {type_name} (its fields: {names})"
                )
                .into());
            };
            if !holds_subject(&field.type_name) {
                return Err(format!(
                    "{named}, a {} field of {type_name}: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)",
                    field.type_name
                )
                .into());
            }
            if !found.iter().any(|(name, _)| name == entry) {
                found.push((entry.clone(), field.type_name.clone()));
            }
        }
    }
    Ok((found, seated))
}

// The states and transitions of a config as a type holds it.
fn listed<'c>(config: &'c Value, key: &str) -> impl Iterator<Item = &'c Value> {
    config
        .get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

/// Whether a state of a Workflow config, as a type holds it, is terminal:
/// one of its states that no transition leaves (`isTerminalState`). A
/// behavior or a client asks it about any row's status. A state the config
/// does not list is not terminal.
pub fn is_terminal_state(config: &Value, state: &str) -> bool {
    listed(config, "states").any(|known| known.as_str() == Some(state))
        && !listed(config, "transitions")
            .any(|transition| transition.get("from").and_then(Value::as_str) == Some(state))
}

/// The outcome of a state of a Workflow config, as a type holds it
/// (`stateOutcome`): for a terminal state, the one its outcomes names, or
/// success; for a state a transition leaves, the one its finished names
/// (D113). Any other state has none.
pub fn state_outcome(config: &Value, state: &str) -> Option<WorkflowOutcome> {
    let named = |member: &str| {
        config
            .get(member)
            .and_then(|outcomes| outcomes.get(state))
            .and_then(Value::as_str)
            .and_then(WorkflowOutcome::parse)
    };
    if is_terminal_state(config, state) {
        return Some(named("outcomes").unwrap_or(WorkflowOutcome::Success));
    }
    named("finished")
}

/// The status of a row a host read, under a Workflow use's id (`Workflow`
/// on a type anchor, the field's name on a field anchor): what another
/// behavior reads of a row it does not run on. `None` when it has none.
pub fn status_of<'r>(row: &'r Row, use_id: &str) -> Option<&'r str> {
    row.owned(use_id, STATUS).and_then(Value::as_str)
}

/// When a row's status under a Workflow use's id last changed, in epoch
/// milliseconds; `None` for a row written before Workflow kept it.
pub fn entered_at_of(row: &Row, use_id: &str) -> Option<i64> {
    row.owned(use_id, ENTERED_AT).and_then(Value::as_i64)
}

/// Workflow: the state machine on a row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Workflow;

impl Behavior for Workflow {
    type Config = WorkflowConfig;

    // The declaration file names the value field, the status (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<WorkflowConfig, ConfigError> {
        WorkflowConfig::parse(raw, target)
    }

    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&WorkflowConfig>,
        after: Option<&WorkflowConfig>,
    ) -> Hook<Option<String>> {
        let Some(before) = before else {
            return Ok(Some(
                "the instances that exist have no status to start from".to_owned(),
            ));
        };
        let Some(after) = after else {
            return Ok(Some("the instances would lose their status".to_owned()));
        };
        let gone: Vec<String> = before
            .states
            .iter()
            .filter(|state| !after.has_state(state))
            .map(|state| format!("\"{state}\""))
            .collect();
        // No row is in a state the new config drops.
        if gone.is_empty() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(format!(
            "an instance may be in {}, which the new config drops",
            gone.join(", ")
        )))
    }

    fn check_params(
        &self,
        _config: &WorkflowConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != TRANSITION {
            return Vec::new();
        }
        transition_issues(params).into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, WorkflowConfig>, _params: &Value) -> Hook<()> {
        cx.set(STATUS, cx.config().initial.as_str())?;
        cx.set(ENTERED_AT, cx.now())
    }

    // settle answers its use's transition before any use's guard is asked
    // (D104): a state the config lacks, in to or in from, is a bad
    // parameter; a from that is not the row's status is the veto stale,
    // before anything else is checked; and with repeat unchanged, a move to
    // the status the row is in answers unchanged and writes nothing, so no
    // other use's gate refuses a move that is no move. A row with no status
    // goes on to the guard, which vetoes it.
    fn settle(
        &self,
        view: &View<'_, WorkflowConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        if operation != TRANSITION {
            return Ok(None);
        }
        let config = view.config();
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        if !config.has_state(to) {
            return Err(not_a_state(view, "/to", to));
        }
        let read = params.get("from").and_then(Value::as_str);
        if let Some(read) = read.filter(|read| !config.has_state(read)) {
            return Err(not_a_state(view, "/from", read));
        }
        let Some(status) = status(view) else {
            return Ok(None);
        };
        if let Some(read) = read.filter(|read| *read != status) {
            let mut details = Map::new();
            details.insert("from".into(), Value::from(read));
            details.insert("status".into(), Value::from(status.as_str()));
            return Err(view.veto(
                Veto::new(format!("{} is {status}, not {read}", target(view)))
                    .with_code("stale")
                    .with_details(details),
            ));
        }
        if config.repeat == WorkflowRepeat::Unchanged && to == status {
            return Ok(Some(json!({ "from": status, "to": to, "unchanged": true })));
        }
        Ok(None)
    }

    // guard checks every transition request of its own use, a caller's or
    // another use's call(), before any handler runs and after its settle:
    // a state the config lacks is a bad parameter, a move the config does
    // not list a veto, a move whose by names a subject the caller is not,
    // who holds no overridePermission, a veto too, and a move whose
    // permission the caller lacks forbidden.
    fn guard(
        &self,
        view: &View<'_, WorkflowConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let Some(params) = request.operation_of(view.use_id(), TRANSITION) else {
            return Ok(None);
        };
        let config = view.config();
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        if !config.has_state(to) {
            return Err(not_a_state(view, "/to", to));
        }
        let Some(from) = status(view) else {
            let missing = view.field().unwrap_or(STATUS);
            return Ok(Some(
                Veto::new(format!("{} has no {missing}", row(view))).with_code("no_status"),
            ));
        };
        let mut details = Map::new();
        details.insert("from".into(), Value::from(from.as_str()));
        details.insert("to".into(), Value::from(to));
        if from == to {
            return Ok(Some(
                Veto::new(format!("{} is already {to}", target(view)))
                    .with_code("already_in_state")
                    .with_details(details),
            ));
        }
        let Some(found) = config
            .moves(&from)
            .into_iter()
            .find(|transition| transition.to == to)
        else {
            let next = config.targets(&from);
            if next.is_empty() {
                return Ok(Some(
                    Veto::new(format!(
                        "{from} is a terminal state: no transition leaves it"
                    ))
                    .with_code("terminal_state")
                    .with_details(details),
                ));
            }
            let reason = format!(
                "no transition leads from {from} to {to}; from {from} it can move to {}",
                next.join(", ")
            );
            details.insert(
                "allowed".into(),
                Value::Array(next.into_iter().map(Value::from).collect()),
            );
            return Ok(Some(
                Veto::new(reason)
                    .with_code("transition_not_allowed")
                    .with_details(details),
            ));
        };
        if !found.by.is_empty() && !is_principal(view, &found.by)? {
            let who = match view.caller().subject.as_str() {
                "" => "a caller with no subject".to_owned(),
                subject => subject.to_owned(),
            };
            details.insert(
                "by".into(),
                Value::Array(
                    found
                        .by
                        .iter()
                        .map(|field| Value::from(field.as_str()))
                        .collect(),
                ),
            );
            return Ok(Some(
                Veto::new(format!(
                    "{who} may not move {} from {from} to {to}: the transition is made by {}",
                    target(view),
                    seats::by_named(&found.by)
                ))
                .with_code("not_principal")
                .with_details(details),
            ));
        }
        if let Some(permission) = &found.permission {
            if !view.can(permission)? {
                return Err(view.forbidden(format!(
                    "{} may not move {} from {from} to {to}: the transition needs permission {permission}",
                    view.caller().subject,
                    target(view)
                )));
            }
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, WorkflowConfig>, name: &str, params: &Value) -> Hook<Value> {
        if name != TRANSITION {
            return Err(cx.defect(format!("it declares no operation {name}")));
        }
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let from = status(cx).unwrap_or_else(|| "null".to_owned());
        if !cx
            .config()
            .moves(&from)
            .iter()
            .any(|transition| transition.to == to)
        {
            return Err(cx.defect(format!(
                "transition from {from} to {to} reached its handler without its guard"
            )));
        }
        cx.set(STATUS, to)?;
        cx.set(ENTERED_AT, cx.now())?;
        Ok(json!({ "from": from, "to": to }))
    }
}

// is_principal says whether the caller may make a transition whose by names
// fields and seats: it is the subject one of its fields holds, read first,
// or a subject seated in one of its seats, through Seats' own rule, or it
// holds the config's overridePermission (D104).
fn is_principal(view: &View<'_, WorkflowConfig>, by: &[String]) -> Hook<bool> {
    let config = view.config();
    let subject = view.caller().subject.as_str();
    let named = by
        .iter()
        .filter(|entry| seats::seat_name(entry).is_none())
        .any(|field| {
            let type_name = config.subject_type(field).unwrap_or("string");
            names_subject(type_name, view.own_field(field).as_ref(), subject)
        });
    if named {
        return Ok(true);
    }
    for seat in by.iter().filter_map(|entry| seats::seat_name(entry)) {
        if let Some(seat) = config.seat(seat) {
            if seats::holds_seat(view, seat, subject)? {
                return Ok(true);
            }
        }
    }
    match &config.override_permission {
        Some(permission) => view.can(permission),
        None => Ok(false),
    }
}

// not_a_state refuses a parameter that names a state the config lacks, at
// its pointer, as an invalid argument.
fn not_a_state(view: &View<'_, WorkflowConfig>, at: &str, state: &str) -> Stop {
    Refusal::operation_params(
        view.behavior(),
        view.use_id(),
        TRANSITION,
        vec![SchemaIssue::new(
            at,
            format!(
                "\"{state}\" is not a state of {} ({})",
                subject(view),
                view.config().states.join(", ")
            ),
        )],
    )
    .into()
}

// status is the use's status as the run has it; `None` when it has none.
fn status<C>(view: &View<'_, C>) -> Option<String> {
    view.get(STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// target names what a refusal is about: the row, and on a field anchor the
// field (`Order o1's fulfilment`).
fn target<C>(view: &View<'_, C>) -> String {
    match view.field() {
        Some(field) => format!("{}'s {field}", row(view)),
        None => row(view),
    }
}

// subject names whose states a refusal lists: the type, and on a field
// anchor the field (`Order.fulfilment`).
fn subject<C>(view: &View<'_, C>) -> String {
    match view.field() {
        Some(field) => format!("{}.{field}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

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
    for key in ["states", "transitions"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if config
        .get("initial")
        .is_some_and(|initial| !initial.is_string())
    {
        issues.push("/initial", "type", "must be string");
    }
    if let Some(states) = config.get("states") {
        state_issues(states, &mut issues);
    }
    if let Some(transitions) = config.get("transitions") {
        transition_list_issues(transitions, &mut issues);
    }
    if let Some(outcomes) = config.get("outcomes") {
        outcome_issues("/outcomes", outcomes, &mut issues);
    }
    if let Some(finished) = config.get("finished") {
        outcome_issues("/finished", finished, &mut issues);
    }
    if let Some(permission) = config.get("overridePermission") {
        schema::text_issues("/overridePermission", permission, &mut issues);
    }
    if config
        .get("repeat")
        .is_some_and(|repeat| repeat.as_str() != Some(REPEAT_UNCHANGED))
    {
        issues.push(
            "/repeat",
            "enum",
            format!("must be one of {}", schema::quoted(REPEAT_UNCHANGED)),
        );
    }
    issues
}

fn state_issues(states: &Value, issues: &mut Issues) {
    let Some(items) = states.as_array() else {
        issues.push("/states", "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push("/states", "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        let at = format!("/states/{i}");
        match item.as_str() {
            None => issues.push(at, "type", "must be string"),
            Some(name) => {
                for (keyword, message) in name_issues(name) {
                    issues.push(at.clone(), keyword, message);
                }
            }
        }
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            "/states",
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

fn transition_list_issues(transitions: &Value, issues: &mut Issues) {
    let Some(items) = transitions.as_array() else {
        issues.push("/transitions", "type", "must be array");
        return;
    };
    for (i, item) in items.iter().enumerate() {
        let at = format!("/transitions/{i}");
        let Some(transition) = item.as_object() else {
            issues.push(at, "type", "must be object");
            continue;
        };
        for key in transition
            .keys()
            .filter(|key| !TRANSITION_KEYS.contains(&key.as_str()))
        {
            issues.push(
                at.clone(),
                "additionalProperties",
                format!("unknown key {}", schema::quoted(key)),
            );
        }
        for key in ["from", "to"] {
            match transition.get(key) {
                None => issues.push(
                    at.clone(),
                    "required",
                    format!("must have required property '{key}'"),
                ),
                Some(value) if !value.is_string() => {
                    issues.push(schema::member(&at, key), "type", "must be string");
                }
                Some(_) => {}
            }
        }
        match transition.get("permission") {
            Some(Value::String(permission)) if schema::length(permission) < 1 => issues.push(
                schema::member(&at, "permission"),
                "minLength",
                "must NOT have fewer than 1 characters",
            ),
            Some(Value::String(_)) | None => {}
            Some(_) => issues.push(schema::member(&at, "permission"), "type", "must be string"),
        }
        if let Some(by) = transition.get("by") {
            by_issues(&schema::member(&at, "by"), by, issues);
        }
    }
}

// by_issues lists what a transition's by breaks of its schema: a non-empty
// list of non-empty strings, each once.
fn by_issues(at: &str, by: &Value, issues: &mut Issues) {
    let Some(items) = by.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (j, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{j}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// outcome_issues lists what outcomes, or finished, breaks of its schema, at
// its path: a non-empty object of an outcome by a state's name.
fn outcome_issues(at: &str, outcomes: &Value, issues: &mut Issues) {
    let Some(named) = outcomes.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (state, outcome) in named {
        // A key that is no state's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = name_issues(state);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push(at, keyword, message);
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        if !outcome
            .as_str()
            .is_some_and(|text| OUTCOMES.contains(&text))
        {
            issues.push(
                schema::member(at, state),
                "enum",
                format!("must be one of {}", OUTCOMES.map(schema::quoted).join(", ")),
            );
        }
    }
}

// name_issues lists what a state's name breaks of the configSchema: its
// length and its pattern, each a keyword of its own.
fn name_issues(name: &str) -> Vec<(&'static str, String)> {
    schema::name_issues(name)
}

// is_state_name matches STATE_PATTERN: a letter, then letters, digits,
// underscores and hyphens, ASCII alone.
#[cfg(test)]
fn is_state_name(name: &str) -> bool {
    schema::is_name(name)
}

// transition_issues lists what transition's parameters break of its
// closed paramsSchema, in the behavior registry's words.
fn transition_issues(params: &Value) -> Issues {
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
    match given.get("to") {
        None => issues.push("", "required", "must have required property 'to'"),
        Some(to) => schema::text_issues("/to", to, &mut issues),
    }
    if let Some(from) = given.get("from") {
        schema::text_issues("/from", from, &mut issues);
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;
    use superschematic_rules::{FieldSpec, TypeSpec, UseSpec};

    fn order_flow() -> Value {
        json!({
            "states": ["draft", "open", "shipped", "cancelled"],
            "transitions": [
                {"from": "draft", "to": "open"},
                {"from": "draft", "to": "cancelled"},
                {"from": "open", "to": "shipped", "permission": "orders.ship"},
                {"from": "open", "to": "cancelled"}
            ]
        })
    }

    // A task is done and may be reopened: finished names done, which a
    // transition leaves, a success; dropped and failed are terminal.
    fn task_flow() -> Value {
        json!({
            "states": ["todo", "doing", "done", "dropped", "failed"],
            "transitions": [
                {"from": "todo", "to": "doing"},
                {"from": "doing", "to": "done"},
                {"from": "doing", "to": "dropped"},
                {"from": "doing", "to": "failed"},
                {"from": "done", "to": "todo"}
            ],
            "outcomes": {"dropped": "neutral"},
            "finished": {"done": "success"}
        })
    }

    fn file() -> Value {
        serde_json::from_str(DECLARATION).expect("the declaration file parses")
    }

    // parse parses a config on an Order whose own fields are title, a
    // string, and assignee, a UUID.
    fn parse(config: &Value) -> Result<WorkflowConfig, ConfigError> {
        let spec = TypeSpec::new("Order")
            .with(UseSpec::on_type(NAME, config.clone()))
            .with_field(FieldSpec::new("title", "string"))
            .with_field(FieldSpec::new("assignee", "Identity.UUID"));
        WorkflowConfig::parse(config, &ConfigTarget::new(&spec, 0))
    }

    // undescribed is a schema with every description left out, at any
    // depth: what the checks implement, and nothing else.
    fn undescribed(schema: &Value) -> Value {
        match schema {
            Value::Object(members) => Value::Object(
                members
                    .iter()
                    .filter(|(key, _)| key.as_str() != "description")
                    .map(|(key, member)| (key.clone(), undescribed(member)))
                    .collect(),
            ),
            Value::Array(items) => Value::Array(items.iter().map(undescribed).collect()),
            other => other.clone(),
        }
    }

    #[test]
    fn a_terminal_state_is_one_no_transition_leaves() {
        let flow = order_flow();
        assert!(is_terminal_state(&flow, "shipped"));
        assert!(is_terminal_state(&flow, "cancelled"));
        assert!(!is_terminal_state(&flow, "open"));
        assert!(!is_terminal_state(&flow, "lost"));
        let config = parse(&flow).expect("a state machine");
        assert!(config.is_terminal("shipped"));
        assert!(!config.is_terminal("draft"));
        assert!(!config.is_terminal("lost"));
    }

    #[test]
    fn a_terminal_state_has_the_outcome_outcomes_names_or_success() {
        let mut flow = order_flow();
        flow["outcomes"] = json!({"cancelled": "failure"});
        assert_eq!(
            state_outcome(&flow, "cancelled"),
            Some(WorkflowOutcome::Failure)
        );
        assert_eq!(
            state_outcome(&flow, "shipped"),
            Some(WorkflowOutcome::Success)
        );
        assert_eq!(state_outcome(&flow, "open"), None);
        assert_eq!(state_outcome(&flow, "lost"), None);
        // A config without outcomes means what it did before them.
        assert_eq!(
            state_outcome(&order_flow(), "cancelled"),
            Some(WorkflowOutcome::Success)
        );
        // A state named like an Object member is a state like any other.
        let named = json!({"states": ["open", "constructor"], "transitions": [{"from": "open", "to": "constructor"}], "outcomes": {}});
        assert_eq!(
            state_outcome(&named, "constructor"),
            Some(WorkflowOutcome::Success)
        );
        // The parsed config agrees, for every terminal state.
        let config = parse(&flow).expect("a state machine");
        assert_eq!(
            config.outcomes,
            vec![
                ("shipped".to_owned(), WorkflowOutcome::Success),
                ("cancelled".to_owned(), WorkflowOutcome::Failure)
            ]
        );
        assert_eq!(config.outcome("open"), None);
    }

    #[test]
    fn a_state_finished_names_is_finished_though_a_transition_leaves_it() {
        let flow = task_flow();
        let config = parse(&flow).expect("a state machine");
        // done is not terminal: a transition leaves it.
        assert!(!config.is_terminal("done"));
        assert!(!is_terminal_state(&flow, "done"));
        assert!(config.is_finished("done"));
        assert_eq!(config.outcome("done"), Some(WorkflowOutcome::Success));
        assert_eq!(
            config.finished,
            vec![("done".to_owned(), WorkflowOutcome::Success)]
        );
        // outcomes keeps the terminal states alone, named or a success.
        assert_eq!(
            config.outcomes,
            vec![
                ("dropped".to_owned(), WorkflowOutcome::Neutral),
                ("failed".to_owned(), WorkflowOutcome::Success)
            ]
        );
        assert!(config.is_terminal("dropped") && config.is_finished("dropped"));
        assert_eq!(config.outcome("dropped"), Some(WorkflowOutcome::Neutral));
        // A state a transition leaves that finished does not name is not
        // finished, nor is a state the config does not list.
        for state in ["todo", "doing", "lost"] {
            assert!(!config.is_finished(state), "{state}");
            assert_eq!(config.outcome(state), None, "{state}");
        }
        // A config without finished names none.
        let plain = parse(&order_flow()).expect("a state machine");
        assert!(plain.finished.is_empty());
        assert!(!plain.is_finished("open") && plain.is_finished("shipped"));
        // finished comes in the order of states, whatever order names them,
        // and a config may have no terminal state.
        let round = parse(&json!({
            "states": ["a", "b", "c"],
            "transitions": [{"from": "a", "to": "b"}, {"from": "b", "to": "c"}, {"from": "c", "to": "a"}],
            "finished": {"c": "failure", "a": "neutral"}
        }))
        .expect("a state machine");
        assert_eq!(
            round.finished,
            vec![
                ("a".to_owned(), WorkflowOutcome::Neutral),
                ("c".to_owned(), WorkflowOutcome::Failure)
            ]
        );
        assert!(round.outcomes.is_empty());
        assert!(round.is_finished("c") && !round.is_finished("b"));
    }

    #[test]
    fn state_outcome_answers_for_a_state_finished_names_as_for_a_terminal_one() {
        let flow = task_flow();
        // A state a transition leaves, which finished names.
        assert_eq!(state_outcome(&flow, "done"), Some(WorkflowOutcome::Success));
        // A terminal state outcomes names, and one it does not.
        assert_eq!(
            state_outcome(&flow, "dropped"),
            Some(WorkflowOutcome::Neutral)
        );
        assert_eq!(
            state_outcome(&flow, "failed"),
            Some(WorkflowOutcome::Success)
        );
        // A state that is neither, and one the config does not list.
        assert_eq!(state_outcome(&flow, "doing"), None);
        assert_eq!(state_outcome(&flow, "todo"), None);
        assert_eq!(state_outcome(&flow, "lost"), None);
        // Each outcome finished may name.
        for outcome in OUTCOMES {
            let mut named = task_flow();
            named["finished"]["done"] = json!(outcome);
            assert_eq!(
                state_outcome(&named, "done"),
                WorkflowOutcome::parse(outcome)
            );
        }
        // Without finished, a state a transition leaves has none, as before.
        let mut plain = task_flow();
        plain
            .as_object_mut()
            .expect("an object")
            .shift_remove("finished");
        assert_eq!(state_outcome(&plain, "done"), None);
    }

    #[test]
    fn finished_names_only_a_state_a_transition_leaves() {
        let reason = |config: Value| match parse(&config) {
            Err(ConfigError::Rule(reason)) => reason,
            other => panic!("not a rule's refusal: {other:?}"),
        };
        let with = |members: Value| {
            let mut flow = task_flow();
            for (key, value) in members.as_object().expect("an object") {
                flow[key] = value.clone();
            }
            flow
        };
        let all = "(todo, doing, done, dropped, failed)";
        assert_eq!(
            reason(with(json!({"finished": {"lost": "success"}}))),
            format!("finished names \"lost\", which is not one of its states {all}")
        );
        assert_eq!(
            reason(with(json!({"finished": {"dropped": "neutral"}}))),
            "finished names \"dropped\", which is a terminal state: no transition leaves it, and outcomes names a terminal state's outcome"
        );
        // The first key that breaks, in the order the config holds them.
        assert_eq!(
            reason(with(json!({"finished": {"done": "success", "failed": "failure", "lost": "neutral"}}))),
            "finished names \"failed\", which is a terminal state: no transition leaves it, and outcomes names a terminal state's outcome"
        );
        // After outcomes' rules, which stay as they were, and before by's.
        assert_eq!(
            reason(with(json!({"outcomes": {"done": "success"}, "finished": {"lost": "success"}}))),
            "outcomes names \"done\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome"
        );
        let mut by = with(json!({"finished": {"lost": "success"}}));
        by["transitions"][0]["by"] = json!(["askee"]);
        assert_eq!(
            reason(by),
            format!("finished names \"lost\", which is not one of its states {all}")
        );
    }

    #[test]
    fn finished_is_held_to_its_schema_as_outcomes_is() {
        // The issues as the core lists them: `<pointer>: <message>`, joined.
        let reason = |member: &str, value: Value| {
            let mut flow = task_flow();
            flow[member] = value;
            ConfigError::Schema(config_issues(&flow).into_issues()).to_string()
        };
        let pattern = format!("must match pattern \"{STATE_PATTERN}\"");
        for member in ["finished", "outcomes"] {
            assert_eq!(
                reason(member, json!({})),
                format!("/{member}: must NOT have fewer than 1 properties")
            );
            assert_eq!(
                reason(member, json!({"done": "aborted"})),
                format!("/{member}/done: must be one of \"success\", \"failure\", \"neutral\"")
            );
            assert_eq!(
                reason(member, json!({"done": true})),
                format!("/{member}/done: must be one of \"success\", \"failure\", \"neutral\"")
            );
            assert_eq!(
                reason(member, json!(["done"])),
                format!("/{member}: must be object")
            );
            assert_eq!(
                reason(member, json!({"in review": "failure"})),
                format!("/{member}: {pattern}; /{member}: property name must be valid")
            );
        }
        assert_eq!(
            ConfigError::Schema(config_issues(&task_flow()).into_issues()).to_string(),
            ""
        );
    }

    #[test]
    fn a_parsed_config_fills_in_its_initial_state_and_lists_moves() {
        let config = parse(&order_flow()).expect("a state machine");
        assert_eq!(config.initial, "draft");
        assert_eq!(config.targets("open"), vec!["shipped", "cancelled"]);
        assert_eq!(
            config.moves("open")[0].permission.as_deref(),
            Some("orders.ship")
        );
        assert!(config.moves("shipped").is_empty());
        let mut later = order_flow();
        later["initial"] = json!("open");
        assert_eq!(parse(&later).expect("a state machine").initial, "open");
    }

    #[test]
    fn an_outcome_reads_and_writes_as_a_config_does() {
        for outcome in [
            WorkflowOutcome::Success,
            WorkflowOutcome::Failure,
            WorkflowOutcome::Neutral,
        ] {
            assert_eq!(WorkflowOutcome::parse(outcome.as_str()), Some(outcome));
            assert_eq!(
                serde_json::to_value(outcome).expect("an outcome"),
                outcome.as_str()
            );
        }
        assert_eq!(WorkflowOutcome::parse("aborted"), None);
    }

    #[test]
    fn a_state_name_matches_the_pattern() {
        for name in ["draft", "in-review", "step_2", "A"] {
            assert!(is_state_name(name), "{name}");
        }
        for name in ["", "in review", "2nd", "_x", "é"] {
            assert!(!is_state_name(name), "{name}");
        }
    }

    /// The checks this module writes by hand are the declaration's
    /// configSchema and paramsSchema, keyword for keyword, and what its
    /// handler and its settle answer is its resultSchema: each schema, its
    /// descriptions left out, is exactly the one they implement, so a
    /// keyword the file gains fails this test until the checks take it.
    #[test]
    fn the_checks_are_the_declaration_files_schemas() {
        let file = file();
        let name = json!({"maxLength": STATE_MAX_LENGTH, "pattern": STATE_PATTERN});
        let mut state = name.clone();
        state["type"] = json!("string");
        assert_eq!(
            undescribed(&file["configSchema"]),
            json!({
                "additionalProperties": false,
                "properties": {
                    "finished": {
                        "additionalProperties": {"enum": OUTCOMES},
                        "minProperties": 1,
                        "propertyNames": name,
                        "type": "object"
                    },
                    "initial": {"type": "string"},
                    "outcomes": {
                        "additionalProperties": {"enum": OUTCOMES},
                        "minProperties": 1,
                        "propertyNames": name,
                        "type": "object"
                    },
                    "states": {
                        "items": state,
                        "minItems": 1,
                        "type": "array",
                        "uniqueItems": true
                    },
                    "overridePermission": {"minLength": 1, "type": "string"},
                    "repeat": {"enum": [REPEAT_UNCHANGED]},
                    "transitions": {
                        "items": {
                            "additionalProperties": false,
                            "properties": {
                                "by": {
                                    "items": {"minLength": 1, "type": "string"},
                                    "minItems": 1,
                                    "type": "array",
                                    "uniqueItems": true
                                },
                                "from": {"type": "string"},
                                "permission": {"minLength": 1, "type": "string"},
                                "to": {"type": "string"}
                            },
                            "required": ["from", "to"],
                            "type": "object"
                        },
                        "type": "array"
                    }
                },
                "required": ["states", "transitions"],
                "type": "object"
            })
        );
        let mut properties: Vec<&String> = file["configSchema"]["properties"]
            .as_object()
            .expect("properties")
            .keys()
            .collect();
        properties.sort();
        assert_eq!(properties, CONFIG_KEYS);
        let transition = &file["configSchema"]["properties"]["transitions"]["items"];
        let mut members: Vec<&String> = transition["properties"]
            .as_object()
            .expect("properties")
            .keys()
            .collect();
        members.sort();
        assert_eq!(members, TRANSITION_KEYS);
        let operations = file["operations"].as_array().expect("operations");
        assert_eq!(operations.len(), 1);
        assert_eq!(operations[0]["name"], json!(TRANSITION));
        assert_eq!(
            undescribed(&operations[0]["paramsSchema"]),
            json!({
                "additionalProperties": false,
                "properties": {
                    "from": {"minLength": 1, "type": "string"},
                    "to": {"minLength": 1, "type": "string"}
                },
                "required": ["to"],
                "type": "object"
            })
        );
        let mut params: Vec<&String> = operations[0]["paramsSchema"]["properties"]
            .as_object()
            .expect("properties")
            .keys()
            .collect();
        params.sort();
        assert_eq!(params, PARAMS_KEYS);
        // The result the handler and the settle answer: from and to, and
        // unchanged only when a repeat wrote nothing.
        assert_eq!(
            undescribed(&operations[0]["resultSchema"]),
            json!({
                "additionalProperties": false,
                "properties": {
                    "from": {"type": "string"},
                    "to": {"type": "string"},
                    "unchanged": {"type": "boolean"}
                },
                "required": ["from", "to"],
                "type": "object"
            })
        );
        assert_eq!(file.get("createParamsSchema"), None);
    }

    #[test]
    fn the_declaration_is_the_files() {
        assert_eq!(
            Workflow.declaration(),
            Declaration::new(NAME)
                .value(STATUS)
                .field(ENTERED_AT)
                .operation(TRANSITION, true)
                .veto("already_in_state")
                .veto("terminal_state")
                .veto("transition_not_allowed")
                .veto("no_status")
                .veto("not_principal")
                .veto("stale")
        );
    }

    #[test]
    fn transitions_parameters_are_held_to_its_paramsschema() {
        // The issues as the core lists them: `<pointer>: <message>`, joined.
        let reason = |params: Value| {
            ConfigError::Schema(transition_issues(&params).into_issues()).to_string()
        };
        assert_eq!(
            reason(json!({"status": "open"})),
            "must NOT have additional properties: status; must have required property 'to'"
        );
        assert_eq!(
            reason(json!({"to": ""})),
            "/to: must NOT have fewer than 1 characters"
        );
        assert_eq!(reason(json!({"to": 5})), "/to: must be string");
        assert_eq!(reason(json!("open")), "must be object");
        assert_eq!(reason(json!({"to": "open"})), "");
        // from, the state the caller read, is a non-empty string beside to.
        assert_eq!(reason(json!({"to": "open", "from": "draft"})), "");
        assert_eq!(
            reason(json!({"to": "open", "from": ""})),
            "/from: must NOT have fewer than 1 characters"
        );
        assert_eq!(
            reason(json!({"to": "open", "from": 5})),
            "/from: must be string"
        );
        // Issues come by pointer, as the engine sorts them.
        assert_eq!(
            reason(json!({"status": "open", "from": 5})),
            "must NOT have additional properties: status; must have required property 'to'; /from: must be string"
        );
        assert_eq!(
            reason(json!({"to": "", "from": ""})),
            "/from: must NOT have fewer than 1 characters; /to: must NOT have fewer than 1 characters"
        );
    }

    #[test]
    fn a_parsed_config_holds_by_override_permission_repeat_and_the_fields_by_names() {
        let config = parse(&json!({
            "states": ["open", "done", "closed"],
            "transitions": [
                {"from": "open", "to": "done", "by": ["assignee", "title"]},
                {"from": "done", "to": "closed", "by": ["assignee"], "permission": "close"}
            ],
            "overridePermission": "moderate",
            "repeat": "unchanged"
        }))
        .expect("a state machine");
        assert_eq!(config.transitions[0].by, ["assignee", "title"]);
        assert_eq!(config.override_permission.as_deref(), Some("moderate"));
        assert_eq!(config.repeat, WorkflowRepeat::Unchanged);
        assert_eq!(
            config.subject_types,
            vec![
                ("assignee".to_owned(), "Identity.UUID".to_owned()),
                ("title".to_owned(), "string".to_owned())
            ]
        );
        assert_eq!(config.subject_type("title"), Some("string"));
        assert_eq!(config.subject_type("status"), None);
        // A config with neither names no by and vetoes a repeat.
        let plain = parse(&order_flow()).expect("a state machine");
        assert_eq!(plain.repeat, WorkflowRepeat::Veto);
        assert!(plain.override_permission.is_none() && plain.subject_types.is_empty());
        assert!(plain.transitions.iter().all(|t| t.by.is_empty()));
        // A transition writes by only when it names one.
        assert_eq!(
            serde_json::to_value(&plain.transitions[0]).expect("a transition"),
            json!({"from": "draft", "to": "open"})
        );
        assert_eq!(
            serde_json::to_value(&config.transitions[1]).expect("a transition"),
            json!({"from": "done", "to": "closed", "permission": "close", "by": ["assignee"]})
        );
        // A refusal names a by of fields alone as it did before seats.
        let by = |entries: &[&str]| -> String {
            seats::by_named(&entries.iter().map(|e| (*e).to_owned()).collect::<Vec<_>>())
        };
        assert_eq!(by(&["a"]), "the subject a holds");
        assert_eq!(by(&["a", "b"]), "the subject a or b holds");
        assert_eq!(by(&["a", "b", "c"]), "the subject a, b or c holds");
        assert!(config.seats.is_empty());
    }

    #[test]
    fn a_status_is_read_from_a_row_by_use_id() {
        let mut row = Row {
            version: 3,
            ..Row::default()
        };
        row.owned.insert(
            "fulfilment".into(),
            json!({"status": "packed", "enteredAt": 1_000_000})
                .as_object()
                .cloned()
                .expect("an object"),
        );
        assert_eq!(status_of(&row, "fulfilment"), Some("packed"));
        assert_eq!(entered_at_of(&row, "fulfilment"), Some(1_000_000));
        assert_eq!(status_of(&row, NAME), None);
        assert_eq!(entered_at_of(&row, NAME), None);
    }
}
