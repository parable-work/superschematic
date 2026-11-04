//! Approvals, sign-offs on the moves of a Workflow (D56), ported onto the
//! pure core (D103) from the engine's (`../behaviors/src/approvals.rs`),
//! with gates whose sign-offs a change of chosen fields consumes and that
//! pin the version their signer read (D104). The config gates states: a
//! transition into a gated state, from a state the gate's `from` lists
//! (every state when it lists none), waits until `count` distinct subjects
//! have signed off for it. A sign-off is one caller's, by its subject, for
//! one state; `signOff` needs the gate's permission, and a caller who lacks
//! it is forbidden. The row's creator, the subject its own field
//! `createdBy` holds (the layout's audit field on a table, D87), cannot
//! sign unless the gate sets `excludeCreator` to false, and nobody signs
//! for the state the row is in. `withdrawSignOff` takes the caller's own
//! back, and `listSignOffs` reads them a page at a time, oldest first, and
//! writes nothing. Every refusal is a veto with a code the declaration
//! lists, but the missing permission and a bad parameter.
//!
//! Approvals passes D103's test: its state is its `signOffs` sub-table, a
//! row per sign-off; it has rules over how that state changes (a sign-off
//! given, withdrawn, consumed by a change of the fields its gate names or
//! by entering the gate's state) and over the moves it gates; and it
//! recurs, as an expense two approvers sign, a document's review, a
//! release's go-ahead.
//!
//! The guard reads the state from the `to` of a transition of the Workflow
//! the gates are of, and runs for every transition request, a caller's,
//! another use's `call()` (a Timers fire), another row's `call_on()` (a
//! Reactions move) alike, before any handler: `approvals_missing`, with the
//! state, the count it requires and the sign-offs it has in its details. It
//! lets a move Workflow's config refuses through for Workflow to refuse,
//! whichever of the two the type lists first.
//!
//! A sign-off counts until it is consumed. The after-change hook consumes
//! them, from the row as the change leaves it, never from which operation
//! ran: a Timers fire or a Reactions move changes the status as a caller's
//! transition does.
//!
//! - An update through the core consumes the sign-offs of each gate whose
//!   `resetOn` names a field it changed (D104), `[]` naming none; a gate
//!   without `resetOn` follows `resetOnChange`, true when absent: any update
//!   consumes its sign-offs, what was signed is what moves.
//! - A row in a gated state has that gate's consumed: entering it used
//!   them, so a return to it signs again, whatever `resetOn` says.
//! - A sign-off for a state the config no longer gates is consumed at the
//!   row's next write through the core. The engine's afterConfigChange
//!   consumed them at the publish; the core writes no row at a publish (a
//!   build-time project's config change is a build, D103).
//!
//! A consumed sign-off stays in the sub-table, with the time it was
//! consumed, for `listSignOffs`; a read of the row shows the ones that
//! count as the engine's `Approvals.gates` field ([`Behavior::read_fields`]),
//! by gated state, each gate's subjects in the order they signed, and no
//! gate with none. A withdrawal deletes its row. Deleting the row deletes
//! its sign-offs, which the host's delete does. A sign-off's key is its
//! number on the row, one more than the greatest it has held, in ten digits
//! zero-padded, so the key's order is the order they were given in and the
//! engine's cursor over its row id carries over; a withdrawal of the
//! greatest number frees it for the next, as SQLite's row id did.
//!
//! A gate may be pinned (D104): `signOff` then names `version`, the row's
//! version its signer read, the `@optimistic` column every write through
//! the core is held to and moves, a sign-off's own among them. A `version`
//! on any gate is checked, before any guard and before the gate's
//! permission ([`Behavior::settle`]), as Workflow's `from` is: one that is
//! not the row's version is the veto `stale`, details `{ version, current
//! }`, so the signer reads again; a pinned gate's `signOff` without one is
//! a bad parameter. On a gate whose count is above one, each later signer
//! signs against the version the earlier sign-off left.
//!
//! Which Workflow the gates are of is the config's `status`, the id of a
//! Workflow use of the type, as StateRules' is: implied by the type's one,
//! named among several. The engine read the instance's one `Workflow`.
//!
//! A write around the core (a repository's bulk update, raw SQL) consumes
//! no sign-off: Approvals hears no change (D103), the escape hatch's price.
//! A table with Approvals keeps a sub-table, so its repositories have no
//! upserts.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then each gated and
//! from state held to the Workflow, each `resetOn` field to the type's own
//! and a gate that excludes the creator to a type with a `createdBy` to
//! read it from, in the engine's words ([`ConfigError::Rule`]). The
//! operations' parameters are held to their closed paramsSchemas in the
//! behavior registry's words. The core runs no JSON Schema, so the schemas'
//! checks are written here, held to the declaration file by this crate's
//! tests. Messages name the row `{type} {key}`, where the engine's named
//! `{schema} {id}`.
//!
//! configChange: gates may be added, removed and changed, and a gate's new
//! count or permission applies to the sign-offs already given. Approvals
//! can be added to a type that has rows, which start with none, and cannot
//! be removed from one: its sign-offs would stay behind with nothing to
//! delete them.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    Refusal, Row, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::declared;
use crate::flow::{self, flow_of, Flow};
use crate::paging::{page, page_request, Page};
use crate::schema::{self, Issues};
use crate::subjects::names_subject;
use crate::values::equal;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Approvals";

