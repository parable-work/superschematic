//! Presence, a heartbeat on a row that stands for a principal (D16), ported
//! onto the pure core (D103) from the engine's
//! (`runtime/engine-workqueue/rust/src/presence.rs`). The row's
//! `principalField` holds the subject of the principal it stands for, and
//! only that principal may beat it: `beat` moves its deadline on. A row not
//! beaten by its deadline is missed. Its initializer sets the first deadline
//! one `ttlMs` after the create, so a worker that never beats is missed too.
//!
//! Presence passes D103's test of a behavior. Its state is the row's
//! presence: its deadline, its last beat, whether it was missed and what its
//! last miss released, held in four owned columns (`deadline`, `lastBeatAt`,
//! `missed`, `released`), and the deadline it arms (`miss`). Its rules say
//! who may beat it (the principal its principalField holds, alone), when it
//! is missed (at its deadline, once, its status moved by `onMissed`), what a
//! miss releases (the principal's leases, through Lease's own `expire`) and
//! how long it stays present in each state (`ttlByState`, D104). And it
//! recurs: a worker's heartbeat, a device's session, an agent's turn.
//!
//! `miss` applies a miss, and only once: on a row past its deadline that is
//! not missed yet, it sets missed, moves the status with `onMissed` when it
//! is one of onMissed's from states, through Workflow's transition, whose
//! guards run, and expires the leases the principal holds on the tables
//! `releaseLeases` names, through Lease's own `expire` on each row
//! (`Cx::call_on`), so each expiry runs Lease's own rules, its guards and
//! the row's after-change hooks. A table's Lease has no `expireHolder` (a
//! command without a row, D104), so the miss finds the rows itself: those
//! whose Lease `holder` holds the principal ([`View::read_holding`]), once
//! per text the subject is named in ([`subjects::subject_forms`]: a UUID's
//! base62 form and its hyphenated one in either case). It spares a lease
//! renewed after the principal's last sight (`notRenewedAfter`): a worker
//! whose presence beat starved while its lease heartbeats went on is alive.
//! It records the keys it expired, by table, in `released`. The leases are
//! the principal's, not the row's: while another row of the type stands for
//! the same principal and is present (not missed, before its deadline), a
//! miss leaves them. A transition a guard vetoes leaves the status as it is
//! and the row missed all the same; any other refusal, a permission the
//! caller lacks say, fails the miss and leaves it as it was. Anyone who may
//! write the row may call miss; on any other row it changes nothing. A
//! missed row stays missed until it beats: the next beat clears missed and
//! sets a new deadline, and `onBeat` moves the status back from its from
//! states.
//!
//! The engine's runner missed every instance past its deadline every
//! `sweepMs`. On a table each row's miss is a deadline (D103, step 3), armed
//! at its `deadline` while it is not missed, which the API's
//! `@behaviorWorker` runs as it falls due and which calls the use's own
//! `miss` (`Cx::call`), so every use's guards run on it as they ran on the
//! sweep's invoke, and any refusal passes on: a guard's veto of the miss
//! refuses the command, which the worker releases to fall due again a minute
//! on (D103). So a table's Presence takes no `sweepMs`: its define check, and
//! the build, refuse it rather than parse a member nothing reads. The
//! worker's principal needs, for releaseLeases, each table's Lease
//! `overridePermission`, which Lease's expire asks of the command's caller.
//! On a table that implements Lease too, Lease's guard holds the deadline's
//! call of Presence's own `miss` as the worker's write: list `Presence.miss`
//! in Lease's `exempt`, as a Timers table lists `Timers.fireTimer`.
//!
//! `ttlByState` (D104) gives a state of the Workflow `status` names (or the
//! type's one) a ttl of its own, `ttlMs` serving the states it does not list.
//! With it, a row's deadline is the later of its last beat and its status's
//! entry into its state (Workflow's `enteredAt`), plus the ttl its state
//! gives: a beat arms it from the beat, and a transition arms it again from
//! the entry. So the deadline is a function of the row's fields, which the
//! after-change hook reconciles on every write of the row through the core,
//! whoever makes the move (a caller's transition, a Timers `then`, Lease's
//! `onExpiry`), and a row that leaves a long state is never missed at the
//! instant it leaves it, since its entry into the next counts as a sighting.
//! Presence hears no raw write: a raw SQL write of the status re-arms nothing
//! until the row's next write through the core or its miss's fire, which
//! reconciles first and arms again at a later deadline rather than missing.
//! Without `ttlByState` the engine's rule stands: the create, then each
//! beat, plus `ttlMs`, and a transition arms nothing.
//!
//! The guard keeps the row to its principal: an update that changes
//! `principalField` once it holds a value is refused, whoever asks
//! (`principal_fixed`). beat checks its caller in its handler
//! (`no_principal`, `not_principal`), by [`subjects::names_subject`], so a
//! UUID field names its subject in either form.
//!
//! configChange: any config may change except `principalField`, which
//! changes only on a type with no rows. A new `ttlMs` or `ttlByState`
//! applies from each row's next beat, or with `ttlByState` its next write
//! through the core. Presence can be added to a type that has rows, which
//! have no deadline until their first beat, and cannot be removed from one:
//! the deadlines and misses they hold would stay behind.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, in the schema loader's words
//! ([`ConfigError::Schema`]), held to the file by this crate's tests, then
//! the table's refusal of `sweepMs`, then the principal field and the
//! Workflow in the engine's words. The tables releaseLeases names are not
//! read here (the core's define check reads no other type): the build holds
//! them to its database, and a miss refuses a name it cannot follow as a
//! defect. Messages name a row `{Type} {key}` where the engine named
//! `{schema} {id}`.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    KeyedRow, SchemaIssue, Stop, Veto, View,
};

