//! Lease, an exclusive, time-bounded lease on an instance (D16), the port
//! of `typescript/src/lease.ts`, held by one principal and numbered by a
//! fencing token. The token is a per-instance integer that starts at 0 and
//! advances at every acquire, every release and every applied expiry, so
//! (instance, token) names one lease. A caller presents it as Lease's
//! precondition (`preconditions: { Lease: { token } }`), the one way any
//! write presents it: the guard refuses a write that presents another
//! token, whoever calls (`token_stale`), so a process of the holder's
//! principal that lost its lease cannot write once a sibling holds a new
//! one. A heartbeat, an acknowledgement and the holder's release need the
//! token; with `requireToken`, so does every other write while a lease is
//! active, the holder's own included. The token is no capability: its
//! field, `Lease.token`, shows it to every reader, and the guard still
//! checks who calls. It fences a principal's processes from each other,
//! not principals.
//!
//! A lease is active while it is held, before its expiry time and within
//! its longest hold since acquire (`maxHoldMs`, or the instance's own
//! `maxHoldField`). A lease past either has lapsed: it gives its holder
//! nothing, so the holder's heartbeat, release, acknowledgement and writes
//! are refused, and only its expiry can follow. `expire` applies the
//! expiry, and anyone who may write the instance may call it; `acquire`
//! applies it first over a lapsed lease; and the `expire` schedule, which
//! the engine's runner runs every `sweepMs` as its principal, calls it on
//! every lapsed lease of the schema, found through the lease columns
//! across the schema (`sql().instances()`) and their index. A principal
//! with `overridePermission` expires a given holder's lease at once,
//! active or not (`expire` with `holder`), and every lease that holder has
//! on the schema (`expireHolder`), for a holder that is gone;
//! `notRenewedAfter` spares an active lease acquired or renewed after a
//! time, whose own heartbeats show the process holding it alive, and
//! `expireHolder` returns the ids it expired. An expiry clears the holder,
//! advances the token, counts the expiry unless the instance is in a
//! terminal state of its Workflow (a holder that finished and died before
//! releasing has not failed), and moves the status with `onExpiry`, or
//! `escalate` at the expiry that reaches `maxExpiries`, only from the
//! config's from states and only through Workflow's transition, whose
//! guards run. A transition a guard vetoes leaves the status as it is and
//! the lease expired. Once the instance has had `maxExpiries` expiries, it
//! cannot be leased until a principal with `overridePermission` resets
//! them. A release applies `onExpiry` too, without counting an expiry, so
//! a holder that hands work back leaves it where it can be taken again; a
//! release with `abandon` is the holder giving the work up as failed, and
//! counts as an expiry, escalate included. The `ended` field says how the
//! last lease ended: release, abandon, or the expiry's reason, ttl, maxHold
//! or holder. `acquire` clears it, so every end's event carries it.
//!
//! The guard keeps a lease exclusive. While a lease is active it refuses an
//! update, a delete and every writing operation of another behavior by any
//! principal but the holder, except an operation the config exempts and a
//! principal with `overridePermission`; with `requireToken`, it refuses the
//! holder's too unless they present the token. A read-only operation
//! passes, and so does a request a behavior's own code makes (`call()` or
//! `update()`, which name it as caller), and Queue's `refresh`, which only
//! recomputes Queue's own copies of the instance's facts. Lease's own
//! operations check their callers themselves, but for `direct`, whose
//! permission the guard asks. Nothing changes the field that limits the
//! hold (`maxHoldField`) while a lease is held.
//!
//! Directives are a holder's control channel. `direct` attaches one to the
//! current token, once per `dedupeKey` when it gives one; `heartbeat`
//! returns the ones not acknowledged, and `acknowledge`, or heartbeat's own
//! `acknowledge`, marks them handled. A principal's direct needs
//! `directPermission` (`overridePermission` when that is absent), which the
//! guard asks; another behavior of the type sends one through `call()`
//! with none. When the token advances, the directives of the lease it ends
//! are deleted, so none reaches the next holder.
//!
//! `directOn` sends one when the target of a link of the type's Links
//! moves on, as Reactions' revised hears it ([`target_move`]): a new
//! revision of Revisions, or a release of Branches. Lease's reactions hear
//! the link's schema on the runner, as its principal, and invoke `direct`
//! on each instance the move reaches ([`moved_on`]) whose lease has a
//! holder, with the entry's data and `revised` and a dedupeKey per move of
//! a target, so a holder hears each move once. `direct` refuses a lapsed
//! lease, which the reaction passes over; a free instance hears nothing.
//! The runner's principal needs `directPermission`, or
//! `overridePermission`, which `parse_config` requires a directOn config to
//! name. Without directOn the reactions are off (`watches` is `None`).
//!
//! A list filters on the holder (`where: { "Lease.holder": "wren" }`), the
//! holder's column through an index on it. Whether a lease is active turns
//! on the clock, which no column holds, so active is not filtered on.
//!
//! Every statement is the same on SQLite and Postgres (D58) but the
//! longest hold the sweep reads from an instance's `maxHoldField`, JSON
//! access to its data ([`hold_sql`]), and the keyset by id, in byte order
//! on Postgres as on SQLite (`COLLATE "C"`).
//!
//! configChange: any config may change. Lease can be added to a schema that
//! has instances, which start free at token 0, and cannot be removed from
//! one: the leases and directives its instances hold would stay behind.

use std::collections::HashSet;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, Every, InstanceView, OperationCx, ReactionCx,
    Schedule, ScheduleCx, SchemaCx, Scope, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, number_value, stringify};
use superschematic_engine::storage::{Dialect, Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, EngineEvent, Error, GuardRequest, Hooks,
    InstanceChange, Result, SchemaIssue,
};
use superschematic_engine_behaviors::{
    is_terminal_state, link_pin, moved_on, target_move, LinkPin, TargetMove,
};

use crate::moves::{self, StatusMove};
use crate::time::iso;

/// How long a lease lasts after its acquire or its last heartbeat when the
/// config gives no ttlMs (`DEFAULT_TTL_MS`).
pub const DEFAULT_TTL_MS: i64 = 60_000;

/// How often the runner expires lapsed leases when the config gives no
/// sweepMs.
pub const DEFAULT_SWEEP_MS: i64 = 5000;

/// The most leases one run of the expire schedule expires; the rest wait
/// for the next.
pub const MAX_SWEEP: i64 = 1000;

/// The instances one query of the sweep, of expireHolder or of a reaction
/// reads.
const BATCH: i64 = 100;

const NAME: &str = "Lease";

/// A move of the status a lease's end makes.
pub type LeaseTransition = StatusMove;

/// A directive the runner sends the holder of an instance's active lease
/// when the target of one of its links moves on.
#[derive(Clone, Debug, PartialEq)]
pub struct LeaseDirectOn {
    /// The link of the type's Links whose target's move sends it.
    pub link: String,
    /// The link's schema, as the type's Links config gives it.
    pub schema: String,
    /// What the link pins: a move of that kind sends it only to the
    /// instances the target moved past.
    pub pin: Option<LinkPin>,
    /// The directive's name.
    pub name: String,
    /// Its data, beside the revised member the runner adds.
    pub data: Option<Map<String, Value>>,
}