/// The sub-table that holds a row per sign-off, keyed by its number.
pub const SIGN_OFFS: &str = "signOffs";

/// The operation that signs off on a row for a gated state.
pub const SIGN_OFF: &str = "signOff";

/// The operation that takes a caller's sign-off back.
pub const WITHDRAW_SIGN_OFF: &str = "withdrawSignOff";

/// The operation that lists a row's sign-offs, a page at a time.
pub const LIST_SIGN_OFFS: &str = "listSignOffs";

/// Approvals' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Approvals.behavior.json");

// The own field a gate that excludes the creator reads the creator from:
// the layout's audit field on a table (D87, D104).
const CREATED_BY: &str = "createdBy";

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 3] = ["gates", "resetOnChange", "status"];
const GATE_KEYS: [&str; 6] = [
    "count",
    "excludeCreator",
    "from",
    "permission",
    "pinned",
    "resetOn",
];

// The bounds the configSchema and the paramsSchemas hold values to.
const MAX_COUNT: i64 = 100;
const MAX_RESET_ON: usize = 64;
const MAX_NOTE: usize = 10_000;
const MAX_LIMIT: i64 = 500;

// The width of a sign-off's number in its key.
const NUMBER_WIDTH: usize = 10;

/// One gate of an Approvals config, parsed: `excludeCreator` filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsGate {
    /// How many distinct subjects must sign off before a transition into
    /// the state.
    pub count: i64,
    /// The permission a caller needs to sign off.
    pub permission: String,
    /// The states a transition from is gated; every state when `None`.
    pub from: Option<Vec<String>>,
    /// Whether the subject who created the row is refused a sign-off.
    pub exclude_creator: bool,
    /// The own fields whose change consumes the gate's sign-offs; None
    /// follows resetOnChange (D104).
    pub reset_on: Option<Vec<String>>,
    /// Whether a sign-off names the row's version its signer read (D104).
    pub pinned: bool,
}

/// Approvals' config, parsed: the defaults filled in, and the Workflow its
/// gates are of resolved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsConfig {
    /// By gated state, in the config's order.
    pub gates: Vec<(String, ApprovalsGate)>,
    /// Whether an update consumes the sign-offs of every gate that names no
    /// resetOn.
    pub reset_on_change: bool,
    /// The Workflow use the gates are of, and its transitions.
    pub status: String,
    pub transitions: Vec<(String, String)>,
    /// The IR type of the type's createdBy field, which a gate that
    /// excludes the creator reads; None when no gate excludes it.
    pub creator_type: Option<String>,
}