use crate::declared;
use crate::flow::Flow;
use crate::moves::{self, StatusMove};
use crate::schema::{self, Issues};
use crate::subjects;
use crate::values;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Presence";

/// Presence's declaration, the file the compiler registers for the work
/// queue and the engine embeds, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-workqueue/rust/declarations/Presence.behavior.json");

/// The deadline a row's miss falls due at (D103, step 3), which calls the
/// use's own miss.
pub const MISS: &str = "miss";

/// The operation the principal beats a row with.
pub const BEAT: &str = "beat";

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 8] = [
    "onBeat",
    "onMissed",
    "principalField",
    "releaseLeases",
    "status",
    "sweepMs",
    "ttlByState",
    "ttlMs",
];
const MOVE_KEYS: [&str; 2] = ["from", "transition"];

// The bounds the configSchema holds the times to.
const MS_MINIMUM: i64 = 1000;

// What the configSchema holds a releaseLeases name to.
const TABLE_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";

// The behavior a releaseLeases table implements, whose expire a miss calls.
const LEASE: &str = "Lease";
const LEASE_HOLDER: &str = "holder";
const LEASE_EXPIRE: &str = "expire";

/// Presence's config, parsed: the principal field's type read, the Workflow
/// its moves and ttlByState read resolved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PresenceConfig {
    pub ttl_ms: i64,
    pub principal_field: String,
    /// The principal field's IR type, which names_subject compares a caller by.
    pub principal_type: String,
    pub on_missed: Option<StatusMove>,
    pub on_beat: Option<StatusMove>,
    /// The tables whose leases the principal holds a miss expires, in the
    /// config's order.
    pub release_leases: Vec<String>,
    /// How long a row stays present in a state, by state, in state order
    /// (D104).
    pub ttl_by_state: Vec<(String, i64)>,
    /// The Workflow use onMissed, onBeat and ttlByState read; None when the
    /// config reads none.
    pub status: Option<String>,
}