/// Lease's config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct LeaseConfig {
    pub ttl_ms: i64,
    pub heartbeat_ms: i64,
    pub sweep_ms: i64,
    pub max_hold_ms: Option<i64>,
    pub max_hold_field: Option<String>,
    pub on_expiry: Option<LeaseTransition>,
    pub max_expiries: Option<i64>,
    pub escalate: Option<LeaseTransition>,
    /// The writing operations other principals may run while a lease is
    /// active, as `<Behavior>.<operation>`.
    pub exempt: Vec<String>,
    /// Whether every write under an active lease presents the current
    /// token.
    pub require_token: bool,
    pub acquire_permission: Option<String>,
    pub override_permission: Option<String>,
    pub direct_permission: Option<String>,
    /// The directives the runner sends the holder when a link's target
    /// moves on, one per link.
    pub direct_on: Vec<LeaseDirectOn>,
}

/// The lease's own columns on an instance.
struct Held {
    holder: Option<String>,
    token: i64,
    acquired_at: Option<i64>,
    /// Its acquire or its last heartbeat; `None` for a lease held before
    /// the column was added, which reads acquired_at.
    renewed_at: Option<i64>,
    expires_at: Option<i64>,
    /// The length the lease was acquired with, which each heartbeat renews
    /// it by.
    ttl_ms: Option<i64>,
    expiries: i64,
    ended_reason: Option<String>,
    ended_at: Option<i64>,
}

// key is the behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn held(view: &InstanceView<'_, LeaseConfig>) -> Result<Held> {
    let columns = view.columns().get()?;
    Ok(Held {
        holder: columns.opt_text("holder")?.map(str::to_owned),
        token: columns.i64("token")?,
        acquired_at: columns.opt_i64("acquired_at")?,
        renewed_at: columns.opt_i64("renewed_at")?,
        expires_at: columns.opt_i64("expires_at")?,
        ttl_ms: columns.opt_i64("ttl_ms")?,
        expiries: columns.i64("expiries")?,
        ended_reason: columns.opt_text("ended_reason")?.map(str::to_owned),
        ended_at: columns.opt_i64("ended_at")?,
    })
}

// hold_limit is the longest the instance's lease may be held after its
// acquire: the instance's maxHoldField when it holds a positive integer,
// else maxHoldMs; `None` for no limit.
fn hold_limit(view: &InstanceView<'_, LeaseConfig>) -> Result<Option<i64>> {
    let config = view.config();
    if let Some(field) = &config.max_hold_field {
        if let Some(Value::Number(value)) = view.data()?.get(field) {
            let value = number_value(value);
            if value.fract() == 0.0 && value.abs() <= 9_007_199_254_740_991.0 && value > 0.0 {
                return Ok(Some(value as i64));
            }
        }
    }
    Ok(config.max_hold_ms)
}

// deadline is when a held lease stops being active.
fn deadline(view: &InstanceView<'_, LeaseConfig>, lease: &Held) -> Result<i64> {
    let expires = lease.expires_at.unwrap_or(0);
    Ok(match hold_limit(view)? {
        None => expires,
        Some(limit) => expires.min(lease.acquired_at.unwrap_or(0) + limit),
    })
}

/// is_active is the one rule for whether a lease holds: held, before its
/// expiry time and within its longest hold.
fn is_active(view: &InstanceView<'_, LeaseConfig>, lease: &Held) -> Result<bool> {
    Ok(lease.holder.is_some() && view.now() < deadline(view, lease)?)
}