impl ApprovalsConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the Workflow it reads and each gate held to it and to
    /// the type's own fields, a rule's reason in the engine's words, which
    /// the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ApprovalsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let flow = flow_of(target, raw.get("status").and_then(Value::as_str))?;
        let empty = Map::new();
        let gates = raw
            .get("gates")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let type_name = target.type_name();
        let fields = target.fields();
        let names = || -> String {
            let names: Vec<&str> = fields.iter().map(|field| field.name.as_str()).collect();
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        };
        let creator = fields.iter().find(|field| field.name == CREATED_BY);
        let mut parsed = Vec::new();
        for (state, gate) in gates {
            let all = flow.listed();
            if flow.lacks(state) {
                return Err(format!(
                    "gate \"{state}\" is not a state of the type's Workflow ({all})"
                )
                .into());
            }
            let from = gate.get("from").map(|from| strings(Some(from)));
            for source in from.iter().flatten() {
                if source == state {
                    return Err(format!(
                        "gate \"{state}\": from names \"{state}\" itself, and no transition leads from a state to itself"
                    )
                    .into());
                }
                if flow.lacks(source) {
                    return Err(format!(
                        "gate \"{state}\": from state \"{source}\" is not a state of the type's Workflow ({all})"
                    )
                    .into());
                }
            }
            let reset_on = gate.get("resetOn").map(|fields| strings(Some(fields)));
            for field in reset_on.iter().flatten() {
                if fields.iter().all(|known| known.name != *field) {
                    return Err(format!(
                        "gate \"{state}\": resetOn names \"{field}\", which is not a field of {type_name} (its fields: {})",
                        names()
                    )
                    .into());
                }
            }
            let exclude_creator = gate.get("excludeCreator") != Some(&Value::Bool(false));
            if exclude_creator && creator.is_none() {
                return Err(format!(
                    "gate \"{state}\" excludes the creator (excludeCreator is true when absent), and {type_name} has no field createdBy to read the creator from: give {type_name} a createdBy field, or set excludeCreator to false"
                )
                .into());
            }
            parsed.push((
                state.clone(),
                ApprovalsGate {
                    count: gate.get("count").and_then(whole).unwrap_or_default(),
                    permission: gate
                        .get("permission")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                    from,
                    exclude_creator,
                    reset_on,
                    pinned: gate.get("pinned") == Some(&Value::Bool(true)),
                },
            ));
        }
        let creator_type = parsed
            .iter()
            .any(|(_, gate)| gate.exclude_creator)
            .then(|| creator.map(|field| field.type_name.clone()))
            .flatten();
        let Flow {
            use_id,
            transitions,
            ..
        } = flow;
        Ok(ApprovalsConfig {
            gates: parsed,
            reset_on_change: raw.get("resetOnChange") != Some(&Value::Bool(false)),
            status: use_id,
            transitions,
            creator_type,
        })
    }

    /// The gate on a state; `None` when the config gates it not.
    pub fn gate(&self, state: &str) -> Option<&ApprovalsGate> {
        self.gates
            .iter()
            .find(|(gated, _)| gated == state)
            .map(|(_, gate)| gate)
    }

    // leads says whether a transition of the Workflow leads from one state
    // to another.
    fn leads(&self, from: &str, to: &str) -> bool {
        flow::leads(&self.transitions, from, to)
    }

    // gated lists the gated states, as a refusal names them.
    fn gated(&self) -> String {
        self.gates
            .iter()
            .map(|(state, _)| state.as_str())
            .collect::<Vec<_>>()
            .join(", ")
    }
}

/// Approvals: sign-offs a move into a gated state waits for.
#[derive(Clone, Copy, Debug, Default)]
pub struct Approvals;

impl Behavior for Approvals {
    type Config = ApprovalsConfig;