impl PresenceConfig {
    /// The ttl a state gives: ttlByState's for a state it lists, else ttlMs.
    pub fn ttl_of(&self, state: Option<&str>) -> i64 {
        state
            .and_then(|state| {
                self.ttl_by_state
                    .iter()
                    .find(|(known, _)| known == state)
                    .map(|(_, ms)| *ms)
            })
            .unwrap_or(self.ttl_ms)
    }

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the table's refusal of sweepMs, then the principal field
    /// and the Workflow, a rule's reason in the engine's words, which the
    /// core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<PresenceConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        if raw.get("sweepMs").is_some() {
            return Err(
                "sweepMs is the engine's runner's, which missed the instances past their deadline every sweepMs: on a table the API's @behaviorWorker runs each row's miss as its deadline falls due, so a table's Presence takes no sweepMs (D104)"
                    .into(),
            );
        }
        let principal_field = text(raw, "principalField");
        let Some(field) = target
            .fields()
            .iter()
            .find(|known| known.name == principal_field)
        else {
            let names: Vec<&str> = target
                .fields()
                .iter()
                .map(|known| known.name.as_str())
                .collect();
            return Err(format!(
                "principalField \"{principal_field}\" is not a field of {} (its fields: {})",
                target.type_name(),
                names.join(", ")
            )
            .into());
        };
        if !subjects::holds_subject(&field.type_name) {
            return Err(format!(
                "principalField \"{principal_field}\" is not a string field of {}",
                target.type_name()
            )
            .into());
        }
        let on_missed = raw.get("onMissed").map(StatusMove::parse);
        let on_beat = raw.get("onBeat").map(StatusMove::parse);
        // In state order (by code point), as a canonical config holds its
        // members, so a config reads alike however its host ordered them.
        let mut ttl_by_state: Vec<(String, i64)> = raw
            .get("ttlByState")
            .and_then(Value::as_object)
            .into_iter()
            .flatten()
            .map(|(state, ms)| (state.clone(), whole(ms).unwrap_or_default()))
            .collect();
        ttl_by_state.sort();
        let named = raw.get("status").and_then(Value::as_str);
        let reads_flow =
            on_missed.is_some() || on_beat.is_some() || !ttl_by_state.is_empty() || named.is_some();
        let flow = if reads_flow {
            moves::flow_for(target, named)?
        } else {
            None
        };
        match &flow {
            None => {
                if on_missed.is_some() {
                    return Err(moves::no_workflow("onMissed"));
                }
                if on_beat.is_some() {
                    return Err(moves::no_workflow("onBeat"));
                }
                if !ttl_by_state.is_empty() {
                    return Err(
                        "ttlByState times the states of Workflow, which the type does not list"
                            .into(),
                    );
                }
            }
            Some(flow) => check_flow(flow, &on_missed, &on_beat, &ttl_by_state)?,
        }
        Ok(PresenceConfig {
            ttl_ms: raw.get("ttlMs").and_then(whole).unwrap_or_default(),
            principal_field,
            principal_type: field.type_name.clone(),
            on_missed,
            on_beat,
            release_leases: moves::strings(raw.get("releaseLeases")),
            ttl_by_state,
            status: flow.map(|flow| flow.use_id),
        })
    }
}

// check_flow holds onMissed, onBeat and ttlByState to the type's Workflow,
// in that order, in the engine's words.
fn check_flow(
    flow: &Flow,
    on_missed: &Option<StatusMove>,
    on_beat: &Option<StatusMove>,
    ttl_by_state: &[(String, i64)],
) -> Result<(), ConfigError> {
    if let Some(rule) = on_missed {
        moves::check_move(flow, "onMissed", rule)?;
    }
    if let Some(rule) = on_beat {
        moves::check_move(flow, "onBeat", rule)?;
    }
    for (state, _) in ttl_by_state {
        if flow.lacks(state) {
            return Err(format!(
                "ttlByState: \"{state}\" is not a state of the type's Workflow ({})",
                flow.listed()
            )
            .into());
        }
    }
    Ok(())
}

/// Presence: a heartbeat on each row, which stands for a principal.
#[derive(Clone, Copy, Debug, Default)]
pub struct Presence;

impl Behavior for Presence {
    type Config = PresenceConfig;