// lapse_reason is why a held lease stopped being active: its longest hold
// when that came no later than its expiry time, else its expiry time.
fn lapse_reason(view: &InstanceView<'_, LeaseConfig>, lease: &Held) -> Result<&'static str> {
    Ok(match hold_limit(view)? {
        Some(limit) if lease.acquired_at.unwrap_or(0) + limit <= lease.expires_at.unwrap_or(0) => {
            "maxHold"
        }
        _ => "ttl",
    })
}

fn details(value: Value) -> Option<Map<String, Value>> {
    match value {
        Value::Object(members) => Some(members),
        _ => None,
    }
}

// said is a veto with a code the declaration lists.
fn said(reason: impl Into<String>, code: &str, extra: Option<Value>) -> Veto {
    Veto {
        reason: reason.into(),
        code: Some(code.to_owned()),
        details: extra.and_then(details),
    }
}

// vetoed is the refusal of one of Lease's operations.
fn vetoed<C>(view: &InstanceView<'_, C>, operation: &str, veto: Veto) -> Error {
    Error::veto(NAME, operation, view.schema(), view.id(), veto)
}

// presented is the token a request presents as Lease's precondition.
fn presented(request: &GuardRequest<'_>) -> Option<i64> {
    match request.precondition()?.get("token")? {
        Value::Number(token) => Some(number_value(token) as i64),
        _ => None,
    }
}

fn stale(token: i64, lease: &Held) -> Veto {
    said(
        format!(
            "token {token} is stale: the lease is at token {}",
            lease.token
        ),
        "token_stale",
        Some(json!({ "token": token, "current": lease.token })),
    )
}

// lapsed is the veto of a call that needs an active lease, its reason
// written for the time the lease lapsed.
fn lapsed(
    view: &InstanceView<'_, LeaseConfig>,
    lease: &Held,
    reason: impl Fn(&str) -> String,
) -> Result<Veto> {
    let at = deadline(view, lease)?;
    Ok(said(
        reason(&iso(at)),
        "lapsed",
        Some(json!({ "expiredAt": at })),
    ))
}

fn forbidden<C>(view: &InstanceView<'_, C>, what: &str, permission: &str) -> Error {
    EngineError::new(
        EngineErrorCode::Forbidden,
        format!(
            "{} may not {what} {} {}: it needs permission {permission}",
            view.principal().subject,
            view.schema(),
            view.id()
        ),
    )
    .into()
}

fn overrides(scope: &Scope<'_, LeaseConfig>) -> Result<bool> {
    match &scope.config().override_permission {
        Some(permission) => scope.can(permission),
        None => Ok(false),
    }
}

// heartbeat_for is the heartbeat interval of a lease acquired for ttl: the
// config's, scaled to a shorter ttl, in JavaScript's arithmetic.
fn heartbeat_for(config: &LeaseConfig, ttl: i64) -> i64 {
    if ttl == config.ttl_ms {
        return config.heartbeat_ms;
    }
    let scaled = (config.heartbeat_ms as f64 * ttl as f64) / config.ttl_ms as f64;
    (scaled.floor() as i64).max(1)
}

/// The instance's Workflow config and status, read as the caller; `None`
/// without Workflow.
struct Status {
    flow: Value,
    status: String,
}

fn status_of(view: &InstanceView<'_, LeaseConfig>) -> Result<Option<Status>> {
    let Some(flow) = view.schemas().config(view.schema(), "Workflow")? else {
        return Ok(None);
    };
    Ok(moves::status_of(view)?.map(|status| Status { flow, status }))
}

// counts reports whether an expiry counts: unless the instance is in a
// terminal state of its Workflow, where its work is finished.
fn counts(status: Option<&Status>) -> bool {
    status.is_none_or(|status| !is_terminal_state(&status.flow, &status.status))
}

// end clears the holder and advances the token, recording how the lease
// ended, and deletes the directives of the lease it ends, so none of them
// reaches the next holder.
fn end(cx: &OperationCx<'_, LeaseConfig>, lease: &Held, expiries: i64, ended: &str) -> Result<()> {
    cx.columns().set(&[
        ("holder", SqlValue::Null),
        ("token", SqlValue::Integer(lease.token + 1)),
        ("acquired_at", SqlValue::Null),
        ("renewed_at", SqlValue::Null),
        ("expires_at", SqlValue::Null),
        ("ttl_ms", SqlValue::Null),
        ("expiries", SqlValue::Integer(expiries)),
        ("ended_reason", SqlValue::from(ended)),
        ("ended_at", SqlValue::Integer(cx.now())),
    ])?;
    let mut params = key(cx);
    params.push(SqlValue::Integer(lease.token));
    cx.sql().run(
        format!(
            "DELETE FROM {} WHERE namespace = ? AND schema = ? AND id = ? AND token = ?",
            cx.sql().table("directives")?
        ),
        &params,
    )?;
    Ok(())
}

// move_status moves the status through Workflow's transition when it is
// one of the rule's from states. A guard's veto leaves it as it is.
fn move_status(
    cx: &OperationCx<'_, LeaseConfig>,
    rule: Option<&LeaseTransition>,
    status: Option<&Status>,
) -> Result<()> {
    moves::apply(cx, rule, status.map(|status| status.status.as_str()))
}

// apply_expiry ends a lease as an expiry: the one code expire, acquire and
// an abandon share.
fn apply_expiry(cx: &OperationCx<'_, LeaseConfig>, lease: &Held, ended: &str) -> Result<()> {
    let config = cx.config();
    let status = status_of(cx)?;
    let counted = counts(status.as_ref());
    let expiries = lease.expiries + i64::from(counted);
    end(cx, lease, expiries, ended)?;
    let capped = counted && config.max_expiries.is_some_and(|max| expiries >= max);
    let rule = match &config.escalate {
        Some(escalate) if capped => Some(escalate),
        _ => config.on_expiry.as_ref(),
    };
    move_status(cx, rule, status.as_ref())
}

// holding holds heartbeat and acknowledge to the holder of an active
// lease, presenting its token; the guard has refused a stale one.
fn holding(
    view: &InstanceView<'_, LeaseConfig>,
    lease: &Held,
    token: Option<i64>,
) -> Result<Option<Veto>> {
    let Some(holder) = &lease.holder else {
        return Ok(Some(said("it is not leased", "not_leased", None)));
    };
    if *holder != view.principal().subject {
        return Ok(Some(said(
            "another principal holds its lease",
            "not_holder",
            None,
        )));
    }
    if token.is_none() {
        return Ok(Some(said(
            "the holder presents its token as Lease's precondition",
            "token_required",
            None,
        )));
    }
    if !is_active(view, lease)? {
        return lapsed(view, lease, |at| {
            format!("its lease expired at {at}; expire applies the expiry, and acquire takes a new lease")
        })
        .map(Some);
    }
    Ok(None)
}

// releasing holds release to the holder of an active lease, presenting its
// token, or a principal with overridePermission, who need not.
fn releasing(
    view: &InstanceView<'_, LeaseConfig>,
    lease: &Held,
    token: Option<i64>,
) -> Result<Option<Veto>> {
    let Some(holder) = &lease.holder else {
        return Ok(Some(said("it is not leased", "not_leased", None)));
    };
    let config = view.config();
    let overridden = overrides(view)?;
    if *holder != view.principal().subject && !overridden {
        if let Some(permission) = &config.override_permission {
            return Err(forbidden(
                view,
                "release another principal's lease of",
                permission,
            ));
        }
        return Ok(Some(said(
            "another principal holds its lease, and only the holder may release it",
            "not_holder",
            None,
        )));
    }
    if token.is_none() && !overridden {
        return Ok(Some(said(
            "the holder releases its lease presenting its token as Lease's precondition",
            "token_required",
            None,
        )));
    }
    if !is_active(view, lease)? {
        return lapsed(view, lease, |at| {
            format!("its lease expired at {at}; expire applies the expiry")
        })
        .map(Some);
    }
    Ok(None)
}

// direct_guard is who may send a directive: a principal with
// directPermission, or overridePermission when that is absent. A
// behavior's call() is not asked (the guard passes it).
fn direct_guard(view: &InstanceView<'_, LeaseConfig>) -> Result<Option<Veto>> {
    let config = view.config();
    let Some(permission) = config
        .direct_permission
        .as_ref()
        .or(config.override_permission.as_ref())
    else {
        return Ok(Some(said(
            "its config names no permission that sends directives (directPermission or overridePermission)",
            "not_configured",
            None,
        )));
    };
    if !view.can(permission)? {
        return Err(forbidden(
            view,
            "send a directive to the holder of",
            permission,
        ));
    }
    Ok(None)
}

// check_guarded is a handler's own check that the guard let only the
// holder of an active lease through: anything else is a defect.
fn check_guarded(cx: &OperationCx<'_, LeaseConfig>, operation: &str, lease: &Held) -> Result<()> {
    if lease.holder.as_deref() != Some(cx.principal().subject.as_str()) || !is_active(cx, lease)? {
        return Err(Error::behavior(
            NAME,
            format!("{operation} reached its handler without its guard"),
        ));
    }
    Ok(())
}

// require_override holds a call to a principal with overridePermission,
// and refuses it outright when the config names none.
fn require_override(scope: &Scope<'_, LeaseConfig>, what: &str) -> Result<()> {
    let subject = &scope.principal().subject;
    let refused =
        |message: String| -> Error { EngineError::new(EngineErrorCode::Forbidden, message).into() };
    match &scope.config().override_permission {
        None => Err(refused(format!(
            "{subject} may not {what} {}: its Lease config names no overridePermission",
            scope.schema()
        ))),
        Some(permission) if !scope.can(permission)? => Err(refused(format!(
            "{subject} may not {what} {}: it needs permission {permission}",
            scope.schema()
        ))),
        Some(_) => Ok(()),
    }
}

fn directive_of(row: &Row) -> Result<Value> {
    let mut out = Map::new();
    out.insert("id".into(), Value::from(row.i64("directive")?));
    out.insert("name".into(), Value::from(row.text("name")?));
    if let Some(data) = row.opt_text("data")? {
        let data: Value = serde_json::from_str(data).map_err(|error| {
            Error::behavior(NAME, format!("a directive's data is no JSON: {error}"))
        })?;
        out.insert("data".into(), data);
    }
    if let Some(dedupe_key) = row.opt_text("dedupe_key")? {
        out.insert("dedupeKey".into(), Value::from(dedupe_key));
    }
    out.insert("createdAt".into(), Value::from(row.i64("created_at")?));
    out.insert("createdBy".into(), Value::from(row.text("created_by")?));
    Ok(Value::Object(out))
}

// id_order is the keyset by id, in byte order on both databases: SQLite
// compares text by its bytes, Postgres by its collation unless told.
fn id_order(dialect: Dialect) -> &'static str {
    match dialect {
        Dialect::Sqlite => "id",
        Dialect::Postgres => "id COLLATE \"C\"",
    }
}