    // The declaration file names its signOffs sub-table and its computed
    // gates field (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ApprovalsConfig, ConfigError> {
        ApprovalsConfig::parse(raw, target)
    }

    // Gates may change; Approvals joins a type with rows and does not leave
    // one, whose sign-offs would stay behind.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&ApprovalsConfig>,
        after: Option<&ApprovalsConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none()).then(|| {
            "the sign-offs its instances hold would stay behind with nothing to delete them"
                .to_owned()
        }))
    }

    fn check_params(
        &self,
        config: &ApprovalsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let issues = params_issues(operation, params);
        if !issues.is_empty() {
            return issues.into_issues();
        }
        // A pinned gate's sign-off names the version its signer read
        // (D104).
        if operation == SIGN_OFF && params.get("version").is_none() {
            let state = state_param(params);
            if config.gate(state).is_some_and(|gate| gate.pinned) {
                return vec![SchemaIssue::new(
                    "/version",
                    format!(
                        "is required: the gate on {state} is pinned, so a sign-off names the row's version its signer read"
                    ),
                )];
            }
        }
        Vec::new()
    }

    // settle holds a sign-off that names a version to the row's, before any
    // guard and before the gate's permission, as Workflow's from is (D104):
    // a stale read is answered to any caller. Only for a state the config
    // gates; any other goes on to no_gate.
    fn settle(
        &self,
        view: &View<'_, ApprovalsConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        if operation != SIGN_OFF || view.config().gate(state_param(params)).is_none() {
            return Ok(None);
        }
        let (Some(named), Some(current)) = (params.get("version").and_then(whole), view.version())
        else {
            return Ok(None);
        };
        if named == current {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert("version".into(), Value::from(named));
        details.insert("current".into(), Value::from(current));
        Err(view.veto(
            Veto::new(format!(
                "{} is at version {current}, not {named}",
                row(view)
            ))
            .with_code("stale")
            .with_details(details),
        ))
    }

    // guard is the gate: a transition of the Workflow the gates are of into
    // a gated state, from a state the gate allows, waits for its count of
    // sign-offs. A move Workflow's config refuses is Workflow's to refuse.
    fn guard(
        &self,
        view: &View<'_, ApprovalsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        let Some(params) = request.operation_of(&config.status, workflow::TRANSITION) else {
            return Ok(None);
        };
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let Some(gate) = config.gate(to) else {
            return Ok(None);
        };
        let signed = signed_by(&view.sub_rows(SIGN_OFFS)?, to).len() as i64;
        if signed >= gate.count {
            return Ok(None);
        }
        let Some(from) = status_of(view) else {
            return Ok(None);
        };
        if !config.leads(&from, to) {
            return Ok(None);
        }
        if gate
            .from
            .as_ref()
            .is_some_and(|states| !states.contains(&from))
        {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert("state".into(), Value::from(to));
        details.insert("required".into(), Value::from(gate.count));
        details.insert("count".into(), Value::from(signed));
        Ok(Some(
            Veto::new(format!(
                "{} cannot move to {to} with {signed} of the {} sign-offs it needs",
                row(view),
                gate.count
            ))
            .with_code("approvals_missing")
            .with_details(details),
        ))
    }

    fn operation(&self, cx: &Cx<'_, ApprovalsConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            SIGN_OFF => sign_off(cx, params),
            WITHDRAW_SIGN_OFF => withdraw_sign_off(cx, params),
            LIST_SIGN_OFFS => list_sign_offs(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // after_change consumes what no longer counts, from the row as the
    // change left it: the sign-offs of a gate the config no longer has, of
    // each gate an update's change of its fields resets (D104), and of the
    // gate of the state the row is in. A delete takes the row's sub-table
    // rows with it.
    fn after_change(&self, cx: &Cx<'_, ApprovalsConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Delete) {
            return Ok(());
        }
        let rows = cx.sub_rows(SIGN_OFFS)?;
        let live: Vec<&SubRow> = rows.iter().filter(|row| is_live(row)).collect();
        if live.is_empty() {
            return Ok(());
        }
        let config = cx.config();
        let now = cx.own();
        let status = status_of(cx);
        for row in live {
            let state = row
                .value("state")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let consumed = match config.gate(state) {
                // A gate a later build removed.
                None => true,
                Some(gate) => {
                    let reset = match change {
                        Change::Update { before, .. } => match &gate.reset_on {
                            Some(fields) => fields
                                .iter()
                                .any(|field| !equal(before.get(field), now.get(field))),
                            None => config.reset_on_change,
                        },
                        _ => false,
                    };
                    // Entering the gate's state used its sign-offs.
                    reset || status.as_deref() == Some(state)
                }
            };
            if consumed {
                let mut values = Map::new();
                values.insert("consumedAt".into(), Value::from(cx.now()));
                cx.update_row(SIGN_OFFS, &row.key, values)?;
            }
        }
        Ok(())
    }

    // A read of the row shows the sign-offs that count, as the engine's
    // Approvals.gates field.
    fn read_fields(
        &self,
        config: &ApprovalsConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let rows = tables.get(SIGN_OFFS).map_or(&[][..], Vec::as_slice);
        let mut gates = Map::new();
        for row in rows.iter().filter(|row| is_live(row)) {
            let (Some(state), Some(subject)) = (
                row.value("state").and_then(Value::as_str),
                row.value("subject").and_then(Value::as_str),
            ) else {
                continue;
            };
            if config.gate(state).is_none() {
                continue;
            }
            let entry = gates
                .entry(state.to_owned())
                .or_insert_with(|| json!({ "signedBy": [] }));
            if let Some(Value::Array(subjects)) = entry.get_mut("signedBy") {
                subjects.push(Value::from(subject));
            }
        }
        let mut fields = Map::new();
        fields.insert("gates".into(), Value::Object(gates));
        fields
    }
}