    // The declaration file types its four fields, which a table keeps as
    // owned columns, and says it arms deadlines (D103, D104).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<PresenceConfig, ConfigError> {
        PresenceConfig::parse(raw, target)
    }

    // Any config may change but principalField on a type with rows;
    // Presence joins a type with rows and cannot leave one.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&PresenceConfig>,
        after: Option<&PresenceConfig>,
    ) -> Hook<Option<String>> {
        Ok(match (before, after) {
            (Some(_), None) => Some(
                "the deadlines and misses its instances hold would stay behind and come back if it were added again"
                    .to_owned(),
            ),
            (Some(before), Some(after))
                if before.principal_field != after.principal_field && cx.has_rows()? =>
            {
                Some(format!(
                    "principalField is where every instance holds its principal: it stays {}",
                    before.principal_field
                ))
            }
            _ => None,
        })
    }

    // beat and miss take the empty closed object.
    fn check_params(
        &self,
        _config: &PresenceConfig,
        _operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let mut issues = Issues::default();
        match params.as_object() {
            None => issues.push("", "type", "must be object"),
            Some(given) => {
                for key in given.keys() {
                    issues.push(
                        "",
                        "additionalProperties",
                        format!("must NOT have additional properties: {key}"),
                    );
                }
            }
        }
        issues.into_issues()
    }

    // A create is not missed, and without ttlByState is missed ttlMs after
    // it; with ttlByState the after-change hook arms it, once every
    // initializer has set the status.
    fn initialize(&self, cx: &Cx<'_, PresenceConfig>, _params: &Value) -> Hook<()> {
        cx.set("missed", false)?;
        let config = cx.config();
        if config.ttl_by_state.is_empty() {
            let deadline = cx.now() + config.ttl_ms;
            cx.set("deadline", deadline)?;
            cx.arm(MISS, deadline, Value::Null)?;
        }
        Ok(())
    }

    // guard keeps the row to its principal: a create sets principalField,
    // an update changes it only while it holds none.
    fn guard(
        &self,
        view: &View<'_, PresenceConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let GuardRequest::Update { after, .. } = request else {
            return Ok(None);
        };
        let field = &view.config().principal_field;
        let Some(before) = view.own_field(field) else {
            return Ok(None);
        };
        if values::equal(Some(&before), after.get(field)) {
            return Ok(None);
        }
        Ok(Some(
            Veto::new(format!(
                "{field} holds the principal the instance stands for, so it cannot change once set"
            ))
            .with_code("principal_fixed"),
        ))
    }

    fn operation(&self, cx: &Cx<'_, PresenceConfig>, name: &str, _params: &Value) -> Hook<Value> {
        match name {
            BEAT => beat(cx),
            MISS => miss(cx),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // A miss that falls due reconciles its row first, so one whose state a
    // raw write moved into a longer one is armed again rather than missed,
    // then misses through the use's own miss, so every use's guards run on
    // it, passing any refusal on.
    fn deadline(&self, cx: &Cx<'_, PresenceConfig>, name: &str, _payload: &Value) -> Hook<()> {
        if name != MISS {
            return Err(cx.defect(format!(
                "deadline {name} fell due, and Presence arms only miss"
            )));
        }
        if !cx.config().ttl_by_state.is_empty() && !is_missed(cx.get("missed").as_ref()) {
            if let Some(due) = due_of(cx).filter(|due| *due > cx.now()) {
                cx.set("deadline", due)?;
                cx.arm(MISS, due, Value::Null)?;
                return Ok(());
            }
        }
        cx.call(cx.use_id(), MISS, &json!({}))?;
        Ok(())
    }

    // With ttlByState, every write of the row through the core arms its miss
    // again when the deadline its state gives moved: a transition's entry, a
    // beat, a create once its status is set. A delete takes the row's
    // deadline with it.
    fn after_change(&self, cx: &Cx<'_, PresenceConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Delete)
            || cx.config().ttl_by_state.is_empty()
            || is_missed(cx.get("missed").as_ref())
        {
            return Ok(());
        }
        let Some(due) = due_of(cx) else {
            return Ok(());
        };
        if millis(cx.get("deadline").as_ref()) != Some(due) {
            cx.set("deadline", due)?;
            cx.arm(MISS, due, Value::Null)?;
        }
        Ok(())
    }
}

// beat moves the deadline on for the principal the row stands for, alone,
// clears missed, and moves the status back with onBeat.
fn beat(cx: &Cx<'_, PresenceConfig>) -> Hook<Value> {
    let config = cx.config();
    let field = &config.principal_field;
    let held = cx.own_field(field);
    if !matches!(&held, Some(Value::String(principal)) if !principal.is_empty()) {
        return Err(cx.veto(
            Veto::new(format!(
                "its {field} holds no principal, so no one may beat it"
            ))
            .with_code("no_principal"),
        ));
    }
    if !subjects::names_subject(&config.principal_type, held.as_ref(), &cx.caller().subject) {
        return Err(cx.veto(
            Veto::new("it stands for another principal, who alone may beat it")
                .with_code("not_principal"),
        ));
    }
    cx.set("lastBeatAt", cx.now())?;
    cx.set("missed", false)?;
    if let Some(status) = &config.status {
        moves::apply(
            cx,
            status,
            config.on_beat.as_ref(),
            status_of(cx, status).as_deref(),
        )?;
    }
    let state = config
        .status
        .as_ref()
        .and_then(|status| status_of(cx, status));
    let deadline = cx.now() + config.ttl_of(state.as_deref());
    cx.set("deadline", deadline)?;
    cx.arm(MISS, deadline, Value::Null)?;
    Ok(json!({ "deadline": deadline }))
}

// miss applies a miss of a row past its deadline and not missed yet, once:
// it sets missed, moves the status with onMissed, and expires the leases
// the principal holds on the tables releaseLeases names, unless another row
// that stands for it is present.
fn miss(cx: &Cx<'_, PresenceConfig>) -> Hook<Value> {
    let config = cx.config();
    let deadline = millis(cx.get("deadline").as_ref());
    if is_missed(cx.get("missed").as_ref()) || deadline.is_none_or(|deadline| cx.now() < deadline) {
        cx.unchanged()?;
        return Ok(json!({ "missed": false }));
    }
    cx.set("missed", true)?;
    if let Some(status) = &config.status {
        moves::apply(
            cx,
            status,
            config.on_missed.as_ref(),
            status_of(cx, status).as_deref(),
        )?;
    }
    let mut released = Map::new();
    if let (false, Some(Value::String(principal))) = (
        config.release_leases.is_empty(),
        cx.own_field(&config.principal_field),
    ) {
        if !principal.is_empty() {
            let rows = principal_rows(cx, &principal)?;
            if !present_elsewhere(cx, &rows) {
                let seen = last_seen(cx, &rows);
                for table in &config.release_leases {
                    let expired = release(cx, table, &principal, seen)?;
                    released.insert(table.clone(), Value::from(expired));
                }
            }
        }
    }
    let released = Value::Object(released);
    cx.set("released", released.clone())?;
    Ok(json!({ "missed": true, "released": released }))
}

// principal_rows is every row of the type whose principal field holds the
// principal, as the host holds them, this one among them, before the miss.
fn principal_rows(cx: &Cx<'_, PresenceConfig>, principal: &str) -> Hook<Vec<KeyedRow>> {
    let field = &cx.config().principal_field;
    let mut rows = Vec::new();
    let mut after: Option<String> = None;
    loop {
        let page = cx.read_linked(cx.type_name(), field, principal, after.as_deref())?;
        rows.extend(page.rows.iter().cloned());
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => return Ok(rows),
        }
    }
}

