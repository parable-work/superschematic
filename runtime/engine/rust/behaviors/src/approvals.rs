//! Approvals, the core's sign-offs on the moves of the type's Workflow
//! (D56), the port of `src/behaviors/core/approvals.ts`. The config gates
//! states: a transition into a gated state, from a state the gate's `from`
//! lists (every state when it lists none), waits until `count` distinct
//! principals have signed off for it. A sign-off is one principal's, by
//! its subject, for one state; `signOff` needs the gate's permission, and a
//! caller who lacks it is forbidden, as Revisions' approve is. The
//! instance's creator, the `created_by` of its row (`sql.instances()`),
//! cannot sign unless the gate sets `excludeCreator` to false, and nobody
//! signs for the state the instance is in. `withdrawSignOff` takes the
//! caller's own back, and `listSignOffs` reads them a page at a time,
//! oldest first; it never goes through a write. Every refusal is a veto
//! with a code the declaration lists, but the missing permission.
//!
//! The guard reads the state from transition's `to`, which Workflow's
//! closed paramsSchema makes the only parameter, and runs for every
//! transition request, a caller's, another behavior's `call()` and another
//! instance's invoke alike, before any handler: `approvals_missing`, with
//! the state, the count it requires and the sign-offs it has in its
//! details. It reads the status only when the gate is not met, and lets a
//! move Workflow refuses through for Workflow to refuse, whichever of the
//! two the type lists first.
//!
//! A sign-off counts until it is consumed. afterChange consumes them, in
//! the change's transaction, from the instance as the change leaves it,
//! never from which operation ran: a Timers fire, a Lease expiry or a
//! Reactions move changes the status as a caller's transition does.
//!
//! - A change of the own fields, an update or an operation's `update()`,
//!   consumes every gate's, unless `resetOnChange` is false: what was
//!   signed is what moves.
//! - An instance in a gated state has that gate's consumed: entering it
//!   used them, so a return to it signs again.
//!
//! A consumed sign-off stays in the behavior's table, with the time it was
//! consumed, for `listSignOffs`; the `Approvals.gates` field holds the ones
//! that count, by state, each gate's subjects in the order they signed, and
//! no gate with none. A withdrawal deletes its row, and the operation's
//! event records it. Deleting the instance deletes its sign-offs.
//!
//! Every statement is the same on SQLite and Postgres (D58): the table is
//! a [`TableSpec`] whose sign-off number the database assigns, with a
//! partial unique index on the sign-offs that count, and each parameter's
//! type is the column's it is compared with or written to.
//!
//! configChange: gates may be added, removed and changed; afterConfigChange
//! consumes a removed gate's sign-offs, and a gate's new count or
//! permission applies to the ones already given. Approvals can be added to
//! a schema that has instances, which start with none, and cannot be
//! removed from one: its sign-offs would stay behind with nothing to delete
//! them.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, IndexSpec, InstanceCx, InstanceView, Page,
    TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, Result,
};

use crate::staterules::status_of;

/// One gate of an Approvals config, parsed: `excludeCreator` filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsGate {
    /// How many distinct principals must sign off before a transition into
    /// the state.
    pub count: i64,
    /// The permission a caller needs to sign off.
    pub permission: String,
    /// The states a transition from is gated; every state when `None`.
    pub from: Option<Vec<String>>,
    /// Whether the principal who created the instance is refused a
    /// sign-off.
    pub exclude_creator: bool,
}

/// Approvals' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsConfig {
    /// The gates, by the state of the type's Workflow each gates, in the
    /// config's order.
    pub gates: Vec<(String, ApprovalsGate)>,
    /// Whether a change of the instance's own fields consumes every gate's
    /// sign-offs.
    pub reset_on_change: bool,
}

impl ApprovalsConfig {
    /// The gate on a state; `None` when the config gates it not.
    pub fn gate(&self, state: &str) -> Option<&ApprovalsGate> {
        self.gates
            .iter()
            .find(|(gated, _)| gated == state)
            .map(|(_, gate)| gate)
    }
}