fn sign_off(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let state = state_param(params);
    let gate = gate_of(cx, state)?;
    let subject = cx.caller().subject.as_str();
    if !cx.can(&gate.permission)? {
        return Err(cx.forbidden(format!(
            "{subject} may not sign off on {} for {state}: it needs permission {}",
            row(cx),
            gate.permission
        )));
    }
    if status_of(cx).as_deref() == Some(state) {
        return Err(refuse(
            cx,
            format!(
                "{} is {state} already; a sign-off for it waits until it has left",
                row(cx)
            ),
            "in_state",
        ));
    }
    if gate.exclude_creator {
        let type_name = cx.config().creator_type.as_deref().unwrap_or("string");
        if names_subject(type_name, cx.own_field(CREATED_BY).as_ref(), subject) {
            return Err(refuse(
                cx,
                format!(
                    "{subject} created {}, and its gate on {state} takes no sign-off from its creator",
                    row(cx)
                ),
                "creator_cannot_sign",
            ));
        }
    }
    let rows = cx.sub_rows(SIGN_OFFS)?;
    if live_of(&rows, state, subject).is_some() {
        return Err(refuse(
            cx,
            format!(
                "{subject} has signed off on {} for {state} already",
                row(cx)
            ),
            "already_signed",
        ));
    }
    let next = rows.iter().filter_map(number_of).max().unwrap_or(0) + 1;
    let note = params.get("note").and_then(Value::as_str);
    let mut values = Map::new();
    values.insert("state".into(), Value::from(state));
    values.insert("subject".into(), Value::from(subject));
    if let Some(note) = note {
        values.insert("note".into(), Value::from(note));
    }
    values.insert("at".into(), Value::from(cx.now()));
    cx.insert_row(SIGN_OFFS, &key_of(next), values)?;
    let mut out = Map::new();
    out.insert("state".into(), Value::from(state));
    out.insert("subject".into(), Value::from(subject));
    if let Some(note) = note {
        out.insert("note".into(), Value::from(note));
    }
    out.insert("at".into(), Value::from(cx.now()));
    out.insert(
        "signedBy".into(),
        Value::Array(
            signed_by(&cx.sub_rows(SIGN_OFFS)?, state)
                .into_iter()
                .map(Value::from)
                .collect(),
        ),
    );
    out.insert("count".into(), Value::from(gate.count));
    Ok(Value::Object(out))
}