// present_elsewhere reports whether another row of the type stands for the
// principal and is present: not missed, and before its deadline. The
// leases are the principal's, so a miss of one of its rows leaves them while
// another still beats.
fn present_elsewhere(cx: &Cx<'_, PresenceConfig>, rows: &[KeyedRow]) -> bool {
    let use_id = cx.use_id();
    rows.iter()
        .filter(|row| Some(row.key.as_str()) != cx.key())
        .any(|row| {
            let owned = row.row.owned.get(use_id);
            let field = |name: &str| owned.and_then(|fields| fields.get(name));
            !is_missed(field("missed"))
                && millis(field("deadline")).is_some_and(|deadline| deadline > cx.now())
        })
}

// last_seen is the principal's last sight on any row of the type that
// stands for it (the engine's MAX(COALESCE(last_beat_at, created_at))): a
// row's last beat; with ttlByState, for a row not missed, the later of it
// and its status's entry into its state, which counts as a sighting; for a
// row with neither, its deadline less ttlMs, its create's arming. A lease
// renewed after it shows its holder alive, and the miss spares it.
fn last_seen(cx: &Cx<'_, PresenceConfig>, rows: &[KeyedRow]) -> i64 {
    let config = cx.config();
    let use_id = cx.use_id();
    rows.iter()
        .map(|row| {
            let owned = row.row.owned.get(use_id);
            let field = |name: &str| owned.and_then(|fields| fields.get(name));
            let mut sight = millis(field("lastBeatAt"));
            if let (false, Some(status)) = (config.ttl_by_state.is_empty(), &config.status) {
                if !is_missed(field("missed")) {
                    let entered = row
                        .row
                        .owned
                        .get(status)
                        .and_then(|fields| fields.get(workflow::ENTERED_AT));
                    sight = later(sight, millis(entered));
                }
            }
            sight
                .or_else(|| millis(field("deadline")).map(|deadline| deadline - config.ttl_ms))
                .unwrap_or(0)
        })
        .max()
        .unwrap_or(0)
}