const NAME: &str = "Approvals";

const COLUMNS: &str = "signoff, state, subject, note, at, consumed_at";

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// sign_off_of reads a sign-off row, as withdrawSignOff and listSignOffs
// return it: a member with no value left out.
fn sign_off_of(row: &Row) -> Result<Value> {
    let mut out = Map::new();
    out.insert("state".into(), Value::from(row.text("state")?));
    out.insert("subject".into(), Value::from(row.text("subject")?));
    if let Some(note) = row.opt_text("note")? {
        out.insert("note".into(), Value::from(note));
    }
    out.insert("at".into(), Value::from(row.i64("at")?));
    if let Some(consumed_at) = row.opt_i64("consumed_at")? {
        out.insert("consumedAt".into(), Value::from(consumed_at));
    }
    Ok(Value::Object(out))
}

// signed_by is the subjects whose sign-off for a state counts, in the
// order they signed.
fn signed_by<C>(view: &InstanceView<'_, C>, state: &str) -> Result<Vec<String>> {
    let table = view.sql().table("signoffs")?;
    let mut params = key(view);
    params.push(SqlValue::from(state));
    view.sql()
        .all(
            format!("SELECT subject FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND consumed_at IS NULL ORDER BY signoff"),
            &params,
        )?
        .iter()
        .map(|row| Ok(row.text("subject")?.to_owned()))
        .collect()
}

// gate_of is the config's gate on a state an operation names, which must
// be one.
fn gate_of<'c>(
    view: &InstanceView<'_, ApprovalsConfig>,
    config: &'c ApprovalsConfig,
    operation: &str,
    state: &str,
) -> Result<&'c ApprovalsGate> {
    config.gate(state).ok_or_else(|| {
        let gated: Vec<&str> = config
            .gates
            .iter()
            .map(|(gated, _)| gated.as_str())
            .collect();
        refuse(
            view,
            operation,
            format!(
                "{state} is not a state {}'s Approvals gates (it gates {})",
                view.schema(),
                gated.join(", ")
            ),
            "no_gate",
        )
    })
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