fn withdraw_sign_off(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let state = state_param(params);
    gate_of(cx, state)?;
    let subject = cx.caller().subject.as_str();
    let rows = cx.sub_rows(SIGN_OFFS)?;
    let Some(found) = live_of(&rows, state, subject) else {
        return Err(refuse(
            cx,
            format!(
                "{subject} has no sign-off on {} for {state} to withdraw",
                row(cx)
            ),
            "not_signed",
        ));
    };
    cx.delete_row(SIGN_OFFS, &found.key)?;
    Ok(sign_off_of(found))
}

fn list_sign_offs(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let request = page_request(params).map_err(|issue| -> Stop {
        Refusal::operation_params(cx.behavior(), cx.use_id(), LIST_SIGN_OFFS, vec![issue]).into()
    })?;
    let only = params.get("state").and_then(Value::as_str);
    let rows: Vec<SubRow> = cx
        .sub_rows(SIGN_OFFS)?
        .into_iter()
        .filter(|row| number_of(row).is_some_and(|number| number > request.after))
        .filter(|row| {
            only.is_none_or(|state| row.value("state").and_then(Value::as_str) == Some(state))
        })
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(rows, request.limit, |row| {
        number_of(row).unwrap_or_default()
    });
    Ok(Page {
        items: found.items.iter().map(sign_off_of).collect(),
        next: found.next,
    }
    .into_json())
}

// gate_of is the config's gate on a state an operation names, which must
// be one.
fn gate_of<'c>(view: &View<'c, ApprovalsConfig>, state: &str) -> Hook<&'c ApprovalsGate> {
    let config = view.config();
    config.gate(state).ok_or_else(|| {
        refuse(
            view,
            format!(
                "{state} is not a state {}'s Approvals gates (it gates {})",
                view.type_name(),
                config.gated()
            ),
            "no_gate",
        )
    })
}

// refuse is a veto of one of the behavior's operations, with no details.
fn refuse<C>(view: &View<'_, C>, reason: String, code: &str) -> Stop {
    view.veto(Veto::new(reason).with_code(code))
}

// sign_off_of reads a sign-off row as withdrawSignOff and listSignOffs
// return it: a member with no value left out.
fn sign_off_of(row: &SubRow) -> Value {
    let mut out = Map::new();
    for field in ["state", "subject", "note", "at", "consumedAt"] {
        if let Some(value) = row.value(field) {
            out.insert(field.into(), value.clone());
        }
    }
    Value::Object(out)
}

// is_live says whether a sign-off counts: it has not been consumed.
fn is_live(row: &SubRow) -> bool {
    row.value("consumedAt").is_none()
}

// signed_by is the subjects whose sign-off for a state counts, in the
// order they signed.
fn signed_by<'r>(rows: &'r [SubRow], state: &str) -> Vec<&'r str> {
    rows.iter()
        .filter(|row| is_live(row) && row.value("state").and_then(Value::as_str) == Some(state))
        .filter_map(|row| row.value("subject").and_then(Value::as_str))
        .collect()
}

// live_of is a subject's sign-off for a state that counts.
fn live_of<'r>(rows: &'r [SubRow], state: &str, subject: &str) -> Option<&'r SubRow> {
    rows.iter().find(|row| {
        is_live(row)
            && row.value("state").and_then(Value::as_str) == Some(state)
            && row.value("subject").and_then(Value::as_str) == Some(subject)
    })
}

// number_of is a sign-off's number, from its key.
fn number_of(row: &SubRow) -> Option<i64> {
    row.key.parse().ok()
}

// key_of is a sign-off's key: its number in ten digits, zero-padded.
fn key_of(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
}

