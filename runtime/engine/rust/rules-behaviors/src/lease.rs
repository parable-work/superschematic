//! Lease, an exclusive, time-bounded lease on a row (D16), ported onto the
//! pure core (D103) from the engine's
//! (`runtime/engine-workqueue/rust/src/lease.rs`), held by one principal
//! and numbered by a fencing token (D104). The token is the row's own
//! integer that starts at 0 and advances at every acquire, every release
//! and every applied expiry, so (row, token) names one lease. A caller
//! presents it as Lease's precondition (`preconditions: { Lease: { token }
//! }`, the command's, [`superschematic_rules::View::precondition`]), the
//! one way a write presents it: the guard refuses a write that presents
//! another token, whoever calls (`token_stale`), so a process of the
//! holder's principal that lost its lease cannot write once a sibling holds
//! a new one. A heartbeat, an acknowledgement and the holder's release need
//! the token; with `requireToken`, so does every other write while a lease
//! is active, the holder's own included. The token is no capability: its
//! column shows it to every reader, and the guard still checks who calls.
//! A request a behavior's code makes presents nothing (D16).
//!
//! Lease passes D103's test: its state is its owned columns (holder, token,
//! times, expiries and how the last lease ended), its `directives`
//! sub-table and the deadline its expiry arms; it has rules over how that
//! state changes (acquire, heartbeat, release, abandon, expire, directives
//! sent and acknowledged, every write fenced by the token); and it recurs,
//! as a session a device claims, a conversation one process owns, a message
//! an agent takes, a job a worker runs (D104).
//!
//! A lease is active while its holder holds a subject, before its expiry
//! time and within its longest hold since acquire (`maxHoldMs`, or the
//! row's own `maxHoldField` when it holds a positive whole number). A lease
//! past either has lapsed: it gives its holder nothing, so the holder's
//! heartbeat, release, acknowledgement and writes are refused, and only its
//! expiry can follow. `expire` applies the expiry, and anyone who may write
//! the row may call it; `acquire` applies it first over a lapsed lease. An
//! acquire or a heartbeat keeps `expiresAt` at the earlier of its ttl and
//! the longest hold, and a heartbeat renews by the length the last acquire
//! or heartbeat gave it (`expiresAt - renewedAt`), which is the engine's
//! renewal while the hold stays where it was: unless the hold capped the
//! last one, it is the acquire's ttl, and once the hold capped it every
//! later renewal is capped there too. No ttl column is kept, so a lease
//! whose hold is raised after it cut a renewal short renews by the
//! config's ttl from then on (below). An expiry clears the holder, advances
//! the token, counts the expiry unless the row is in a terminal state of
//! its Workflow, and moves the status with `onExpiry`, or `escalate` at the
//! expiry that reaches `maxExpiries`, only from the config's from states
//! and only through Workflow's transition (`Cx::call`), whose guards run; a
//! veto leaves the status as it is and the lease expired. Once the row has
//! had `maxExpiries` expiries it cannot be leased until a caller with
//! `overridePermission` resets them. A release applies `onExpiry` too,
//! without counting an expiry; a release with `abandon` counts as one,
//! escalate included. `ended` says how the last lease ended (release,
//! abandon, or the expiry's reason: ttl, maxHold or holder) and when;
//! acquire clears it.
//!
//! The expiry and the longest hold are one deadline, `expire`, armed at
//! `expiresAt` by each acquire and heartbeat and cleared at each end, which
//! the API's behavior worker runs when it falls due (D103, step 3), in place
//! of the engine runner's sweep. A deadline that falls due on a lease still
//! active (a raw write moved it) arms again at its deadline; otherwise it
//! calls the use's own `expire` (`Cx::call`), so every use's guards run as
//! they ran on the sweep's invoke, and passes on any refusal: a guard's
//! veto of the fire refuses the command, which the worker releases to fall
//! due a minute on, as the sweep tried it at its next run. `sweepMs`, how
//! often the runner swept, is refused on a table, as `directOn` is, which
//! waits for Revisions and Branches on the core (D103, step 4); `expireHolder`
//! runs on the table as a whole, which the core runs no command for, so a
//! table's declaration leaves it out (D104's Left for later).
//!
//! The guard keeps a lease exclusive. While a lease is active it refuses an
//! update, a delete and every writing operation of another behavior by any
//! caller but the holder, except an operation the config exempts and a
//! caller with `overridePermission`; with `requireToken`, it refuses the
//! holder's too unless they present the token. A read-only operation
//! passes, and so does a request a behavior's own code makes (`call()`,
//! `call_on()`), but for a use's call of its own operation on its row, any
//! call a deadline's handler makes on its row (the core's `fired`) and a
//! call a behavior makes as the caller's own request (`as_caller`, D104): a
//! deadline's handler calls its own operation (Timers' `fireTimer`, Lease's
//! `expire`, Presence's `miss`) as the engine's runner invoked it, or
//! moves the row itself (Retries' `then`, Workflow's `transition`), as the
//! command's caller, so the worker's fire meets an active lease as the
//! runner's did, and a table that wants it through lists it in `exempt`.
//! Reconcile's steps toward an observed state are each the observer's own
//! (`Cx::call_as_caller`), held as its request whatever `exempt` says of
//! `observe`, and presented the observer's token, so an observation that
//! lands while another principal holds the lease moves nothing past its
//! holder: a step is held unless `exempt` lists `Workflow.transition`.
//! Lease's own operations check their callers themselves, but for
//! `direct`, whose permission the guard asks of a caller. Nothing changes
//! the field that limits the hold while a lease is held. The engine's pass
//! of Queue's `refresh` is dropped: Queue folds into `@queue` (D104).
//!
//! Directives are a holder's control channel, rows of the `directives`
//! sub-table keyed by their number in ten digits. `direct` attaches one to
//! the current token, once per `dedupeKey` when it gives one; `heartbeat`
//! returns the ones not acknowledged, and `acknowledge`, or heartbeat's own
//! `acknowledge`, marks them handled. A caller's direct needs
//! `directPermission` (`overridePermission` when that is absent), which the
//! guard asks; another behavior of the row sends one through `call()` with
//! none. Every end of a lease deletes its directives, so none reaches the
//! next holder.
//!
//! An update of the row's `maxHoldField` while a lease is held, which only
//! a caller with `overridePermission` makes, brings its `expiresAt` to the
//! new longest hold when that comes first and moves its deadline there, as
//! the engine's expiry read the hold at each sweep; a longer one leaves it
//! where it was, unless the old hold cut the last renewal short
//! (`expiresAt` at the old hold), when the lease takes the renewal the
//! config's ttl gives it under the new hold, which its heartbeats renew by
//! from then on: the engine kept the acquire's ttl and renewed by it, so a
//! raise never shortened a heartbeat's renewal, and the core, which keeps
//! no ttl, reads the config's in its place (D104's Left for later).
//!
//! A field with no value reads as the engine's default wherever Lease reads
//! its own state, `token` 0 and `expiries` 0, so a row that predates its use
//! (a column added NULL) is a free row at token 0. `active` is no column: a
//! read computes it at the read's time ([`Behavior::read_fields_at`]). The
//! engine's guidance (its `describe`) has no counterpart on the core.
//!
//! Which Workflow the moves name, and whose terminal states an expiry does
//! not count in, is the config's `status` (D104), as Timers' and StateRules'
//! is: the named Workflow use, else the type's one, else none (an expiry
//! always counts, and a move is refused in the engine's words).
//!
//! configChange: any config may change. Lease can be added to a type that
//! has rows, which start free at token 0, and cannot be removed from one:
//! the leases and directives its rows hold would stay behind.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    Refusal, Row, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::declared;
use crate::moves::{self, StatusMove};
use crate::schema::{self, Issues};
use crate::values::{date_time_text, equal};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Lease";