// hold_sql is the longest hold as SQL over the relation's row, with its
// parameters: the instance's maxHoldField when it holds a positive
// integer, else maxHoldMs; `None` when the config gives neither.
fn hold_sql(config: &LeaseConfig, dialect: Dialect) -> Option<(String, Vec<SqlValue>)> {
    let fallback = config.max_hold_ms.map_or(SqlValue::Null, SqlValue::Integer);
    let Some(field) = &config.max_hold_field else {
        return config
            .max_hold_ms
            .map(|ms| ("?".to_owned(), vec![SqlValue::Integer(ms)]));
    };
    Some(match dialect {
        Dialect::Sqlite => {
            let path = SqlValue::from(format!("$.\"{field}\""));
            (
                "COALESCE(CASE WHEN json_type(data, ?) = 'integer' AND json_extract(data, ?) > 0 THEN json_extract(data, ?) END, ?)".to_owned(),
                vec![path.clone(), path.clone(), path, fallback],
            )
        }
        // The instance's data is text: its member as JSON, an integer as
        // its number has no fraction.
        Dialect::Postgres => {
            let name = SqlValue::from(field.as_str());
            (
                "COALESCE(CASE WHEN jsonb_typeof((data::jsonb)->(?::text)) = 'number' AND ((data::jsonb)->>(?::text))::numeric > 0 AND scale(((data::jsonb)->>(?::text))::numeric) = 0 THEN ((data::jsonb)->>(?::text))::numeric END, ?::bigint)".to_owned(),
                vec![name.clone(), name.clone(), name.clone(), name, fallback],
            )
        }
    })
}

// expire_one invokes expire on one instance for a sweep or expireHolder,
// and returns why it expired, or `None`. A guard's veto leaves that
// instance for the next sweep and the others go on; any other failure, a
// principal the policy refuses say, fails the run, so the deployment sees
// it.
fn expire_one(scope: &Scope<'_, LeaseConfig>, id: &str, params: &Value) -> Result<Option<String>> {
    match scope
        .instances()
        .invoke(scope.schema(), id, "expire", params, None)
    {
        Ok(result) => Ok(result
            .get("reason")
            .and_then(Value::as_str)
            .map(str::to_owned)),
        Err(error) if error.is_veto() => Ok(None),
        Err(error) => Err(error),
    }
}

// acknowledge_directives marks directives of the lease handled, for
// acknowledge and heartbeat's acknowledge, and returns how many it marked:
// none when each was handled already. An id not sent under its token
// refuses the call.
fn acknowledge_directives(
    cx: &OperationCx<'_, LeaseConfig>,
    operation: &str,
    path: &str,
    lease: &Held,
    ids: &[i64],
) -> Result<u64> {
    let table = cx.sql().table("directives")?;
    let marks = vec!["?"; ids.len()].join(", ");
    let mut params = key(cx);
    params.push(SqlValue::Integer(lease.token));
    params.extend(ids.iter().map(|id| SqlValue::Integer(*id)));
    let found: HashSet<i64> = cx
        .sql()
        .all(
            format!("SELECT directive FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND directive IN ({marks})"),
            &params,
        )?
        .iter()
        .map(|row| row.i64("directive"))
        .collect::<std::result::Result<_, _>>()?;
    let missing: Vec<String> = ids
        .iter()
        .filter(|id| !found.contains(id))
        .map(i64::to_string)
        .collect();
    if !missing.is_empty() {
        return Err(EngineError::operation_params(
            NAME,
            operation,
            vec![SchemaIssue::new(
                path,
                format!(
                    "no directive {} was sent under token {}",
                    missing.join(", "),
                    lease.token
                ),
            )],
        )
        .into());
    }
    let mut params = vec![SqlValue::Integer(cx.now())];
    params.extend(key(cx));
    params.push(SqlValue::Integer(lease.token));
    params.extend(ids.iter().map(|id| SqlValue::Integer(*id)));
    Ok(cx
        .sql()
        .run(
            format!("UPDATE {table} SET acknowledged_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND directive IN ({marks}) AND acknowledged_at IS NULL"),
            &params,
        )?
        .changes)
}

// direct_key is the dedupe key of a directOn directive: one per lease for
// each move of a target of the link, by the target's id, a revision by its
// number and a release by the release pointer's version it made.
fn direct_key(rule: &LeaseDirectOn, target: &str, moved: &TargetMove) -> String {
    match moved {
        TargetMove::Revision { revision } => format!("revised {} {target} {revision}", rule.link),
        TargetMove::Release { release, .. } => format!("released {} {target} {release}", rule.link),
    }
}

// held_among lists the ids, of the given ones, whose lease has a holder in
// Lease's column, a page of ids at a time: the instances a directive can
// reach, or that direct refuses as lapsed.
fn held_among(cx: &ReactionCx<'_, LeaseConfig>, ids: &[String]) -> Result<Vec<String>> {
    let relation = cx.sql().instances()?;
    let mut kept: HashSet<String> = HashSet::new();
    for page in ids.chunks(BATCH as usize) {
        let marks = vec!["?"; page.len()].join(", ");
        let params: Vec<SqlValue> = page.iter().map(SqlValue::from).collect();
        for row in cx.sql().all(
            format!("SELECT id FROM {relation} WHERE holder IS NOT NULL AND id IN ({marks})"),
            &params,
        )? {
            kept.insert(row.text("id")?.to_owned());
        }
    }
    Ok(ids
        .iter()
        .filter(|id| kept.contains(*id))
        .cloned()
        .collect())
}

fn integer(params: &Value, name: &str) -> Option<i64> {
    match params.get(name)? {
        Value::Number(number) => Some(number_value(number) as i64),
        _ => None,
    }
}

/// Lease: an exclusive, time-bounded lease on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Lease;