// status_of is the status of the Workflow the gates are of, as the run has
// it; `None` when the row has none.
fn status_of(view: &View<'_, ApprovalsConfig>) -> Option<String> {
    view.get_of(&view.config().status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn state_param(params: &Value) -> &str {
    params
        .get("state")
        .and_then(Value::as_str)
        .unwrap_or_default()
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

// ---- the configSchema and the paramsSchemas, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    if !config.contains_key("gates") {
        issues.push("", "required", "must have required property 'gates'");
    }
    if config
        .get("resetOnChange")
        .is_some_and(|reset| !reset.is_boolean())
    {
        issues.push("/resetOnChange", "type", "must be boolean");
    }
    if let Some(status) = config.get("status") {
        schema::text_issues("/status", status, &mut issues);
    }
    if let Some(gates) = config.get("gates") {
        gates_issues(gates, &mut issues);
    }
    issues
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
        gate_issues(&schema::member(at, state), gate, issues);
    }
}

fn gate_issues(at: &str, gate: &Value, issues: &mut Issues) {
    let Some(members) = gate.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &GATE_KEYS, issues);
    for key in ["count", "permission"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(count) = members.get("count") {
        schema::integer_issues(
            &schema::member(at, "count"),
            count,
            Some(1),
            Some(MAX_COUNT),
            issues,
        );
    }
    if let Some(permission) = members.get("permission") {
        schema::text_issues(&schema::member(at, "permission"), permission, issues);
    }
    for key in ["excludeCreator", "pinned"] {
        if members.get(key).is_some_and(|flag| !flag.is_boolean()) {
            issues.push(schema::member(at, key), "type", "must be boolean");
        }
    }
    if let Some(from) = members.get("from") {
        list_issues(&schema::member(at, "from"), from, Some(1), None, issues);
    }
    if let Some(reset_on) = members.get("resetOn") {
        list_issues(
            &schema::member(at, "resetOn"),
            reset_on,
            None,
            Some(MAX_RESET_ON),
            issues,
        );
    }
}

// list_issues lists what a list of names breaks of `{ type: array,
// minItems?, maxItems?, uniqueItems: true, items: { type: string,
// minLength: 1 } }`.
fn list_issues(
    at: &str,
    value: &Value,
    min: Option<usize>,
    max: Option<usize>,
    issues: &mut Issues,
) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if let Some(min) = min.filter(|min| items.len() < *min) {
        issues.push(
            at,
            "minItems",
            format!("must NOT have fewer than {min} items"),
        );
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

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
fn unknown_keys(at: &str, members: &Map<String, Value>, known: &[&str], issues: &mut Issues) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words: signOff names its state,
// and a note and a version it may; withdrawSignOff its state; listSignOffs
// a limit, a cursor and a state it may.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = match operation {
        SIGN_OFF => &["note", "state", "version"],
        WITHDRAW_SIGN_OFF => &["state"],
        LIST_SIGN_OFFS => &["cursor", "limit", "state"],
        _ => return issues,
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if operation != LIST_SIGN_OFFS && !given.contains_key("state") {
        issues.push("", "required", "must have required property 'state'");
    }
    if let Some(state) = given.get("state") {
        schema::text_issues("/state", state, &mut issues);
    }
    if operation == SIGN_OFF {
        match given.get("note") {
            Some(Value::String(note)) if schema::length(note) > MAX_NOTE => issues.push(
                "/note",
                "maxLength",
                format!("must NOT have more than {MAX_NOTE} characters"),
            ),
            Some(Value::String(_)) | None => {}
            Some(_) => issues.push("/note", "type", "must be string"),
        }
        if let Some(version) = given.get("version") {
            schema::integer_issues("/version", version, Some(1), None, &mut issues);
        }
    }
    if operation == LIST_SIGN_OFFS {
        if let Some(limit) = given.get("limit") {
            schema::integer_issues("/limit", limit, Some(1), Some(MAX_LIMIT), &mut issues);
        }
        if given
            .get("cursor")
            .is_some_and(|cursor| !cursor.is_string())
        {
            issues.push("/cursor", "type", "must be string");
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_sign_offs_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of(1), "0000000001");
        assert_eq!(key_of(1_234_567_890), "1234567890");
        assert_eq!(number_of(&SubRow::new("0000000042", Map::new())), Some(42));
        assert_eq!(number_of(&SubRow::new("x", Map::new())), None);
    }
}