// consume marks the sign-offs that count as consumed: every gate's, or one
// state's.
fn consume(cx: &InstanceCx<'_, ApprovalsConfig>, state: Option<&str>) -> Result<()> {
    let table = cx.sql().table("signoffs")?;
    let mut params = vec![SqlValue::Integer(cx.now())];
    params.extend(key(cx));
    let only = match state {
        Some(state) => {
            params.push(SqlValue::from(state));
            " AND state = ?"
        }
        None => "",
    };
    cx.sql().run(
        format!("UPDATE {table} SET consumed_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL{only}"),
        &params,
    )?;
    Ok(())
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

/// Approvals: sign-offs a move into a gated state waits for.
#[derive(Clone, Copy, Debug, Default)]
pub struct Approvals;

impl Behavior for Approvals {
    type Config = ApprovalsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Approvals.behavior.json")
    }

    // The configSchema holds the shape; this holds every gated state and
    // every from state to the states of the type's Workflow, whose config it
    // is given, and fills in the defaults.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ApprovalsConfig> {
        let empty = Map::new();
        let gates = raw
            .get("gates")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        // A type without Workflow, or a Workflow config of the wrong shape,
        // is the requires check's or Workflow's own to refuse.
        let states: Option<Vec<String>> = target
            .configs()
            .get("Workflow")
            .and_then(|flow| flow.get("states"))
            .and_then(Value::as_array)
            .map(|states| {
                states
                    .iter()
                    .map(|state| match state {
                        Value::String(state) => state.clone(),
                        other => other.to_string(),
                    })
                    .collect()
            });
        let mut parsed = Vec::new();
        for state in js_keys(gates) {
            let gate = &gates[state];
            let from = gate.get("from").map(|from| strings(Some(from)));
            if let Some(states) = &states {
                let all = states.join(", ");
                if !states.iter().any(|known| known == state) {
                    return Err(Error::config(format!(
                        "gate \"{state}\" is not a state of the type's Workflow ({all})"
                    )));
                }
                for source in from.iter().flatten() {
                    if source == state {
                        return Err(Error::config(format!(
                            "gate \"{state}\": from names \"{state}\" itself, and no transition leads from a state to itself"
                        )));
                    }
                    if !states.contains(source) {
                        return Err(Error::config(format!(
                            "gate \"{state}\": from state \"{source}\" is not a state of the type's Workflow ({all})"
                        )));
                    }
                }
            }
            parsed.push((
                state.to_owned(),
                ApprovalsGate {
                    count: match gate.get("count") {
                        Some(Value::Number(count)) => number_value(count) as i64,
                        _ => 0,
                    },
                    permission: gate
                        .get("permission")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                    from,
                    exclude_creator: gate.get("excludeCreator") != Some(&Value::Bool(false)),
                },
            ));
        }
        Ok(ApprovalsConfig {
            gates: parsed,
            reset_on_change: raw.get("resetOnChange") != Some(&Value::Bool(false)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the sign-offs its instances hold would stay behind with nothing to delete them"
                        .to_owned()
                }))
            }),
            // A removed gate's sign-offs no longer count. A version without
            // Approvals is published only on a schema with no instances,
            // which hold none; whatever is left goes.
            after_config_change: Some(|_, cx| {
                let table = cx.sql().table("signoffs")?;
                let scope = [SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())];
                let Some(config) = cx.config() else {
                    cx.sql().run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
                        &scope,
                    )?;
                    return Ok(());
                };
                let Some(before) = cx.before() else {
                    return Ok(());
                };
                let removed: Vec<&str> = before
                    .gates
                    .iter()
                    .map(|(state, _)| state.as_str())
                    .filter(|state| config.gate(state).is_none())
                    .collect();
                if removed.is_empty() {
                    return Ok(());
                }
                let mut params = vec![SqlValue::Integer(cx.now())];
                params.extend(scope);
                params.extend(removed.iter().map(|state| SqlValue::from(*state)));
                let marks = vec!["?"; removed.len()].join(", ");
                cx.sql().run(
                    format!("UPDATE {table} SET consumed_at = ? WHERE namespace = ? AND schema = ? AND consumed_at IS NULL AND state IN ({marks})"),
                    &params,
                )?;
                Ok(())
            }),
            guidance: Some(|_, config, target| {
                crate::guidance::approvals::guidance(config, target)
            }),
            // One sign-off that counts per principal and state; consumed
            // ones stay beside it.
            migrations: vec![BehaviorMigration::new(1, "sign-offs").table(
                TableSpec::new("signoffs")
                    .row_id("signoff")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("state", ColumnSpec::text().not_null())
                    .column("subject", ColumnSpec::text().not_null())
                    .column("note", ColumnSpec::text())
                    .column("at", ColumnSpec::integer().not_null())
                    .column("consumed_at", ColumnSpec::integer())
                    .index(
                        IndexSpec::new(
                            "signoffs_live",
                            &["namespace", "schema", "id", "state", "subject"],
                        )
                        .unique()
                        .filter("consumed_at IS NULL"),
                    )
                    .index(IndexSpec::new(
                        "signoffs_by_instance",
                        &["namespace", "schema", "id", "signoff"],
                    )),
            )],
            guard: Some(guard),
            operations: vec![
                ("signOff", sign_off),
                ("withdrawSignOff", |_, cx, params| {
                    let state = params
                        .get("state")
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    gate_of(cx, cx.config(), "withdrawSignOff", state)?;
                    let subject = cx.principal().subject.as_str();
                    let table = cx.sql().table("signoffs")?;
                    let mut params = key(cx);
                    params.extend([SqlValue::from(state), SqlValue::from(subject)]);
                    let Some(row) = cx.sql().get(
                        format!("SELECT {COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND subject = ? AND consumed_at IS NULL"),
                        &params,
                    )?
                    else {
                        return Err(refuse(
                            cx,
                            "withdrawSignOff",
                            format!(
                                "{subject} has no sign-off on {} {} for {state} to withdraw",
                                cx.schema(),
                                cx.id()
                            ),
                            "not_signed",
                        ));
                    };
                    cx.sql().run(
                        format!("DELETE FROM {table} WHERE signoff = ?"),
                        &[SqlValue::Integer(row.i64("signoff")?)],
                    )?;
                    sign_off_of(&row)
                }),
                ("listSignOffs", |_, cx, params| {
                    let request = page_request(NAME, "listSignOffs", params)?;
                    let table = cx.sql().table("signoffs")?;
                    let mut values = key(cx);
                    values.push(SqlValue::Integer(request.after));
                    let only = match params.get("state").and_then(Value::as_str) {
                        Some(state) => {
                            values.push(SqlValue::from(state));
                            " AND state = ?"
                        }
                        None => "",
                    };
                    values.push(SqlValue::Integer(request.limit + 1));
                    let rows = cx.sql().all(
                        format!(
                            "SELECT {COLUMNS} FROM {table}
                             WHERE namespace = ? AND schema = ? AND id = ? AND signoff > ?{only}
                             ORDER BY signoff LIMIT ?"
                        ),
                        &values,
                    )?;
                    let found = page(rows, request.limit, |row| {
                        row.i64("signoff").unwrap_or_default()
                    });
                    Ok(Page {
                        items: found
                            .items
                            .iter()
                            .map(sign_off_of)
                            .collect::<Result<Vec<_>>>()?,
                        next: found.next,
                    }
                    .to_json())
                }),
            ],
            // The sign-offs that count, by state, in the order they were
            // given.
            fields: vec![("gates", |_, view| {
                let table = view.sql().table("signoffs")?;
                let rows = view.sql().all(
                    format!("SELECT state, subject FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL ORDER BY signoff"),
                    &key(view),
                )?;
                let mut gates = Map::new();
                for row in &rows {
                    let entry = gates
                        .entry(row.text("state")?.to_owned())
                        .or_insert_with(|| json!({ "signedBy": [] }));
                    if let Some(Value::Array(subjects)) = entry.get_mut("signedBy") {
                        subjects.push(Value::from(row.text("subject")?));
                    }
                }
                Ok(Some(Value::Object(gates)))
            })],
            // Consumes what no longer counts, from the instance as the change
            // left it: every gate's after a change of the own fields (with
            // resetOnChange), and the gate's of the state the instance is in.
            after_change: Some(|_, cx, change| {
                let table = cx.sql().table("signoffs")?;
                if matches!(change, InstanceChange::Delete) {
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                    return Ok(());
                }
                let live = cx.sql().all(
                    format!("SELECT DISTINCT state FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL"),
                    &key(cx),
                )?;
                if live.is_empty() {
                    return Ok(());
                }
                let own = matches!(
                    change,
                    InstanceChange::Update { .. }
                        | InstanceChange::Operation {
                            before: Some(_),
                            ..
                        }
                );
                if own && cx.config().reset_on_change {
                    return consume(cx, None);
                }
                if let Some(status) = status_of(cx)? {
                    if live
                        .iter()
                        .any(|row| row.opt_text("state").ok().flatten() == Some(status.as_str()))
                    {
                        consume(cx, Some(&status))?;
                    }
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn sign_off(
    _: &Approvals,
    cx: &superschematic_engine::OperationCx<'_, ApprovalsConfig>,
    params: &Value,
) -> Result<Value> {
    let state = params
        .get("state")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let gate = gate_of(cx, cx.config(), "signOff", state)?;
    let subject = cx.principal().subject.as_str();
    if !cx.can(&gate.permission)? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{subject} may not sign off on {} {} for {state}: it needs permission {}",
                cx.schema(),
                cx.id(),
                gate.permission
            ),
        )
        .into());
    }
    if status_of(cx)?.as_deref() == Some(state) {
        return Err(refuse(
            cx,
            "signOff",
            format!(
                "{} {} is {state} already; a sign-off for it waits until it has left",
                cx.schema(),
                cx.id()
            ),
            "in_state",
        ));
    }
    if gate.exclude_creator {
        let instances = cx.sql().instances()?;
        let row = cx.sql().get(
            format!("SELECT created_by FROM {instances} WHERE id = ?"),
            &[SqlValue::from(cx.id())],
        )?;
        if let Some(row) = row {
            if row.text("created_by")? == subject {
                return Err(refuse(
                    cx,
                    "signOff",
                    format!(
                        "{subject} created {} {}, and its gate on {state} takes no sign-off from its creator",
                        cx.schema(),
                        cx.id()
                    ),
                    "creator_cannot_sign",
                ));
            }
        }
    }
    let table = cx.sql().table("signoffs")?;
    let mut found = key(cx);
    found.extend([SqlValue::from(state), SqlValue::from(subject)]);
    if cx
        .sql()
        .get(
            format!("SELECT 1 AS found FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND subject = ? AND consumed_at IS NULL"),
            &found,
        )?
        .is_some()
    {
        return Err(refuse(
            cx,
            "signOff",
            format!(
                "{subject} has signed off on {} {} for {state} already",
                cx.schema(),
                cx.id()
            ),
            "already_signed",
        ));
    }
    let note = params.get("note").and_then(Value::as_str);
    let mut values = key(cx);
    values.extend([
        SqlValue::from(state),
        SqlValue::from(subject),
        note.map_or(SqlValue::Null, SqlValue::from),
        SqlValue::Integer(cx.now()),
    ]);
    cx.sql().run(
        format!("INSERT INTO {table} (namespace, schema, id, state, subject, note, at) VALUES (?, ?, ?, ?, ?, ?, ?)"),
        &values,
    )?;
    let mut out = Map::new();
    out.insert("state".into(), Value::from(state));
    out.insert("subject".into(), Value::from(subject));
    if let Some(note) = note {
        out.insert("note".into(), Value::from(note));
    }
    out.insert("at".into(), Value::from(cx.now()));
    out.insert(
        "signedBy".into(),
        Value::Array(signed_by(cx, state)?.into_iter().map(Value::from).collect()),
    );
    out.insert("count".into(), Value::from(gate.count));
    Ok(Value::Object(out))
}