/// The sub-table of the directives sent to a row's holder.
pub const DIRECTIVES: &str = "directives";

/// The operation that applies an expiry, and the deadline the expiry and
/// the longest hold fall due at.
pub const EXPIRE: &str = "expire";

/// The operations, by their declared names.
pub const ACQUIRE: &str = "acquire";
pub const HEARTBEAT: &str = "heartbeat";
pub const RELEASE: &str = "release";
pub const DIRECT: &str = "direct";
pub const ACKNOWLEDGE: &str = "acknowledge";
pub const RESET_EXPIRIES: &str = "resetExpiries";

/// How long a lease lasts after its acquire or its last heartbeat when the
/// config gives no ttlMs.
pub const DEFAULT_TTL_MS: i64 = 60_000;

/// The IR types maxHoldField may have: an integer field, held to the
/// build's list by its test.
pub const HOLD_TYPES: [&str; 1] = ["Generic.Int64"];

/// Lease's declaration, the file `make behaviors` writes for the work
/// queue's crate, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-workqueue/rust/declarations/Lease.behavior.json");

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 15] = [
    "acquirePermission",
    "directOn",
    "directPermission",
    "escalate",
    "exempt",
    "heartbeatMs",
    "maxExpiries",
    "maxHoldField",
    "maxHoldMs",
    "onExpiry",
    "overridePermission",
    "requireToken",
    "status",
    "sweepMs",
    "ttlMs",
];
const MOVE_KEYS: [&str; 2] = ["from", "transition"];
const DIRECT_ON_KEYS: [&str; 3] = ["data", "name", "revised"];
const REVISED_KEYS: [&str; 1] = ["link"];

// What a directive's name and a directOn link's name are held to.
const DIRECTIVE_NAME: &str = "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$";
const LINK_NAME: &str = "^[a-z][A-Za-z0-9]{0,63}$";
const EXEMPT_NAME: &str = "^([A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[a-z][A-Za-z0-9]*$";

// The bounds the paramsSchemas give.
const MAX_IDS: usize = 500;
const MAX_DEDUPE_KEY: usize = 128;
const MAX_DIRECT_ON: usize = 16;

/// A directive's key is its number in this many digits, zero-padded.
const NUMBER_WIDTH: usize = 10;

/// Lease's config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LeaseConfig {
    pub ttl_ms: i64,
    pub heartbeat_ms: i64,
    pub max_hold_ms: Option<i64>,
    pub max_hold_field: Option<String>,
    pub on_expiry: Option<StatusMove>,
    pub max_expiries: Option<i64>,
    pub escalate: Option<StatusMove>,
    /// The writing operations others may run under an active lease, as
    /// `<Behavior>.<operation>`.
    pub exempt: Vec<String>,
    pub require_token: bool,
    pub acquire_permission: Option<String>,
    pub override_permission: Option<String>,
    pub direct_permission: Option<String>,
    /// The Workflow use its moves name and whose terminal states an expiry
    /// does not count in; None for a type with none.
    pub status: Option<String>,
    /// That Workflow's terminal states.
    pub terminal: Vec<String>,
}

impl LeaseConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the engine's rules in its order and words, then what a
    /// table's Lease refuses.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<LeaseConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let number = |name: &str| raw.get(name).and_then(whole);
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        let ttl_ms = number("ttlMs").unwrap_or(DEFAULT_TTL_MS);
        let heartbeat_ms = number("heartbeatMs").unwrap_or(ttl_ms.div_euclid(3));
        if heartbeat_ms * 2 > ttl_ms {
            return Err(format!(
                "heartbeatMs ({heartbeat_ms}) is more than half of ttlMs ({ttl_ms}): one late heartbeat would lose the lease"
            )
            .into());
        }
        let max_hold_field = text("maxHoldField");
        if let Some(field) = &max_hold_field {
            check_hold_field(target, field)?;
        }
        let on_expiry = raw.get("onExpiry").map(StatusMove::parse);
        let escalate = raw.get("escalate").map(StatusMove::parse);
        let flow = moves::flow_for(target, raw.get("status").and_then(Value::as_str))?;
        for (at, rule) in [("onExpiry", &on_expiry), ("escalate", &escalate)] {
            let Some(rule) = rule else {
                continue;
            };
            match &flow {
                None => return Err(moves::no_workflow(at)),
                Some(flow) => moves::check_move(flow, at, rule)?,
            }
        }
        let max_expiries = number("maxExpiries");
        if escalate.is_some() && max_expiries.is_none() {
            return Err(
                "escalate applies at the expiry that reaches maxExpiries, which the config does not give"
                    .into(),
            );
        }
        let exempt = moves::strings(raw.get("exempt"));
        let mut listed: Vec<&str> = Vec::new();
        for entry in target.uses() {
            if !listed.contains(&entry.behavior.as_str()) {
                listed.push(&entry.behavior);
            }
        }
        for entry in &exempt {
            let behavior = &entry[..entry.rfind('.').unwrap_or(0)];
            if behavior == NAME {
                return Err(format!(
                    "exempt names {entry}: Lease's own operations check their callers themselves"
                )
                .into());
            }
            if !listed.contains(&behavior) {
                return Err(format!(
                    "exempt names {entry}, but the type does not list {behavior} (it lists {})",
                    listed.join(", ")
                )
                .into());
            }
        }
        // What a table's Lease refuses, after every check the engine made.
        if raw.get("directOn").is_some() {
            return Err(
                "directOn sends a directive when the target of a link moves on, a revision of Revisions or a release of Branches, which waits for Revisions and Branches on the core (D103, step 4)"
                    .into(),
            );
        }
        if raw.get("sweepMs").is_some() {
            return Err(
                "sweepMs is how often the engine's runner swept the lapsed leases: a table's lease expires at its own deadline, which the API's behavior worker runs when it falls due (D103), so a table's Lease takes no sweepMs"
                    .into(),
            );
        }
        let terminal = flow
            .as_ref()
            .map(|flow| {
                flow.states
                    .iter()
                    .filter(|state| moves::is_terminal(flow, state))
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        Ok(LeaseConfig {
            ttl_ms,
            heartbeat_ms,
            max_hold_ms: number("maxHoldMs"),
            max_hold_field,
            on_expiry,
            max_expiries,
            escalate,
            exempt,
            require_token: raw.get("requireToken") == Some(&Value::Bool(true)),
            acquire_permission: text("acquirePermission"),
            override_permission: text("overridePermission"),
            direct_permission: text("directPermission"),
            status: flow.map(|flow| flow.use_id),
            terminal,
        })
    }

    // heartbeat_for is the heartbeat interval of a lease acquired for ttl:
    // the config's, scaled to a shorter ttl, in JavaScript's arithmetic.
    fn heartbeat_for(&self, ttl: i64) -> i64 {
        if ttl == self.ttl_ms {
            return self.heartbeat_ms;
        }
        let scaled = (self.heartbeat_ms as f64 * ttl as f64) / self.ttl_ms as f64;
        (scaled.floor() as i64).max(1)
    }
}