// release expires the leases the principal holds on one table through
// Lease's own expire on each row, sparing one renewed after its last sight,
// and answers the keys it expired, in key order.
fn release(
    cx: &Cx<'_, PresenceConfig>,
    table: &str,
    principal: &str,
    seen: i64,
) -> Hook<Vec<String>> {
    let Some(spec) = cx.read_spec(table)? else {
        return Err(cx.defect(format!(
            "releaseLeases names {table}, which is no table of the core's layout"
        )));
    };
    let Some(lease) = spec
        .uses
        .iter()
        .find(|entry| entry.behavior == LEASE)
        .map(|entry| entry.id().to_owned())
    else {
        return Err(cx.defect(format!(
            "releaseLeases names {table}, which implements no Lease"
        )));
    };
    let mut held: BTreeMap<String, String> = BTreeMap::new();
    for form in subjects::subject_forms(&cx.config().principal_type, principal) {
        let value = Value::from(form.as_str());
        let mut after: Option<String> = None;
        loop {
            let page = cx.read_holding(table, &lease, LEASE_HOLDER, &value, after.as_deref())?;
            for row in &page.rows {
                held.entry(row.key.clone()).or_insert_with(|| form.clone());
            }
            match (page.more, page.rows.last()) {
                (true, Some(last)) => after = Some(last.key.clone()),
                _ => break,
            }
        }
    }
    let mut expired = Vec::new();
    // A row whose call waits on the host goes on to the next, so one round
    // answers every row's next read (Stop::is_waiting); the wait is
    // returned at the end, over a refusal after it.
    let mut waiting: Option<Stop> = None;
    for (key, form) in held {
        let answer = match cx.call_on(
            table,
            &key,
            &lease,
            LEASE_EXPIRE,
            &json!({ "holder": form, "notRenewedAfter": seen }),
        ) {
            Ok(answer) => answer,
            Err(stop) if stop.is_waiting() => {
                waiting.get_or_insert(stop);
                continue;
            }
            Err(_) if waiting.is_some() => continue,
            Err(stop) => return Err(stop),
        };
        if answer.get("expired") == Some(&Value::Bool(true)) {
            expired.push(key);
        }
    }
    if let Some(stop) = waiting {
        return Err(stop);
    }
    Ok(expired)
}

// due_of is the deadline a row's state gives under ttlByState: the later of
// its last beat and its status's entry into its state, plus that state's
// ttl; none when it has neither.
fn due_of(view: &View<'_, PresenceConfig>) -> Option<i64> {
    let config = view.config();
    let status = config.status.as_deref()?;
    let since = later(
        millis(view.get("lastBeatAt").as_ref()),
        millis(view.get_of(status, workflow::ENTERED_AT).as_ref()),
    )?;
    Some(since + config.ttl_of(status_of(view, status).as_deref()))
}