// guard is the gate: a transition of the type's Workflow into a gated
// state, from a state the gate allows, waits for its count of sign-offs.
fn guard(
    _: &Approvals,
    view: &InstanceView<'_, ApprovalsConfig>,
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
    let Some(gate) = view.config().gate(to) else {
        return Ok(None);
    };
    let signed = signed_by(view, to)?.len() as i64;
    if signed >= gate.count {
        return Ok(None);
    }
    // A move Workflow refuses is Workflow's to refuse, whichever of the two
    // the type lists first.
    let Some(from) = status_of(view)? else {
        return Ok(None);
    };
    let flow = view.schemas().config(view.schema(), "Workflow")?;
    let listed = flow
        .as_ref()
        .and_then(|flow| flow.get("transitions"))
        .and_then(Value::as_array)
        .is_some_and(|moves| {
            moves.iter().any(|edge| {
                edge.get("from").and_then(Value::as_str) == Some(from.as_str())
                    && edge.get("to").and_then(Value::as_str) == Some(to)
            })
        });
    if !listed {
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
    Ok(Some(Veto {
        reason: format!(
            "{} {} cannot move to {to} with {signed} of the {} sign-offs it needs",
            view.schema(),
            view.id(),
            gate.count
        ),
        code: Some("approvals_missing".to_owned()),
        details: Some(details),
    }))
}