/// The lease's owned fields on a row, a field with no value read as the
/// engine's default.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct Held {
    holder: Option<String>,
    token: i64,
    acquired_at: Option<i64>,
    renewed_at: Option<i64>,
    expires_at: Option<i64>,
    expiries: i64,
}

impl Held {
    // of reads the fields as a use's owned fields hold them.
    fn of(get: impl Fn(&str) -> Option<Value>) -> Held {
        let millis = |field: &str| get(field).as_ref().and_then(whole);
        Held {
            holder: get("holder")
                .and_then(|holder| holder.as_str().map(str::to_owned))
                .filter(|holder| !holder.is_empty()),
            token: millis("token").unwrap_or(0),
            acquired_at: millis("acquiredAt"),
            renewed_at: millis("renewedAt"),
            expires_at: millis("expiresAt"),
            expiries: millis("expiries").unwrap_or(0),
        }
    }

    // deadline is when a held lease stops being active: its expiry time,
    // or its longest hold after its acquire when that comes first.
    fn deadline(&self, limit: Option<i64>) -> i64 {
        let expires = self.expires_at.unwrap_or(0);
        match limit {
            None => expires,
            Some(limit) => expires.min(self.acquired_at.unwrap_or(0) + limit),
        }
    }

    // is_active is the one rule for whether a lease holds: its holder
    // holds a subject, before its expiry time and within its longest hold.
    fn is_active(&self, limit: Option<i64>, now: i64) -> bool {
        self.holder.is_some() && now < self.deadline(limit)
    }

    // lapse_reason is why a held lease stopped being active: its longest
    // hold when that came no later than its expiry time, else its expiry
    // time.
    fn lapse_reason(&self, limit: Option<i64>) -> &'static str {
        match limit {
            Some(limit)
                if self.acquired_at.unwrap_or(0) + limit <= self.expires_at.unwrap_or(0) =>
            {
                "maxHold"
            }
            _ => "ttl",
        }
    }
}

fn held(view: &View<'_, LeaseConfig>) -> Held {
    Held::of(|field| view.get(field))
}

// hold_limit is the longest a row's lease may be held after its acquire:
// the row's maxHoldField when it holds a positive whole number (a JSON
// number, or a decimal string as an adapter may read a BIGINT), else
// maxHoldMs; `None` for no limit.
fn hold_limit(config: &LeaseConfig, own: &Map<String, Value>) -> Option<i64> {
    if let Some(field) = &config.max_hold_field {
        let held = match own.get(field) {
            Some(value @ Value::Number(_)) => whole(value),
            Some(Value::String(text)) => text.parse::<i64>().ok(),
            _ => None,
        };
        if let Some(limit) = held.filter(|limit| *limit > 0 && *limit <= 9_007_199_254_740_991) {
            return Some(limit);
        }
    }
    config.max_hold_ms
}

fn limit_of(view: &View<'_, LeaseConfig>) -> Option<i64> {
    hold_limit(view.config(), &view.own())
}

// iso is an instant as a refusal names it: JavaScript's toISOString, the
// epoch digits where it gives none.
fn iso(ms: i64) -> String {
    date_time_text(ms).unwrap_or_else(|| ms.to_string())
}

// said is a veto with a code the declaration lists.
fn said(reason: impl Into<String>, code: &str, details: Option<Value>) -> Veto {
    let veto = Veto::new(reason).with_code(code);
    match details {
        Some(Value::Object(members)) => veto.with_details(members),
        _ => veto,
    }
}