// status_of is the Workflow use's status, as the run has it.
fn status_of<C>(view: &View<'_, C>, status: &str) -> Option<String> {
    view.get_of(status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// later is the later of two times either may lack.
fn later(a: Option<i64>, b: Option<i64>) -> Option<i64> {
    match (a, b) {
        (Some(a), Some(b)) => Some(a.max(b)),
        (a, b) => a.or(b),
    }
}

// is_missed reads missed, a boolean as the core writes it; an adapter that
// read an integer column as its number reads 1 as missed.
fn is_missed(value: Option<&Value>) -> bool {
    match value {
        Some(Value::Bool(missed)) => *missed,
        Some(Value::Number(number)) => number.as_i64().is_some_and(|n| n != 0),
        _ => false,
    }
}

// millis reads epoch milliseconds: a whole number.
fn millis(value: Option<&Value>) -> Option<i64> {
    whole(value?)
}

// whole reads a whole number, as JSON may write one (2 or 2.0).
fn whole(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    number.as_i64().or_else(|| {
        number
            .as_f64()
            .filter(|n| n.is_finite() && n.fract() == 0.0)
            .map(|n| n as i64)
    })
}

fn text(raw: &Value, key: &str) -> String {
    raw.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

// ---- the configSchema, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    for key in ["ttlMs", "principalField"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    for key in ["ttlMs", "sweepMs"] {
        if let Some(ms) = config.get(key) {
            schema::integer_issues(
                &schema::member("", key),
                ms,
                Some(MS_MINIMUM),
                None,
                &mut issues,
            );
        }
    }
    for key in ["principalField", "status"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    for key in ["onMissed", "onBeat"] {
        if let Some(rule) = config.get(key) {
            move_issues(&schema::member("", key), rule, &mut issues);
        }
    }
    if let Some(tables) = config.get("releaseLeases") {
        tables_issues("/releaseLeases", tables, &mut issues);
    }
    if let Some(by_state) = config.get("ttlByState") {
        ttl_by_state_issues("/ttlByState", by_state, &mut issues);
    }
    issues
}

// move_issues lists what a status move breaks of `{ transition, from }`,
// both required, from a list of distinct states.
fn move_issues(at: &str, rule: &Value, issues: &mut Issues) {
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
        let at = schema::member(at, "from");
        let Some(items) = from.as_array() else {
            issues.push(&at, "type", "must be array");
            return;
        };
        if items.is_empty() {
            issues.push(&at, "minItems", "must NOT have fewer than 1 items");
        }
        for (i, item) in items.iter().enumerate() {
            schema::text_issues(&format!("{at}/{i}"), item, issues);
        }
        unique_issues(&at, items, issues);
    }
}

// tables_issues lists what releaseLeases breaks of a list of distinct table
// names.
fn tables_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    for (i, item) in items.iter().enumerate() {
        match item {
            Value::String(name) if !is_table_name(name) => issues.push(
                format!("{at}/{i}"),
                "pattern",
                format!("must match pattern \"{TABLE_PATTERN}\""),
            ),
            Value::String(_) => {}
            _ => issues.push(format!("{at}/{i}"), "type", "must be string"),
        }
    }
    unique_issues(at, items, issues);
}

// ttl_by_state_issues lists what ttlByState breaks of an object of at least
// one member, each an integer of at least 1000.
fn ttl_by_state_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(members) = value.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if members.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (state, ms) in members {
        schema::integer_issues(
            &schema::member(at, state),
            ms,
            Some(MS_MINIMUM),
            None,
            issues,
        );
    }
}

fn unique_issues(at: &str, items: &[Value], issues: &mut Issues) {
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// is_table_name says whether a name matches TABLE_PATTERN: a letter, then
// at most 127 letters, digits, underscores and hyphens, ASCII alone.
fn is_table_name(name: &str) -> bool {
    schema::is_name(name) && name.len() <= 128
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