impl Behavior for Lease {
    type Config = LeaseConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Lease.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its fields, its Workflow's states and the behaviors it lists.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<LeaseConfig> {
        let number = |name: &str| integer(raw, name);
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        let ttl_ms = number("ttlMs").unwrap_or(DEFAULT_TTL_MS);
        let heartbeat_ms = number("heartbeatMs").unwrap_or(ttl_ms.div_euclid(3));
        if heartbeat_ms * 2 > ttl_ms {
            return Err(Error::config(format!(
                "heartbeatMs ({heartbeat_ms}) is more than half of ttlMs ({ttl_ms}): one late heartbeat would lose the lease"
            )));
        }
        let max_hold_field = text("maxHoldField");
        if let Some(field) = &max_hold_field {
            check_integer_field(target, field)?;
        }
        let on_expiry = raw.get("onExpiry").map(StatusMove::parse);
        let escalate = raw.get("escalate").map(StatusMove::parse);
        moves::check_moves(
            target,
            &[
                ("onExpiry", on_expiry.as_ref()),
                ("escalate", escalate.as_ref()),
            ],
        )?;
        let max_expiries = number("maxExpiries");
        if escalate.is_some() && max_expiries.is_none() {
            return Err(Error::config(
                "escalate applies at the expiry that reaches maxExpiries, which the config does not give",
            ));
        }
        let exempt = moves::strings(raw.get("exempt"));
        for entry in &exempt {
            let behavior = &entry[..entry.rfind('.').unwrap_or(0)];
            if behavior == NAME {
                return Err(Error::config(format!(
                    "exempt names {entry}: Lease's own operations check their callers themselves"
                )));
            }
            if !target.behaviors().iter().any(|name| name == behavior) {
                return Err(Error::config(format!(
                    "exempt names {entry}, but the type does not list {behavior} (it lists {})",
                    target.behaviors().join(", ")
                )));
            }
        }
        let direct_permission = text("directPermission");
        let override_permission = text("overridePermission");
        let direct_on = check_direct_on(
            raw,
            target,
            direct_permission.is_some() || override_permission.is_some(),
        )?;
        Ok(LeaseConfig {
            ttl_ms,
            heartbeat_ms,
            sweep_ms: number("sweepMs").unwrap_or(DEFAULT_SWEEP_MS),
            max_hold_ms: number("maxHoldMs"),
            max_hold_field,
            on_expiry,
            max_expiries,
            escalate,
            exempt,
            require_token: raw.get("requireToken") == Some(&Value::Bool(true)),
            acquire_permission: text("acquirePermission"),
            override_permission,
            direct_permission,
            direct_on,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the leases and directives its instances hold would stay behind".to_owned()
                }))
            }),
            guidance: Some(|_, config, target| crate::guidance::lease::guidance(config, target)),
            migrations: migrations(),
            filters: vec![(
                "holder",
                BehaviorFilter::new("holder", "string").described(
                    "The principal that holds its lease, a lapsed one included until its expiry is applied; null for a free instance.",
                ),
            )],
            guard: Some(guard),
            operations: vec![
                ("acquire", acquire),
                ("heartbeat", heartbeat),
                ("release", release),
                ("expire", expire),
                ("direct", direct),
                ("acknowledge", acknowledge),
                ("resetExpiries", reset_expiries),
            ],
            schema_operations: vec![("expireHolder", expire_holder)],
            schedules: vec![(
                "expire",
                Schedule {
                    every: Every::Config(|_, config: &LeaseConfig| Ok(Some(config.sweep_ms))),
                    run: sweep,
                },
            )],
            reactions: Some(superschematic_engine::behaviors::Reactions {
                watches: Some(|_, config: &LeaseConfig, _| {
                    if config.direct_on.is_empty() {
                        return Ok(None);
                    }
                    let mut schemas: Vec<String> = Vec::new();
                    for rule in &config.direct_on {
                        if !schemas.contains(&rule.schema) {
                            schemas.push(rule.schema.clone());
                        }
                    }
                    Ok(Some(schemas))
                }),
                react,
            }),
            // Each field reads the lease's columns; a free instance has no
            // holder, acquiredAt, renewedAt or expiresAt, and a held one no
            // ended.
            fields: vec![
                ("holder", |_, view| Ok(held(view)?.holder.map(Value::from))),
                ("token", |_, view| Ok(Some(Value::from(held(view)?.token)))),
                ("acquiredAt", |_, view| {
                    Ok(held(view)?.acquired_at.map(Value::from))
                }),
                ("renewedAt", |_, view| {
                    let lease = held(view)?;
                    Ok(lease
                        .holder
                        .as_ref()
                        .and(lease.renewed_at.or(lease.acquired_at))
                        .map(Value::from))
                }),
                ("expiresAt", |_, view| {
                    let lease = held(view)?;
                    Ok(match lease.holder {
                        None => None,
                        Some(_) => Some(Value::from(deadline(view, &lease)?)),
                    })
                }),
                ("active", |_, view| {
                    Ok(Some(Value::Bool(is_active(view, &held(view)?)?)))
                }),
                ("expiries", |_, view| {
                    Ok(Some(Value::from(held(view)?.expiries)))
                }),
                ("ended", |_, view| {
                    let lease = held(view)?;
                    Ok(lease.ended_reason.map(|reason| {
                        json!({ "reason": reason, "at": lease.ended_at })
                    }))
                }),
            ],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    cx.sql().run(
                        format!(
                            "DELETE FROM {} WHERE namespace = ? AND schema = ? AND id = ?",
                            cx.sql().table("directives")?
                        ),
                        &key(cx),
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn migrations() -> Vec<BehaviorMigration> {
    let text = || ColumnSpec::text().not_null();
    let integer = || ColumnSpec::integer().not_null();
    vec![
        BehaviorMigration::new(1, "lease")
            .column("holder", ColumnSpec::text())
            .column("token", integer().default(0))
            .column("acquired_at", ColumnSpec::integer())
            .column("expires_at", ColumnSpec::integer())
            .column("ttl_ms", ColumnSpec::integer())
            .column("expiries", integer().default(0))
            // The sweep reads the held leases, and expireHolder one
            // holder's.
            .index("held", &["holder", "expires_at"])
            .table(
                TableSpec::new("directives")
                    .column("namespace", text())
                    .column("schema", text())
                    .column("id", text())
                    .column("token", integer())
                    .column("directive", integer())
                    .column("name", text())
                    .column("data", ColumnSpec::text())
                    .column("created_by", text())
                    .column("created_at", integer())
                    .column("acknowledged_at", ColumnSpec::integer())
                    .primary_key(&["namespace", "schema", "id", "token", "directive"]),
            ),
        // How the last lease ended, which its event carries.
        BehaviorMigration::new(2, "ended")
            .column("ended_reason", ColumnSpec::text())
            .column("ended_at", ColumnSpec::integer()),
        // When the holder last renewed it, which notRenewedAfter reads, and
        // the key that sends a directive once per lease.
        BehaviorMigration::new(3, "renewed")
            .column("renewed_at", ColumnSpec::integer())
            .up(|sql| {
                sql.run(
                    format!(
                        "ALTER TABLE {} ADD COLUMN dedupe_key {}",
                        sql.table("directives")?,
                        ColumnSpec::text().definition(sql.dialect())?
                    ),
                    &[],
                )?;
                Ok(())
            }),
        // The holder alone, which a list that filters on Lease.holder reads
        // in creation order; held, which leads with it, orders by
        // expires_at.
        BehaviorMigration::new(4, "holder index").index("holder", &["holder"]),
    ]
}

// check_integer_field holds maxHoldField to an integer field of the type.
fn check_integer_field(target: &ConfigTarget<'_>, field: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "maxHoldField \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    if !moves::only_types(target, field, "integer") {
        return Err(Error::config(format!(
            "maxHoldField \"{field}\" is not an integer field of {}",
            target.type_name()
        )));
    }
    Ok(())
}