// presented is the token the command presents as the use's precondition,
// to a guard asked about its own write.
fn presented(view: &View<'_, LeaseConfig>) -> Option<i64> {
    view.precondition()?.get("token").and_then(whole)
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
fn lapsed(lease: &Held, limit: Option<i64>, reason: impl Fn(&str) -> String) -> Veto {
    let at = lease.deadline(limit);
    said(reason(&iso(at)), "lapsed", Some(json!({ "expiredAt": at })))
}

// target names the row a message is about: its type and its key.
fn target<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn forbidden<C>(view: &View<'_, C>, what: &str, permission: &str) -> Stop {
    view.forbidden(format!(
        "{} may not {what} {}: it needs permission {permission}",
        view.caller().subject,
        target(view)
    ))
}

fn overrides(view: &View<'_, LeaseConfig>) -> Hook<bool> {
    match &view.config().override_permission {
        Some(permission) => view.can(permission),
        None => Ok(false),
    }
}

// status_of is the status of the Workflow the moves name, as the run has
// it; None without one.
fn status_of(view: &View<'_, LeaseConfig>) -> Option<String> {
    let use_id = view.config().status.as_deref()?;
    view.get_of(use_id, crate::workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// counts reports whether an expiry counts: unless the row is in a terminal
// state of its Workflow, where its work is finished.
fn counts(config: &LeaseConfig, status: Option<&str>) -> bool {
    status.is_none_or(|status| !config.terminal.iter().any(|state| state == status))
}

// end clears the holder and advances the token, recording how the lease
// ended, deletes the directives of the lease it ends, so none reaches the
// next holder, and clears the expiry's deadline.
fn end(cx: &Cx<'_, LeaseConfig>, lease: &Held, expiries: i64, ended: &str) -> Hook<()> {
    cx.set("holder", Value::Null)?;
    cx.set("token", lease.token + 1)?;
    cx.set("acquiredAt", Value::Null)?;
    cx.set("renewedAt", Value::Null)?;
    cx.set("expiresAt", Value::Null)?;
    cx.set("expiries", expiries)?;
    cx.set("ended", json!({ "reason": ended, "at": cx.now() }))?;
    for row in cx.sub_rows(DIRECTIVES)? {
        cx.delete_row(DIRECTIVES, &row.key)?;
    }
    cx.clear(EXPIRE)
}

// move_status moves the status with a rule from the status read before
// the lease ended.
fn move_status(
    cx: &Cx<'_, LeaseConfig>,
    rule: Option<&StatusMove>,
    status: Option<&str>,
) -> Hook<()> {
    let Some(use_id) = cx.config().status.as_deref() else {
        return Ok(());
    };
    moves::apply(cx, use_id, rule, status).map(|_| ())
}

// apply_expiry ends a lease as an expiry: the one code expire, acquire and
// an abandon share.
fn apply_expiry(cx: &Cx<'_, LeaseConfig>, lease: &Held, ended: &str) -> Hook<()> {
    let config = cx.config();
    let status = status_of(cx);
    let counted = counts(config, status.as_deref());
    let expiries = lease.expiries + i64::from(counted);
    end(cx, lease, expiries, ended)?;
    let capped = counted && config.max_expiries.is_some_and(|max| expiries >= max);
    let rule = match &config.escalate {
        Some(escalate) if capped => Some(escalate),
        _ => config.on_expiry.as_ref(),
    };
    move_status(cx, rule, status.as_deref())
}

// holding holds heartbeat and acknowledge to the holder of an active
// lease, presenting its token; the guard has refused a stale one.
fn holding(view: &View<'_, LeaseConfig>, lease: &Held, token: Option<i64>) -> Option<Veto> {
    let Some(holder) = &lease.holder else {
        return Some(said("it is not leased", "not_leased", None));
    };
    if *holder != view.caller().subject {
        return Some(said(
            "another principal holds its lease",
            "not_holder",
            None,
        ));
    }
    if token.is_none() {
        return Some(said(
            "the holder presents its token as Lease's precondition",
            "token_required",
            None,
        ));
    }
    let limit = limit_of(view);
    if !lease.is_active(limit, view.now()) {
        return Some(lapsed(lease, limit, |at| {
            format!("its lease expired at {at}; expire applies the expiry, and acquire takes a new lease")
        }));
    }
    None
}

// releasing holds release to the holder of an active lease, presenting its
// token, or a caller with overridePermission, who need not.
fn releasing(view: &View<'_, LeaseConfig>, lease: &Held, token: Option<i64>) -> Hook<Option<Veto>> {
    let Some(holder) = &lease.holder else {
        return Ok(Some(said("it is not leased", "not_leased", None)));
    };
    let config = view.config();
    let overridden = overrides(view)?;
    if *holder != view.caller().subject && !overridden {
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
    let limit = limit_of(view);
    if !lease.is_active(limit, view.now()) {
        return Ok(Some(lapsed(lease, limit, |at| {
            format!("its lease expired at {at}; expire applies the expiry")
        })));
    }
    Ok(None)
}

// direct_guard is who may send a directive: a caller with directPermission,
// or overridePermission when that is absent. A behavior's call() is not
// asked (the guard passes it).
fn direct_guard(view: &View<'_, LeaseConfig>) -> Hook<Option<Veto>> {
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
fn check_guarded(cx: &Cx<'_, LeaseConfig>, operation: &str, lease: &Held) -> Hook<()> {
    let holds = lease.holder.as_deref() == Some(cx.caller().subject.as_str());
    if !holds || !lease.is_active(limit_of(cx), cx.now()) {
        return Err(cx.defect(format!("{operation} reached its handler without its guard")));
    }
    Ok(())
}

// require_override holds a call to a caller with overridePermission, and
// refuses it outright when the config names none.
fn require_override(view: &View<'_, LeaseConfig>, what: &str) -> Hook<()> {
    let subject = &view.caller().subject;
    let type_name = view.type_name();
    match &view.config().override_permission {
        None => Err(view.forbidden(format!(
            "{subject} may not {what} {type_name}: its Lease config names no overridePermission"
        ))),
        Some(permission) if !view.can(permission)? => Err(view.forbidden(format!(
            "{subject} may not {what} {type_name}: it needs permission {permission}"
        ))),
        Some(_) => Ok(()),
    }
}

// operation_params refuses one of Lease's operations' parameters.
fn operation_params<C>(view: &View<'_, C>, operation: &str, path: &str, message: String) -> Stop {
    Refusal::operation_params(
        view.behavior(),
        view.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

// number_of is a directive's number, from its key.
fn number_of(row: &SubRow) -> Option<i64> {
    row.key.parse().ok()
}

// key_of is a directive's key: its number in ten digits, zero-padded.
fn key_of(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
}

// token_of is the token a directive was sent under.
fn token_of(row: &SubRow) -> Option<i64> {
    row.value("token").and_then(whole)
}

// directive_of reads a directive as heartbeat returns it, as the engine
// did: its number, name, data and dedupeKey when it has them, and who sent
// it and when.
fn directive_of(row: &SubRow) -> Value {
    let mut out = Map::new();
    out.insert("id".into(), number_of(row).map_or(Value::Null, Value::from));
    out.insert(
        "name".into(),
        row.value("name").cloned().unwrap_or(Value::Null),
    );
    if let Some(data) = row.value("data") {
        out.insert("data".into(), data.clone());
    }
    if let Some(dedupe_key) = row.value("dedupeKey") {
        out.insert("dedupeKey".into(), dedupe_key.clone());
    }
    out.insert(
        "createdAt".into(),
        row.value("sentAt").cloned().unwrap_or(Value::Null),
    );
    out.insert(
        "createdBy".into(),
        row.value("sentBy").cloned().unwrap_or(Value::Null),
    );
    Value::Object(out)
}

// ids_of reads a list of ids the paramsSchema let through.
fn ids_of(value: Option<&Value>) -> Vec<i64> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(whole)
        .collect()
}

// acknowledge_directives marks directives of the lease handled, for
// acknowledge and heartbeat's acknowledge, and returns how many it marked:
// none when each was handled already. An id not sent under its token
// refuses the call.
fn acknowledge_directives(
    cx: &Cx<'_, LeaseConfig>,
    operation: &str,
    path: &str,
    lease: &Held,
    ids: &[i64],
) -> Hook<usize> {
    let rows = cx.sub_rows(DIRECTIVES)?;
    let sent: Vec<&SubRow> = rows
        .iter()
        .filter(|row| token_of(row) == Some(lease.token))
        .collect();
    let missing: Vec<String> = ids
        .iter()
        .filter(|id| !sent.iter().any(|row| number_of(row) == Some(**id)))
        .map(i64::to_string)
        .collect();
    if !missing.is_empty() {
        return Err(operation_params(
            cx,
            operation,
            path,
            format!(
                "no directive {} was sent under token {}",
                missing.join(", "),
                lease.token
            ),
        ));
    }
    let mut marked = 0;
    for row in sent {
        let unhandled = row.value("acknowledgedAt").is_none();
        if unhandled && number_of(row).is_some_and(|number| ids.contains(&number)) {
            let mut values = Map::new();
            values.insert("acknowledgedAt".into(), Value::from(cx.now()));
            cx.update_row(DIRECTIVES, &row.key, values)?;
            marked += 1;
        }
    }
    Ok(marked)
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

/// Lease: an exclusive, time-bounded lease on each row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Lease;

impl Behavior for Lease {
    type Config = LeaseConfig;

    // The declaration file names its owned fields, the computed active,
    // its directives sub-table, that it arms deadlines and takes a
    // precondition (D103, D104); expireHolder, an operation of the table as
    // a whole, is left out.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<LeaseConfig, ConfigError> {
        LeaseConfig::parse(raw, target)
    }

    // Any config may change; Lease joins a type with rows and does not
    // leave one, whose leases and directives would stay behind.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&LeaseConfig>,
        after: Option<&LeaseConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none())
            .then(|| "the leases and directives its instances hold would stay behind".to_owned()))
    }

    fn check_params(
        &self,
        _config: &LeaseConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // The preconditionSchema: `{ token }`, a whole number at least 0.
    fn check_precondition(&self, _config: &LeaseConfig, entry: &Value) -> Vec<SchemaIssue> {
        let mut issues = Issues::default();
        let Some(members) = entry.as_object() else {
            issues.push("", "type", "must be object");
            return issues.into_issues();
        };
        for key in members.keys().filter(|key| key.as_str() != "token") {
            issues.push(
                "",
                "additionalProperties",
                format!("must NOT have additional properties: {key}"),
            );
        }
        match members.get("token") {
            None => issues.push("", "required", "must have required property 'token'"),
            Some(token) => schema::integer_issues("/token", token, Some(0), None, &mut issues),
        }
        issues.into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, LeaseConfig>, _params: &Value) -> Hook<()> {
        cx.set("token", 0)?;
        cx.set("expiries", 0)
    }

    // guard is the lease's exclusion and its fence: see the module's
    // header. A new row holds no lease, so a create has nothing to exclude
    // or fence.
    fn guard(
        &self,
        view: &View<'_, LeaseConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        if matches!(request, GuardRequest::Create { .. }) {
            return Ok(None);
        }
        let config = view.config();
        let lease = held(view);
        let writes = match request {
            GuardRequest::Operation { writes, .. } => *writes,
            _ => true,
        };
        let token = presented(view);
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
                DIRECT if caller.is_none() => direct_guard(view),
                HEARTBEAT | ACKNOWLEDGE => Ok(holding(view, &lease, token)),
                RELEASE => releasing(view, &lease, token),
                _ => Ok(None),
            };
        }
        let Some(holder) = &lease.holder else {
            return Ok(None);
        };
        if !writes || overrides(view)? {
            return Ok(None);
        }
        if let (GuardRequest::Update { patch, .. }, Some(field)) = (request, &config.max_hold_field)
        {
            if patch.contains_key(field) {
                return Ok(Some(said(
                    format!("{field} limits how long its lease may be held, so it cannot change while the lease is held"),
                    "hold_limit_fixed",
                    None,
                )));
            }
        }
        // A request a behavior's code makes passes, but for a use's call of
        // its own operation on its row, a call a deadline's handler makes
        // and a call made as the caller's own request (D104): each is the
        // command's caller's request, as the engine's runner invoked it.
        if let GuardRequest::Operation {
            caller: Some(caller),
            use_id,
            caller_type,
            fired,
            as_caller,
            ..
        } = request
        {
            if !((caller == use_id || *fired || *as_caller) && caller_type.is_none()) {
                return Ok(None);
            }
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
        let mine = *holder == view.caller().subject;
        let limit = limit_of(view);
        if lease.is_active(limit, view.now()) {
            if !mine {
                let until = lease.deadline(limit);
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
            return Ok(Some(lapsed(&lease, limit, |at| {
                format!("the caller's lease expired at {at}, so it no longer holds the instance")
            })));
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, LeaseConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            ACQUIRE => acquire(cx, params),
            HEARTBEAT => heartbeat(cx, params),
            RELEASE => release(cx, params),
            EXPIRE => expire(cx, params),
            DIRECT => direct(cx, params),
            ACKNOWLEDGE => acknowledge(cx, params),
            RESET_EXPIRIES => reset_expiries(cx),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // The expiry's deadline: a lease still active (its deadline moved past
    // now since the arming, as only a raw write can move it) arms again at
    // its deadline; anything else calls the use's own expire, so every
    // use's guards run on it as they did on the engine's sweep, and passes
    // on any refusal, a guard's veto among them, which leaves the deadline
    // to fall due again.
    fn deadline(&self, cx: &Cx<'_, LeaseConfig>, name: &str, _payload: &Value) -> Hook<()> {
        if name != EXPIRE {
            return Err(cx.defect(format!(
                "deadline {name} fell due, and Lease arms only {EXPIRE}"
            )));
        }
        let lease = held(cx);
        let limit = limit_of(cx);
        if lease.is_active(limit, cx.now()) {
            return cx.arm(EXPIRE, lease.deadline(limit), Value::Null);
        }
        cx.call(cx.use_id(), EXPIRE, &json!({}))?;
        Ok(())
    }

    // An update of maxHoldField while a lease is held (by a caller with
    // overridePermission, the guard holds it from any other) brings the
    // lease's expiry to its new longest hold when that comes first, and
    // moves its deadline there. When the old hold cut the last renewal
    // short, the lease takes the renewal the config's ttl gives it under
    // the new hold instead, so its heartbeats renew by that ttl again
    // rather than by the short length the cut left. A delete takes the
    // row's directives and its deadline with it, which the host's delete
    // does.
    fn after_change(&self, cx: &Cx<'_, LeaseConfig>, change: &Change<'_>) -> Hook<()> {
        let config = cx.config();
        let (Change::Update { before, .. }, Some(field)) = (change, &config.max_hold_field) else {
            return Ok(());
        };
        if equal(before.get(field), cx.own().get(field)) {
            return Ok(());
        }
        let lease = held(cx);
        if lease.holder.is_none() {
            return Ok(());
        }
        let limit = limit_of(cx);
        let acquired = lease.acquired_at.unwrap_or(0);
        let cut =
            hold_limit(config, before).is_some_and(|old| lease.expires_at == Some(acquired + old));
        let expires_at = match (cut, lease.renewed_at) {
            (true, Some(renewed)) => match limit {
                None => renewed + config.ttl_ms,
                Some(limit) => (renewed + config.ttl_ms).min(acquired + limit),
            },
            _ => lease.deadline(limit),
        };
        if lease.expires_at == Some(expires_at) {
            return Ok(());
        }
        cx.set("expiresAt", expires_at)?;
        cx.arm(EXPIRE, expires_at, Value::Null)
    }

    // A read shows whether the lease is active at the read's time.
    fn read_fields_at(
        &self,
        config: &LeaseConfig,
        row: &Row,
        _tables: &BTreeMap<String, Vec<SubRow>>,
        now: i64,
    ) -> Map<String, Value> {
        let lease = Held::of(|field| row.owned(NAME, field).cloned());
        let active = lease.is_active(hold_limit(config, &row.own), now);
        let mut fields = Map::new();
        fields.insert("active".into(), Value::Bool(active));
        fields
    }
}

fn acquire(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    if let Some(permission) = &config.acquire_permission {
        if !cx.can(permission)? {
            return Err(forbidden(cx, "acquire the lease of", permission));
        }
    }
    let ttl = params.get("ttlMs").and_then(whole).unwrap_or(config.ttl_ms);
    if ttl > config.ttl_ms {
        return Err(operation_params(
            cx,
            ACQUIRE,
            "/ttlMs",
            format!("{ttl} is longer than the config's ttlMs, {}", config.ttl_ms),
        ));
    }
    let mut lease = held(cx);
    let limit = limit_of(cx);
    if lease.is_active(limit, cx.now()) {
        let until = lease.deadline(limit);
        let at = iso(until);
        let expires = Some(json!({ "expiresAt": until }));
        return Err(cx.veto(
            if lease.holder.as_deref() == Some(cx.caller().subject.as_str()) {
                said(
                    format!("the caller already holds its lease, until {at}; heartbeat renews it"),
                    "held_by_caller",
                    expires,
                )
            } else {
                said(
                    format!("another principal holds its lease, until {at}"),
                    "held_by_another",
                    expires,
                )
            },
        ));
    }
    let lapsed = lease.holder.is_some();
    if let Some(max) = config.max_expiries {
        let counted = lapsed && counts(config, status_of(cx).as_deref());
        let expiries = lease.expiries + i64::from(counted);
        if expiries >= max {
            return Err(cx.veto(said(
                format!("its lease has expired {expiries} times, the most its config allows; resetExpiries lets it be leased again"),
                "max_expiries",
                Some(json!({ "expiries": expiries, "maxExpiries": max })),
            )));
        }
    }
    if lapsed {
        apply_expiry(cx, &lease, lease.lapse_reason(limit))?;
        lease = held(cx);
    }
    let token = lease.token + 1;
    let now = cx.now();
    let expires_at = match limit {
        None => now + ttl,
        Some(limit) => now + ttl.min(limit),
    };
    cx.set("holder", cx.caller().subject.as_str())?;
    cx.set("token", token)?;
    cx.set("acquiredAt", now)?;
    cx.set("renewedAt", now)?;
    cx.set("expiresAt", expires_at)?;
    cx.set("ended", Value::Null)?;
    cx.arm(EXPIRE, expires_at, Value::Null)?;
    Ok(json!({ "token": token, "expiresAt": expires_at, "heartbeatMs": config.heartbeat_for(ttl) }))
}

// heartbeat: the guard holds it to the holder of an active lease, with its
// token. Its acknowledge marks directives handled in the same write. It
// renews by the length the last acquire or heartbeat gave the lease.
fn heartbeat(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let lease = held(cx);
    check_guarded(cx, HEARTBEAT, &lease)?;
    let ids = ids_of(params.get("acknowledge"));
    if !ids.is_empty() {
        acknowledge_directives(cx, HEARTBEAT, "/acknowledge", &lease, &ids)?;
    }
    let length = match (lease.expires_at, lease.renewed_at) {
        (Some(expires), Some(renewed)) => expires - renewed,
        _ => cx.config().ttl_ms,
    };
    let now = cx.now();
    let expires_at = match limit_of(cx) {
        None => now + length,
        Some(limit) => (now + length).min(lease.acquired_at.unwrap_or(0) + limit),
    };
    cx.set("expiresAt", expires_at)?;
    cx.set("renewedAt", now)?;
    cx.arm(EXPIRE, expires_at, Value::Null)?;
    let directives: Vec<Value> = cx
        .sub_rows(DIRECTIVES)?
        .iter()
        .filter(|row| token_of(row) == Some(lease.token) && row.value("acknowledgedAt").is_none())
        .map(directive_of)
        .collect();
    Ok(json!({ "expiresAt": expires_at, "directives": directives }))
}

// release: the guard holds it to the holder of an active lease, with its
// token, or a caller with overridePermission. An abandon is an expiry.
fn release(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let lease = held(cx);
    if lease.holder.is_none() || !lease.is_active(limit_of(cx), cx.now()) {
        return Err(cx.defect("release reached its handler without its guard"));
    }
    if params.get("abandon") == Some(&Value::Bool(true)) {
        apply_expiry(cx, &lease, "abandon")?;
        return Ok(json!({}));
    }
    let status = status_of(cx);
    end(cx, &lease, lease.expiries, "release")?;
    move_status(cx, config.on_expiry.as_ref(), status.as_deref())?;
    Ok(json!({}))
}

fn expire(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let holder = params.get("holder").and_then(Value::as_str);
    let not_renewed_after = params.get("notRenewedAfter").and_then(whole);
    if holder.is_none() && not_renewed_after.is_some() {
        return Err(operation_params(
            cx,
            EXPIRE,
            "/notRenewedAfter",
            "notRenewedAfter spares a holder's active leases, so it needs holder".to_owned(),
        ));
    }
    if holder.is_some() {
        require_override(cx, "expire the lease of another holder of")?;
    }
    let lease = held(cx);
    let limit = limit_of(cx);
    let active = lease.is_active(limit, cx.now());
    // A lease it does not expire is left as it was.
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
        cx.unchanged()?;
        return Ok(json!({ "expired": false }));
    }
    let reason = if active {
        "holder"
    } else {
        lease.lapse_reason(limit)
    };
    apply_expiry(cx, &lease, reason)?;
    Ok(json!({ "expired": true, "reason": reason }))
}

// direct: who may send one is the guard's question (direct_guard).
fn direct(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let lease = held(cx);
    let reason = "no lease is active, so there is no holder to direct";
    if lease.holder.is_none() {
        return Err(cx.veto(said(reason, "not_leased", None)));
    }
    let limit = limit_of(cx);
    if !lease.is_active(limit, cx.now()) {
        let at = lease.deadline(limit);
        return Err(cx.veto(said(reason, "lapsed", Some(json!({ "expiredAt": at })))));
    }
    let rows = cx.sub_rows(DIRECTIVES)?;
    let dedupe_key = params.get("dedupeKey").and_then(Value::as_str);
    if let Some(dedupe_key) = dedupe_key {
        let sent = rows.iter().find(|row| {
            token_of(row) == Some(lease.token)
                && row.value("dedupeKey").and_then(Value::as_str) == Some(dedupe_key)
        });
        if let Some(sent) = sent {
            // Sent already: the directive stands as it was sent.
            cx.unchanged()?;
            return Ok(json!({ "id": number_of(sent), "created": false }));
        }
    }
    let number = rows.iter().filter_map(number_of).max().unwrap_or(0) + 1;
    let mut values = Map::new();
    values.insert("token".into(), Value::from(lease.token));
    values.insert(
        "name".into(),
        params.get("name").cloned().unwrap_or(Value::Null),
    );
    if let Some(data) = params.get("data") {
        values.insert("data".into(), data.clone());
    }
    if let Some(dedupe_key) = dedupe_key {
        values.insert("dedupeKey".into(), Value::from(dedupe_key));
    }
    values.insert("sentBy".into(), Value::from(cx.caller().subject.as_str()));
    values.insert("sentAt".into(), Value::from(cx.now()));
    cx.insert_row(DIRECTIVES, &key_of(number), values)?;
    Ok(json!({ "id": number, "created": true }))
}

// acknowledge: the guard holds it to the holder of an active lease, with
// its token.
fn acknowledge(cx: &Cx<'_, LeaseConfig>, params: &Value) -> Hook<Value> {
    let lease = held(cx);
    check_guarded(cx, ACKNOWLEDGE, &lease)?;
    // Every one acknowledged already: nothing changes.
    let ids = ids_of(params.get("ids"));
    if acknowledge_directives(cx, ACKNOWLEDGE, "/ids", &lease, &ids)? == 0 {
        cx.unchanged()?;
    }
    Ok(json!({}))
}

fn reset_expiries(cx: &Cx<'_, LeaseConfig>) -> Hook<Value> {
    let Some(permission) = &cx.config().override_permission else {
        return Err(cx.veto(said(
            "its config names no overridePermission, which resetting its expiries needs",
            "not_configured",
            None,
        )));
    };
    if !cx.can(permission)? {
        return Err(forbidden(cx, "reset the expiries of", permission));
    }
    let expiries = held(cx).expiries;
    if expiries == 0 {
        cx.unchanged()?;
        return Ok(json!({ "expiries": expiries }));
    }
    cx.set("expiries", 0)?;
    Ok(json!({ "expiries": expiries }))
}

// check_hold_field holds maxHoldField to an integer field of the type, as
// its spec lists its own fields.
fn check_hold_field(target: &ConfigTarget<'_>, field: &str) -> Result<(), ConfigError> {
    let fields = target.fields();
    let Some(found) = fields.iter().find(|known| known.name == field) else {
        let names: Vec<&str> = fields.iter().map(|known| known.name.as_str()).collect();
        return Err(format!(
            "maxHoldField \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        )
        .into());
    };
    if !HOLD_TYPES.contains(&found.type_name.as_str()) {
        return Err(format!(
            "maxHoldField \"{field}\" is not an integer field of {}",
            target.type_name()
        )
        .into());
    }
    Ok(())
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
    for key in [
        "acquirePermission",
        "directPermission",
        "maxHoldField",
        "overridePermission",
        "status",
    ] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    for (key, minimum) in [
        ("heartbeatMs", 1),
        ("maxExpiries", 1),
        ("maxHoldMs", 1000),
        ("sweepMs", 1000),
        ("ttlMs", 1000),
    ] {
        if let Some(value) = config.get(key) {
            schema::integer_issues(
                &schema::member("", key),
                value,
                Some(minimum),
                None,
                &mut issues,
            );
        }
    }
    if let Some(flag) = config.get("requireToken") {
        schema::boolean_issues("/requireToken", flag, &mut issues);
    }
    for key in ["onExpiry", "escalate"] {
        if let Some(rule) = config.get(key) {
            move_issues(&schema::member("", key), rule, &mut issues);
        }
    }
    if let Some(exempt) = config.get("exempt") {
        exempt_issues(exempt, &mut issues);
    }
    if let Some(direct_on) = config.get("directOn") {
        direct_on_issues(direct_on, &mut issues);
    }
    issues
}

// move_issues lists what a status move breaks of `{ transition, from }`.
pub(crate) fn move_issues(at: &str, rule: &Value, issues: &mut Issues) {
    let Some(members) = rule.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &MOVE_KEYS, issues);
    for key in ["transition", "from"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(transition) = members.get("transition") {
        schema::text_issues(&schema::member(at, "transition"), transition, issues);
    }
    if let Some(from) = members.get("from") {
        state_list_issues(&schema::member(at, "from"), from, issues);
    }
}

// state_list_issues lists what a list of states breaks of `{ type: array,
// minItems: 1, uniqueItems: true, items: { type: string, minLength: 1 } }`.
pub(crate) fn state_list_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
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

fn exempt_issues(exempt: &Value, issues: &mut Issues) {
    let at = "/exempt";
    let Some(items) = exempt.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    for (i, item) in items.iter().enumerate() {
        let at = format!("{at}/{i}");
        match item {
            Value::String(name) if !is_exempt_name(name) => issues.push(
                at,
                "pattern",
                format!("must match pattern \"{EXEMPT_NAME}\""),
            ),
            Value::String(_) => {}
            _ => issues.push(at, "type", "must be string"),
        }
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

fn direct_on_issues(direct_on: &Value, issues: &mut Issues) {
    let at = "/directOn";
    let Some(entries) = direct_on.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if entries.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if entries.len() > MAX_DIRECT_ON {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {MAX_DIRECT_ON} items"),
        );
    }
    for (i, entry) in entries.iter().enumerate() {
        let at = format!("{at}/{i}");
        let Some(members) = entry.as_object() else {
            issues.push(&at, "type", "must be object");
            continue;
        };
        unknown_keys(&at, members, &DIRECT_ON_KEYS, issues);
        for key in ["revised", "name"] {
            if !members.contains_key(key) {
                issues.push(
                    &at,
                    "required",
                    format!("must have required property '{key}'"),
                );
            }
        }
        if members.get("data").is_some_and(|data| !data.is_object()) {
            issues.push(schema::member(&at, "data"), "type", "must be object");
        }
        if let Some(name) = members.get("name") {
            pattern_issues(
                &schema::member(&at, "name"),
                name,
                DIRECTIVE_NAME,
                is_directive_name,
                issues,
            );
        }
        if let Some(revised) = members.get("revised") {
            let at = schema::member(&at, "revised");
            let Some(revised) = revised.as_object() else {
                issues.push(&at, "type", "must be object");
                continue;
            };
            unknown_keys(&at, revised, &REVISED_KEYS, issues);
            match revised.get("link") {
                None => issues.push(&at, "required", "must have required property 'link'"),
                Some(link) => pattern_issues(
                    &schema::member(&at, "link"),
                    link,
                    LINK_NAME,
                    is_link_name,
                    issues,
                ),
            }
        }
    }
}

// pattern_issues lists what a value breaks of `{ type: string, pattern }`.
fn pattern_issues(
    at: &str,
    value: &Value,
    pattern: &str,
    matches: fn(&str) -> bool,
    issues: &mut Issues,
) {
    match value {
        Value::String(text) if !matches(text) => {
            issues.push(at, "pattern", format!("must match pattern \"{pattern}\""));
        }
        Value::String(_) => {}
        _ => issues.push(at, "type", "must be string"),
    }
}

// is_directive_name says whether a name matches DIRECTIVE_NAME.
fn is_directive_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && name.chars().count() <= 64
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | ':' | '-'))
}

// is_link_name says whether a name matches LINK_NAME.
fn is_link_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_lowercase())
        && name.chars().count() <= 64
        && chars.all(|c| c.is_ascii_alphanumeric())
}

// is_exempt_name says whether a name matches EXEMPT_NAME: an optional
// package, a behavior and an operation, joined by dots.
fn is_exempt_name(name: &str) -> bool {
    let word = |part: &str, first: fn(char) -> bool, rest: fn(char) -> bool| {
        let mut chars = part.chars();
        chars.next().is_some_and(first) && chars.all(rest)
    };
    let behavior = |part: &str| {
        word(
            part,
            |c| c.is_ascii_uppercase(),
            |c| c.is_ascii_alphanumeric(),
        )
    };
    let operation = |part: &str| {
        word(
            part,
            |c| c.is_ascii_lowercase(),
            |c| c.is_ascii_alphanumeric(),
        )
    };
    let parts: Vec<&str> = name.split('.').collect();
    match parts.as_slice() {
        [b, o] => behavior(b) && operation(o),
        [p, b, o] => {
            word(
                p,
                |c| c.is_ascii_alphabetic(),
                |c| c.is_ascii_alphanumeric() || c == '_' || c == '-',
            ) && behavior(b)
                && operation(o)
        }
        _ => false,
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
// paramsSchema, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = match operation {
        ACQUIRE => &["ttlMs"],
        HEARTBEAT => &["acknowledge"],
        RELEASE => &["abandon"],
        EXPIRE => &["holder", "notRenewedAfter"],
        DIRECT => &["data", "dedupeKey", "name"],
        ACKNOWLEDGE => &["ids"],
        RESET_EXPIRIES => &[],
        _ => return issues,
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    match operation {
        ACQUIRE => {
            if let Some(ttl) = given.get("ttlMs") {
                schema::integer_issues("/ttlMs", ttl, Some(1000), None, &mut issues);
            }
        }
        HEARTBEAT => {
            if let Some(ids) = given.get("acknowledge") {
                ids_issues("/acknowledge", ids, false, &mut issues);
            }
        }
        RELEASE => {
            if let Some(abandon) = given.get("abandon") {
                schema::boolean_issues("/abandon", abandon, &mut issues);
            }
        }
        EXPIRE => {
            if let Some(holder) = given.get("holder") {
                schema::text_issues("/holder", holder, &mut issues);
            }
            if let Some(after) = given.get("notRenewedAfter") {
                schema::integer_issues("/notRenewedAfter", after, Some(0), None, &mut issues);
            }
        }
        DIRECT => {
            match given.get("name") {
                None => issues.push("", "required", "must have required property 'name'"),
                Some(name) => pattern_issues(
                    "/name",
                    name,
                    DIRECTIVE_NAME,
                    is_directive_name,
                    &mut issues,
                ),
            }
            if given.get("data").is_some_and(|data| !data.is_object()) {
                issues.push("/data", "type", "must be object");
            }
            match given.get("dedupeKey") {
                Some(Value::String(key)) if key.is_empty() => {
                    issues.push(
                        "/dedupeKey",
                        "minLength",
                        "must NOT have fewer than 1 characters",
                    );
                }
                Some(Value::String(key)) if schema::length(key) > MAX_DEDUPE_KEY => {
                    issues.push(
                        "/dedupeKey",
                        "maxLength",
                        format!("must NOT have more than {MAX_DEDUPE_KEY} characters"),
                    );
                }
                Some(Value::String(_)) | None => {}
                Some(_) => issues.push("/dedupeKey", "type", "must be string"),
            }
        }
        ACKNOWLEDGE => match given.get("ids") {
            None => issues.push("", "required", "must have required property 'ids'"),
            Some(ids) => ids_issues("/ids", ids, true, &mut issues),
        },
        _ => {}
    }
    issues
}

// ids_issues lists what a list of directive ids breaks of `{ type: array,
// minItems?: 1, maxItems: 500, uniqueItems: true, items: { type: integer,
// minimum: 1 } }`.
fn ids_issues(at: &str, value: &Value, required: bool, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if required && items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if items.len() > MAX_IDS {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {MAX_IDS} items"),
        );
    }
    for (i, item) in items.iter().enumerate() {
        schema::integer_issues(&format!("{at}/{i}"), item, Some(1), None, issues);
    }
    if let Some((j, i)) = schema::duplicate_value(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_directives_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of(1), "0000000001");
        assert_eq!(number_of(&SubRow::new("0000000042", Map::new())), Some(42));
    }

    #[test]
    fn the_names_match_their_patterns() {
        for name in ["rebase", "a.b:c-d_e", "X"] {
            assert!(is_directive_name(name), "{name}");
        }
        for name in ["", "1st", "a b", &format!("a{}", "b".repeat(64))] {
            assert!(!is_directive_name(name), "{name}");
        }
        for name in ["spec", "aB9"] {
            assert!(is_link_name(name), "{name}");
        }
        for name in ["Spec", "a_b", ""] {
            assert!(!is_link_name(name), "{name}");
        }
        for name in [
            "Timers.fireTimer",
            "test.Gate.signal",
            "my-pkg.Comments.add",
        ] {
            assert!(is_exempt_name(name), "{name}");
        }
        for name in [
            "timers.fireTimer",
            "Timers.FireTimer",
            "Timers",
            "a.b.C.d",
            "Timers.fire_timer",
        ] {
            assert!(!is_exempt_name(name), "{name}");
        }
    }

    #[test]
    fn a_lease_is_active_until_its_deadline_and_lapses_for_its_reason() {
        let lease = Held {
            holder: Some("wren".into()),
            token: 1,
            acquired_at: Some(1000),
            renewed_at: Some(1000),
            expires_at: Some(61_000),
            expiries: 0,
        };
        assert!(lease.is_active(None, 60_999));
        assert!(!lease.is_active(None, 61_000));
        assert_eq!(lease.deadline(Some(30_000)), 31_000);
        assert_eq!(lease.lapse_reason(Some(30_000)), "maxHold");
        assert_eq!(lease.lapse_reason(Some(90_000)), "ttl");
        assert_eq!(lease.lapse_reason(None), "ttl");
        assert!(!Held::default().is_active(None, 0));
    }
}