// check_direct_on holds directOn to the type's Links, one entry per link,
// and to a permission that sends directives, which the runner's principal
// needs, and returns each entry with its link's schema.
fn check_direct_on(
    raw: &Value,
    target: &ConfigTarget<'_>,
    permitted: bool,
) -> Result<Vec<LeaseDirectOn>> {
    let entries = raw
        .get("directOn")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    if entries.is_empty() {
        return Ok(Vec::new());
    }
    if !target.behaviors().iter().any(|name| name == "Links") {
        return Err(Error::config(
            "directOn names links of Links, which the type does not list",
        ));
    }
    if !permitted {
        return Err(Error::config(
            "directOn sends directives as the runner's principal, which needs directPermission, or overridePermission when that is absent; the config names neither",
        ));
    }
    let links = target
        .configs()
        .get("Links")
        .and_then(|links| links.get("links"));
    let mut seen: HashSet<String> = HashSet::new();
    let mut checked = Vec::new();
    for (index, entry) in entries.iter().enumerate() {
        let at = format!("directOn[{index}]");
        let name = entry
            .get("revised")
            .and_then(|revised| revised.get("link"))
            .and_then(Value::as_str)
            .unwrap_or_default();
        if !seen.insert(name.to_owned()) {
            return Err(Error::config(format!(
                "{at} names link {name} again: one directive per link"
            )));
        }
        let data = entry.get("data").and_then(Value::as_object);
        if data.is_some_and(|data| data.contains_key("revised")) {
            return Err(Error::config(format!(
                "{at}.data holds revised, the member the runner sets to the link's move"
            )));
        }
        // A Links config of the wrong shape is Links' to refuse.
        let Some(links) = links.and_then(Value::as_object) else {
            continue;
        };
        let Some(link) = links.get(name) else {
            return Err(Error::config(format!(
                "{at}.revised names link {name}, which is not a link of the type's Links ({})",
                js_keys(links).join(", ")
            )));
        };
        checked.push(LeaseDirectOn {
            link: name.to_owned(),
            schema: match link.get("schema") {
                Some(Value::String(schema)) => schema.clone(),
                Some(other) => stringify(other),
                None => "undefined".to_owned(),
            },
            pin: link_pin(link),
            name: entry
                .get("name")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            data: data.cloned(),
        });
    }
    Ok(checked)
}

// guard is the lease's exclusion and its fence: see the module's header.
// A new instance holds no lease, so a create has nothing to exclude or
// fence.
fn guard(
    _: &Lease,
    view: &InstanceView<'_, LeaseConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    if matches!(request, GuardRequest::Create { .. }) {
        return Ok(None);
    }
    let config = view.config();
    let lease = held(view)?;
    let writes = match request {
        GuardRequest::Operation { writes, .. } => *writes,
        _ => true,
    };
    let token = presented(request);
    if let Some(token) = token.filter(|token| writes && *token != lease.token) {
        return Ok(Some(stale(token, &lease)));
    }
    if let GuardRequest::Operation {
        behavior: NAME,
        operation,
        caller,
        ..
    } = request
    {
        return match *operation {
            "direct" if caller.is_none() => direct_guard(view),
            "heartbeat" | "acknowledge" => holding(view, &lease, token),
            "release" => releasing(view, &lease, token),
            _ => Ok(None),
        };
    }
    let refresh = matches!(
        request,
        GuardRequest::Operation {
            behavior: "Queue",
            operation: "refresh",
            ..
        }
    );
    let Some(holder) = &lease.holder else {
        return Ok(None);
    };
    if !writes || refresh || overrides(view)? {
        return Ok(None);
    }
    if let (GuardRequest::Update { patch, .. }, Some(field)) = (request, &config.max_hold_field) {
        if patch.contains_key(field) {
            return Ok(Some(said(
                format!("{field} limits how long its lease may be held, so it cannot change while the lease is held"),
                "hold_limit_fixed",
                None,
            )));
        }
    }
    if !matches!(request, GuardRequest::Delete { .. }) && request.caller().is_some() {
        return Ok(None);
    }
    if let GuardRequest::Operation {
        behavior,
        operation,
        ..
    } = request
    {
        let named = format!("{behavior}.{operation}");
        if config.exempt.contains(&named) {
            return Ok(None);
        }
    }
    let mine = *holder == view.principal().subject;
    if is_active(view, &lease)? {
        if !mine {
            let until = deadline(view, &lease)?;
            return Ok(Some(said(
                format!("another principal holds its lease, until {}", iso(until)),
                "held_by_another",
                Some(json!({ "expiresAt": until })),
            )));
        }
        return Ok((config.require_token && token.is_none()).then(|| {
            said(
                "its config requires every write under the lease to present the lease's token as Lease's precondition",
                "token_required",
                None,
            )
        }));
    }
    if mine {
        return lapsed(view, &lease, |at| {
            format!("the caller's lease expired at {at}, so it no longer holds the instance")
        })
        .map(Some);
    }
    Ok(None)
}

fn acquire(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let config = cx.config();
    if let Some(permission) = &config.acquire_permission {
        if !cx.can(permission)? {
            return Err(forbidden(cx, "acquire the lease of", permission));
        }
    }
    let ttl = integer(params, "ttlMs").unwrap_or(config.ttl_ms);
    if ttl > config.ttl_ms {
        return Err(EngineError::operation_params(
            NAME,
            "acquire",
            vec![SchemaIssue::new(
                "/ttlMs",
                format!("{ttl} is longer than the config's ttlMs, {}", config.ttl_ms),
            )],
        )
        .into());
    }
    let mut lease = held(cx)?;
    if is_active(cx, &lease)? {
        let until = deadline(cx, &lease)?;
        let at = iso(until);
        let expires = Some(json!({ "expiresAt": until }));
        return Err(
            if lease.holder.as_deref() == Some(cx.principal().subject.as_str()) {
                vetoed(
                    cx,
                    "acquire",
                    said(
                        format!(
                            "the caller already holds its lease, until {at}; heartbeat renews it"
                        ),
                        "held_by_caller",
                        expires,
                    ),
                )
            } else {
                vetoed(
                    cx,
                    "acquire",
                    said(
                        format!("another principal holds its lease, until {at}"),
                        "held_by_another",
                        expires,
                    ),
                )
            },
        );
    }
    let lapsed = lease.holder.is_some();
    if let Some(max) = config.max_expiries {
        let counted = lapsed && counts(status_of(cx)?.as_ref());
        let expiries = lease.expiries + i64::from(counted);
        if expiries >= max {
            return Err(vetoed(
                cx,
                "acquire",
                said(
                    format!("its lease has expired {expiries} times, the most its config allows; resetExpiries lets it be leased again"),
                    "max_expiries",
                    Some(json!({ "expiries": expiries, "maxExpiries": max })),
                ),
            ));
        }
    }
    if lapsed {
        let reason = lapse_reason(cx, &lease)?;
        apply_expiry(cx, &lease, reason)?;
        lease = held(cx)?;
    }
    let token = lease.token + 1;
    let now = cx.now();
    let expires_at = match hold_limit(cx)? {
        None => now + ttl,
        Some(limit) => now + ttl.min(limit),
    };
    cx.columns().set(&[
        ("holder", SqlValue::from(cx.principal().subject.as_str())),
        ("token", SqlValue::Integer(token)),
        ("acquired_at", SqlValue::Integer(now)),
        ("renewed_at", SqlValue::Integer(now)),
        ("expires_at", SqlValue::Integer(expires_at)),
        ("ttl_ms", SqlValue::Integer(ttl)),
        ("ended_reason", SqlValue::Null),
        ("ended_at", SqlValue::Null),
    ])?;
    Ok(
        json!({ "token": token, "expiresAt": expires_at, "heartbeatMs": heartbeat_for(config, ttl) }),
    )
}

// heartbeat: the guard holds it to the holder of an active lease, with its
// token. Its acknowledge marks directives handled in the same write.
fn heartbeat(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let lease = held(cx)?;
    check_guarded(cx, "heartbeat", &lease)?;
    let ids = ids_of(params.get("acknowledge"));
    if !ids.is_empty() {
        acknowledge_directives(cx, "heartbeat", "/acknowledge", &lease, &ids)?;
    }
    let ttl = lease.ttl_ms.unwrap_or(cx.config().ttl_ms);
    let now = cx.now();
    let expires_at = match hold_limit(cx)? {
        None => now + ttl,
        Some(limit) => (now + ttl).min(lease.acquired_at.unwrap_or(0) + limit),
    };
    cx.columns().set(&[
        ("expires_at", SqlValue::Integer(expires_at)),
        ("renewed_at", SqlValue::Integer(now)),
    ])?;
    let mut key = key(cx);
    key.push(SqlValue::Integer(lease.token));
    let directives = cx
        .sql()
        .all(
            format!(
                "SELECT directive, name, data, dedupe_key, created_by, created_at FROM {}
                 WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND acknowledged_at IS NULL ORDER BY directive",
                cx.sql().table("directives")?
            ),
            &key,
        )?
        .iter()
        .map(directive_of)
        .collect::<Result<Vec<_>>>()?;
    Ok(json!({ "expiresAt": expires_at, "directives": directives }))
}

fn ids_of(value: Option<&Value>) -> Vec<i64> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|id| match id {
            Value::Number(id) => Some(number_value(id) as i64),
            _ => None,
        })
        .collect()
}

// release: the guard holds it to the holder of an active lease, with its
// token, or a principal with overridePermission. An abandon is an expiry.
fn release(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let config = cx.config();
    let lease = held(cx)?;
    if lease.holder.is_none() || !is_active(cx, &lease)? {
        return Err(Error::behavior(
            NAME,
            "release reached its handler without its guard",
        ));
    }
    if params.get("abandon") == Some(&Value::Bool(true)) {
        apply_expiry(cx, &lease, "abandon")?;
        return Ok(json!({}));
    }
    let status = match &config.on_expiry {
        None => None,
        Some(_) => status_of(cx)?,
    };
    end(cx, &lease, lease.expiries, "release")?;
    move_status(cx, config.on_expiry.as_ref(), status.as_ref())?;
    Ok(json!({}))
}

fn expire(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let holder = params.get("holder").and_then(Value::as_str);
    let not_renewed_after = integer(params, "notRenewedAfter");
    if holder.is_none() && not_renewed_after.is_some() {
        return Err(EngineError::operation_params(
            NAME,
            "expire",
            vec![SchemaIssue::new(
                "/notRenewedAfter",
                "notRenewedAfter spares a holder's active leases, so it needs holder",
            )],
        )
        .into());
    }
    if holder.is_some() {
        require_override(cx, "expire the lease of another holder of")?;
    }
    let lease = held(cx)?;
    let active = is_active(cx, &lease)?;
    // A lease it does not expire is left as it was: no event, no seq.
    let spared = match (&lease.holder, holder) {
        (None, _) => true,
        (Some(_), None) => active,
        (Some(held), Some(holder)) => held != holder,
    };
    // Or its holder renewed it after the time: the process holding it is
    // alive.
    let renewed = active
        && not_renewed_after
            .is_some_and(|after| lease.renewed_at.or(lease.acquired_at).unwrap_or(0) > after);
    if spared || renewed {
        cx.unchanged();
        return Ok(json!({ "expired": false }));
    }
    let reason = if active {
        "holder"
    } else {
        lapse_reason(cx, &lease)?
    };
    apply_expiry(cx, &lease, reason)?;
    Ok(json!({ "expired": true, "reason": reason }))
}

// direct: who may send one is the guard's question (direct_guard).
fn direct(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let lease = held(cx)?;
    let reason = "no lease is active, so there is no holder to direct";
    if lease.holder.is_none() {
        return Err(vetoed(cx, "direct", said(reason, "not_leased", None)));
    }
    if !is_active(cx, &lease)? {
        let at = deadline(cx, &lease)?;
        return Err(vetoed(
            cx,
            "direct",
            said(reason, "lapsed", Some(json!({ "expiredAt": at }))),
        ));
    }
    let table = cx.sql().table("directives")?;
    let mut key = key(cx);
    key.push(SqlValue::Integer(lease.token));
    let dedupe_key = params.get("dedupeKey").and_then(Value::as_str);
    if let Some(dedupe_key) = dedupe_key {
        let mut params = key.clone();
        params.push(SqlValue::from(dedupe_key));
        if let Some(sent) = cx.sql().get(
            format!("SELECT directive FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND dedupe_key = ?"),
            &params,
        )? {
            // Sent already: the directive stands as it was sent.
            cx.unchanged();
            return Ok(json!({ "id": sent.i64("directive")?, "created": false }));
        }
    }
    let last = cx
        .sql()
        .get(
            format!("SELECT MAX(directive) AS last FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ?"),
            &key,
        )?
        .map(|row| row.opt_i64("last"))
        .transpose()?
        .flatten()
        .unwrap_or(0);
    let id = last + 1;
    let mut values = key;
    values.extend([
        SqlValue::Integer(id),
        SqlValue::from(
            params
                .get("name")
                .and_then(Value::as_str)
                .unwrap_or_default(),
        ),
        params
            .get("data")
            .map_or(SqlValue::Null, |data| SqlValue::from(stringify(data))),
        dedupe_key.map_or(SqlValue::Null, SqlValue::from),
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::Integer(cx.now()),
    ]);
    cx.sql().run(
        format!("INSERT INTO {table} (namespace, schema, id, token, directive, name, data, dedupe_key, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
        &values,
    )?;
    Ok(json!({ "id": id, "created": true }))
}

// acknowledge: the guard holds it to the holder of an active lease, with
// its token.
fn acknowledge(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    let lease = held(cx)?;
    check_guarded(cx, "acknowledge", &lease)?;
    // Every one acknowledged already: nothing changes.
    if acknowledge_directives(
        cx,
        "acknowledge",
        "/ids",
        &lease,
        &ids_of(params.get("ids")),
    )? == 0
    {
        cx.unchanged();
    }
    Ok(json!({}))
}

fn reset_expiries(_: &Lease, cx: &OperationCx<'_, LeaseConfig>, _: &Value) -> Result<Value> {
    let Some(permission) = &cx.config().override_permission else {
        return Err(vetoed(
            cx,
            "resetExpiries",
            said(
                "its config names no overridePermission, which resetting its expiries needs",
                "not_configured",
                None,
            ),
        ));
    };
    if !cx.can(permission)? {
        return Err(forbidden(cx, "reset the expiries of", permission));
    }
    let expiries = held(cx)?.expiries;
    if expiries == 0 {
        cx.unchanged();
        return Ok(json!({ "expiries": expiries }));
    }
    cx.columns().set(&[("expiries", SqlValue::Integer(0))])?;
    Ok(json!({ "expiries": expiries }))
}

// expire_holder: every lease one holder has on the schema, found through
// the index on the holder, expired by expire on each instance, so each
// expiry runs that instance's guards and appends its event.
fn expire_holder(_: &Lease, cx: &SchemaCx<'_, LeaseConfig>, params: &Value) -> Result<Value> {
    require_override(cx, "expire the leases of a holder on")?;
    let holder = params
        .get("holder")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let mut invoked = Map::new();
    invoked.insert("holder".into(), Value::from(holder));
    if let Some(after) = params.get("notRenewedAfter") {
        invoked.insert("notRenewedAfter".into(), after.clone());
    }
    let invoked = Value::Object(invoked);
    let relation = cx.sql().instances()?;
    let order = id_order(cx.dialect());
    let mut expired: Vec<String> = Vec::new();
    let mut reasons = Map::new();
    let mut after = String::new();
    loop {
        let ids: Vec<String> = cx
            .sql()
            .all(
                format!("SELECT id FROM {relation} WHERE holder = ? AND {order} > ? ORDER BY {order} LIMIT ?"),
                &[
                    SqlValue::from(holder),
                    SqlValue::from(after.as_str()),
                    SqlValue::Integer(BATCH),
                ],
            )?
            .iter()
            .map(|row| row.text("id").map(str::to_owned))
            .collect::<std::result::Result<_, _>>()?;
        for id in &ids {
            if let Some(reason) = expire_one(cx, id, &invoked)? {
                expired.push(id.clone());
                let count = reasons.get(&reason).and_then(Value::as_i64).unwrap_or(0);
                reasons.insert(reason, Value::from(count + 1));
            }
        }
        if (ids.len() as i64) < BATCH {
            return Ok(json!({ "expired": expired.len(), "reasons": reasons, "ids": expired }));
        }
        after = ids.last().cloned().unwrap_or_default();
    }
}

// sweep: every sweepMs, the runner expires the leases past their expiry
// time or their longest hold, at most MAX_SWEEP a run.
fn sweep(_: &Lease, cx: &ScheduleCx<'_, LeaseConfig>) -> Result<()> {
    let relation = cx.sql().instances()?;
    let order = id_order(cx.dialect());
    let hold = hold_sql(cx.config(), cx.dialect());
    let past = match &hold {
        None => String::new(),
        Some((sql, _)) => format!(" OR acquired_at + {sql} <= ?"),
    };
    let statement = format!(
        "SELECT id FROM {relation}
         WHERE holder IS NOT NULL AND {order} > ? AND (expires_at <= ?{past})
         ORDER BY {order} LIMIT ?"
    );
    let mut after = String::new();
    let mut swept = 0;
    while swept < MAX_SWEEP {
        let mut params = vec![SqlValue::from(after.as_str()), SqlValue::Integer(cx.now())];
        if let Some((_, hold)) = &hold {
            params.extend(hold.iter().cloned());
            params.push(SqlValue::Integer(cx.now()));
        }
        params.push(SqlValue::Integer(BATCH.min(MAX_SWEEP - swept)));
        let ids: Vec<String> = cx
            .sql()
            .all(Sql::same(statement.as_str()), &params)?
            .iter()
            .map(|row| row.text("id").map(str::to_owned))
            .collect::<std::result::Result<_, _>>()?;
        for id in &ids {
            expire_one(cx, id, &json!({}))?;
        }
        swept += ids.len() as i64;
        if (ids.len() as i64) < BATCH {
            return Ok(());
        }
        after = ids.last().cloned().unwrap_or_default();
    }
    Ok(())
}

// react: when a link's target moves on, the holder of each instance it
// moves on hears the entry's directive. Without directOn the reactions are
// off on the schema, so the runner reads none of its events.
fn react(_: &Lease, cx: &ReactionCx<'_, LeaseConfig>, event: &EngineEvent) -> Result<()> {
    for rule in &cx.config().direct_on {
        let Some(moved) = target_move(&**cx, NAME, "directOn", &rule.link, &rule.schema, event)?
        else {
            continue;
        };
        let target = event.instance_id.as_deref().unwrap_or_default();
        let revised = match &moved {
            TargetMove::Revision { revision } => json!({
                "link": rule.link, "schema": rule.schema, "id": target, "revision": revision
            }),
            TargetMove::Release { release, commit } => json!({
                "link": rule.link, "schema": rule.schema, "id": target, "release": release, "commit": commit
            }),
        };
        let mut data = rule.data.clone().unwrap_or_default();
        data.insert("revised".into(), revised);
        let params = json!({
            "name": rule.name,
            "data": data,
            "dedupeKey": direct_key(rule, target, &moved),
        });
        let reached = moved_on(&**cx, &rule.link, rule.pin, target, &moved)?;
        for id in held_among(cx, &reached)? {
            match cx
                .instances()
                .invoke(cx.schema(), &id, "direct", &params, None)
            {
                Ok(_) => {}
                // A lease that lapsed or ended since, or a guard's veto: the
                // instance has no holder to tell, and the next holder
                // starts from the target as it is.
                Err(error) if error.is_veto() => {}
                Err(error) => return Err(error),
            }
        }
    }
    Ok(())
}
