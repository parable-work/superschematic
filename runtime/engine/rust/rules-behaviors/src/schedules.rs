//! Schedules, named schedules on a row (D104): `implements Schedules<Problem,
//! { max, minMs?, maxMs?, kinds, timeZone?, outcomes, retry?, endOn?, status?,
//! permission? }>`. Like Seats and Reconcile, only the core implements it: no
//! engine registers it, and its declaration is the file `make behaviors`
//! writes for the package `@superschematic/rules`
//! (`rules-behaviors/declarations`), read in place.
//!
//! Schedules is held to D103's test of a behavior and passes it. Its state is
//! its own: the `schedules` sub-table, a row per named schedule with its
//! kind, spec, zone, state, anchor, next run, the retry of its latest, its
//! runs and attempts and the outcome reported, and the deadline each live one
//! arms. Its rules say which schedules a row may hold (kinds, bounds, max, one
//! live name each), how a run falls due and arms the next (an overdue one
//! fires once, a resumed one past its time fires at once, once), how a run's
//! outcome is recorded and tried again, and when a row's schedules end
//! (cancel, an after's one run, endOn). It recurs: a reminder, a recurring
//! invoice, a digest, a report's delivery, an agent's wake-ups.
//!
//! The rules:
//!
//! - `setSchedule({ name, after?, every?, cron?, timeZone? })` sets a schedule
//!   of exactly one kind the config's `kinds` take: `after`, one run that many
//!   milliseconds on; `every`, a run at each whole interval from the time it
//!   is set; `cron`, a run at each time a line names ([`crate::calendar`]),
//!   read in `timeZone`, the config's when absent, else UTC. A delay and an
//!   interval lie within `minMs..maxMs`; a cron line's runs come at least
//!   `minMs` apart on the local clock (its times of day, and the dates its
//!   day fields name, [`Cron::closest_ms`]), and its next run lies within
//!   `maxMs` of the set. A live schedule of the name (active or paused) with
//!   the same kind, spec (a line normalized) and zone answers its view
//!   `unchanged: true`, whatever its state; another is replaced, its old
//!   arming moved (`replaced: true`). A name with no live schedule needs
//!   room: the veto `schedule_limit` while the row holds `max` live ones. A
//!   finished name set again is a new schedule. A replace and a finished
//!   name set again count the name's runs on from its row's, so a run's
//!   number names one run of the name while its row lasts.
//! - Each active schedule arms the deadline of its name at its due, the retry
//!   of its latest run when one is armed, else its next run; the deadline's
//!   payload is `{}`, since the sub-table row is the truth the fire reads. The
//!   fire (the API's behavior worker runs it, D103) records a run and emits
//!   the event `run` (`{ name, run, attempt, kind, spec, timeZone?, setAt,
//!   dueAt, firedAt, nextAt? }`), which the table's event queue carries and
//!   the worker hands to the project's handler, idempotent by the row's key,
//!   the name, `setAt`, the run and the attempt, which no two fires share
//!   (a name pruned and set again has a later `setAt`); it arms the next
//!   run from the time it fired, so an overdue schedule fires once: an
//!   every's at the first point of its grid (`setAt + k * interval`) after
//!   the fire, a cron line's at its first run after the fire, and an after
//!   finishes (`endedBy: run`), as does a cron line with no run left within
//!   the calendar's horizon. A fire of a schedule that is no longer active,
//!   or is gone, does nothing: its arming is stale.
//! - `pauseSchedule` keeps the next run and the retry and clears the
//!   deadline; `resumeSchedule` arms it again at its due, which, past, falls
//!   due at once and fires once. `cancelSchedule` finishes a live schedule
//!   (`endedBy: cancel`) rather than deleting it, so its latest run's late
//!   report still lands, a read shows why it stopped, and it stops counting
//!   toward `max` at once. A repeat of each answers `unchanged: true`; a pause
//!   or a resume of a finished schedule is the veto `schedule_finished`.
//! - `reportRun({ name, run, attempt?, outcome })` records an outcome of
//!   `outcomes` of the latest run. A run the schedule has not had is the
//!   veto `no_run`; an older run, the latest run of a schedule the name's
//!   current one replaced (it has not fired), an attempt other than the
//!   latest, and the outcome recorded answer `unchanged: true`, since a
//!   handler delivered at least once reports again. The config's `retry`
//!   outcome tries the run again `afterMs` on, as its next attempt, armed
//!   unless the next regular run comes first; on an after or a cron line
//!   finished by its run, it revives the schedule for the try, unless the row
//!   is at `max` or ended. Another outcome cancels a retry armed for it.
//! - `endOn: { enters }` finishes every live schedule of a row whose Workflow,
//!   the one `status` names (or the type's one), is in a listed state
//!   (`endedBy: endOn`): on every write through the core and every write of
//!   the row its change queue carries, a raw one among them, as Timers'
//!   `enters` is heard. A row in such a state takes no live schedule: a set
//!   and a resume are the veto `schedules_ended`.
//! - `listSchedules({ name? })` reads the row's schedules by name, in code
//!   point order, live and finished.
//! - A write that finishes a schedule prunes the finished ones beyond `max`,
//!   those finished longest ago first (ties by name), so a row's sub-table
//!   stays within twice `max`.
//!
//! The parameter rules come first, then the permission (`permission`, which
//! every operation needs when the config names one), then the name's row,
//! the answers that change nothing, and the state's vetoes, all in
//! [`Behavior::settle`] before any other use's guard: a caller without the
//! permission learns nothing of the row, and no other use's gate refuses a
//! call that changes nothing. The fire calls no operation, so no guard is
//! asked of it: a lease another principal holds (Lease) never holds a run,
//! while the handler's `reportRun`, the worker's write, is held unless
//! Lease's `exempt` lists it.
//!
//! Values are kept as the sub-table's fields declare them: date-times in
//! epoch milliseconds, `runs` and `attempt` whole numbers (a decimal string
//! an adapter reads a BIGINT as is read too), a null one absent.
//!
//! The config is checked at define as every ported behavior's is: first as
//! the declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then by Schedules' own
//! rules ([`ConfigError::Rule`]): `minMs` within `maxMs`, a known `timeZone`
//! with cron among `kinds`, `retry` an outcome of `outcomes` within the
//! bounds, `status` with `endOn`, and `endOn`'s states those of the
//! Workflow it reads ([`crate::flow`]). The operations' parameters are held
//! to their closed paramsSchemas in the behavior registry's words. The core
//! runs no JSON Schema, so the schemas' checks are written here, held to the
//! declaration file by this crate's tests.
//!
//! configChange: any change is allowed, an added and a removed use included.
//! A dropped kind holds new sets alone, and a live schedule of it keeps
//! running; a lowered `max` refuses new names until fewer are live and
//! prunes finished ones to it at the next finish, finishing no live one; new
//! bounds hold new sets and retries alone; a dropped outcome stays recorded;
//! a new retry applies at the next report, a new endOn at the row's next
//! write, a new default zone at the next set. A removed use leaves its rows;
//! its armed deadlines fall due into `not_found`, and its queued events wait
//! for a handler. A write around the core (raw SQL of the sub-table or the
//! queues) runs none of these rules, the escape hatch D103 keeps: the next
//! fire reads the row's sub-table as it is, and arms the deadline again at a
//! due a raw write moved later.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Heard, Hook, Refusal,
    SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::calendar::{Cron, Zone};
use crate::declared;
use crate::flow::flow_of;
use crate::schema::{self, Issues};
use crate::timers::parse_date_time;
use crate::values::date_time_text;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Schedules";

/// Schedules' declaration, the file `make behaviors` writes for the core
/// alone (no engine implements it), read in place.
pub const DECLARATION: &str = include_str!("../declarations/Schedules.behavior.json");

/// The sub-table of a row's schedules, keyed by name.
pub const SCHEDULES: &str = "schedules";

/// The operation that sets a schedule.
pub const SET_SCHEDULE: &str = "setSchedule";

/// The operation that pauses a schedule.
pub const PAUSE_SCHEDULE: &str = "pauseSchedule";

/// The operation that resumes a schedule.
pub const RESUME_SCHEDULE: &str = "resumeSchedule";

/// The operation that finishes a schedule.
pub const CANCEL_SCHEDULE: &str = "cancelSchedule";

/// The operation that records a run's outcome.
pub const REPORT_RUN: &str = "reportRun";

/// The operation that reads a row's schedules.
pub const LIST_SCHEDULES: &str = "listSchedules";

/// The event a fire emits.
pub const RUN: &str = "run";

/// The least delay, interval and gap between a cron line's times of day,
/// when the config gives none: a minute.
pub const DEFAULT_MIN_MS: i64 = 60_000;

/// The greatest delay and interval, and how far off a cron line's next run
/// may be when it is set, when the config gives none: thirty days.
pub const DEFAULT_MAX_MS: i64 = 2_592_000_000;

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 10] = [
    "endOn",
    "kinds",
    "max",
    "maxMs",
    "minMs",
    "outcomes",
    "permission",
    "retry",
    "status",
    "timeZone",
];
const RETRY_KEYS: [&str; 2] = ["afterMs", "outcome"];
const END_ON_KEYS: [&str; 1] = ["enters"];

// The kinds the configSchema's enum takes, in its order.
const KINDS: [&str; 3] = ["after", "every", "cron"];

// What the configSchema holds max, the bounds and the outcomes to.
const MAX_MAX: i64 = 100;
const MIN_BOUND_MS: i64 = 1000;
const MAX_OUTCOMES: usize = 32;
const OUTCOME_PATTERN: &str = "^[a-z][a-z0-9_]*$";
const OUTCOME_MAX_LENGTH: usize = 64;

// The states of a schedule, and what finished one.
const ACTIVE: &str = "active";
const PAUSED: &str = "paused";
const FINISHED: &str = "finished";
const BY_RUN: &str = "run";
const BY_CANCEL: &str = "cancel";
const BY_END_ON: &str = "endOn";

/// A kind of schedule.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SchedulesKind {
    /// One run a delay after it is set.
    After,
    /// A run at each whole interval from when it is set.
    Every,
    /// A run at each time a cron line names.
    Cron,
}

impl SchedulesKind {
    /// The kind of its name: `after`, `every` or `cron`.
    pub fn named(name: &str) -> Option<SchedulesKind> {
        match name {
            "after" => Some(SchedulesKind::After),
            "every" => Some(SchedulesKind::Every),
            "cron" => Some(SchedulesKind::Cron),
            _ => None,
        }
    }

    /// Its name, as a schedule's `kind` holds it.
    pub fn name(self) -> &'static str {
        match self {
            SchedulesKind::After => "after",
            SchedulesKind::Every => "every",
            SchedulesKind::Cron => "cron",
        }
    }
}

/// The outcome that tries a run again, and how long after its report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SchedulesRetry {
    pub outcome: String,
    pub after_ms: i64,
}

/// Schedules' config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct SchedulesConfig {
    /// How many schedules a row holds live, active or paused.
    pub max: i64,
    pub min_ms: i64,
    pub max_ms: i64,
    /// The kinds a schedule may be, in the config's order.
    pub kinds: Vec<SchedulesKind>,
    /// The zone a cron line is read in when setSchedule names none: the
    /// config's, else UTC.
    pub time_zone: Zone,
    /// The outcomes reportRun records, in the config's order.
    pub outcomes: Vec<String>,
    pub retry: Option<SchedulesRetry>,
    /// The states endOn names (`endOn.enters`).
    pub end_on: Option<Vec<String>>,
    /// The Workflow use endOn reads, by its id, resolved through
    /// [`crate::flow`]; None without endOn.
    pub status: Option<String>,
    pub permission: Option<String>,
}

impl SchedulesConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then by Schedules' rules, a rule's reason which the core
    /// prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<SchedulesConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let number = |key: &str| raw.get(key).and_then(whole);
        let text = |key: &str| raw.get(key).and_then(Value::as_str).map(str::to_owned);
        let max = number("max").unwrap_or(1);
        let min_ms = number("minMs").unwrap_or(DEFAULT_MIN_MS);
        let max_ms = number("maxMs").unwrap_or(DEFAULT_MAX_MS);
        if min_ms > max_ms {
            return Err(format!(
                "minMs ({min_ms}) is more than maxMs ({max_ms}): no delay or interval lies between them"
            )
            .into());
        }
        let kinds: Vec<SchedulesKind> = strings(raw.get("kinds"))
            .iter()
            .filter_map(|kind| SchedulesKind::named(kind))
            .collect();
        let time_zone = match text("timeZone") {
            None => Zone::utc(),
            Some(name) => {
                let zone = Zone::named(&name).map_err(|_| {
                    format!(
                        "timeZone \"{name}\" is not a time zone of the IANA database the core bundles"
                    )
                })?;
                if !kinds.contains(&SchedulesKind::Cron) {
                    return Err(
                        "timeZone names the zone a cron line is read in, and kinds leaves out cron"
                            .into(),
                    );
                }
                zone
            }
        };
        let outcomes = strings(raw.get("outcomes"));
        let retry = match raw.get("retry") {
            None => None,
            Some(retry) => {
                let outcome = retry
                    .get("outcome")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
                let after_ms = retry.get("afterMs").and_then(whole).unwrap_or_default();
                if !outcomes.contains(&outcome) {
                    return Err(format!(
                        "retry.outcome \"{outcome}\" is not one of outcomes ({})",
                        outcomes.join(", ")
                    )
                    .into());
                }
                if after_ms < min_ms || after_ms > max_ms {
                    return Err(format!(
                        "retry.afterMs ({after_ms}) is outside minMs..maxMs ({min_ms}..{max_ms}): a retry is held to the bounds a schedule is"
                    )
                    .into());
                }
                Some(SchedulesRetry { outcome, after_ms })
            }
        };
        let named = text("status");
        let (end_on, status) = match raw.get("endOn") {
            None => {
                if named.is_some() {
                    return Err(
                        "status names the Workflow endOn reads, and the config gives no endOn"
                            .into(),
                    );
                }
                (None, None)
            }
            Some(end_on) => {
                let flow = flow_of(target, named.as_deref())?;
                let enters = strings(end_on.get("enters"));
                if let Some(state) = enters.iter().find(|state| flow.lacks(state)) {
                    return Err(format!(
                        "endOn.enters names \"{state}\", which is not a state of the type's Workflow ({})",
                        flow.listed()
                    )
                    .into());
                }
                (Some(enters), Some(flow.use_id))
            }
        };
        Ok(SchedulesConfig {
            max,
            min_ms,
            max_ms,
            kinds,
            time_zone,
            outcomes,
            retry,
            end_on,
            status,
            permission: text("permission"),
        })
    }

    /// Whether the config takes a kind of schedule.
    pub fn takes(&self, kind: SchedulesKind) -> bool {
        self.kinds.contains(&kind)
    }

    /// The kinds, as a refusal lists them.
    pub fn listed_kinds(&self) -> String {
        self.kinds
            .iter()
            .map(|kind| kind.name())
            .collect::<Vec<_>>()
            .join(", ")
    }

    /// Whether endOn names a state.
    pub fn ends_in(&self, state: &str) -> bool {
        self.end_on
            .as_ref()
            .is_some_and(|enters| enters.iter().any(|entered| entered == state))
    }
}

/// Schedules: named schedules on a row, each arming its next run, recording
/// its runs and their outcomes.
#[derive(Clone, Copy, Debug, Default)]
pub struct Schedules;

impl Behavior for Schedules {
    type Config = SchedulesConfig;

    // The declaration file names its schedules sub-table, that it arms
    // deadlines and hears its row's writes, its six operations, its event
    // run and its five vetoes.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<SchedulesConfig, ConfigError> {
        SchedulesConfig::parse(raw, target)
    }

    // Any change, an added and a removed use included: Schedules keeps no
    // owned field, and each config's rules hold the writes made under it.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&SchedulesConfig>,
        _after: Option<&SchedulesConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &SchedulesConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // settle answers its operations before any use's guard is asked (D104):
    // the parameter rules, the permission, the name's row, the answers that
    // change nothing, then the state's vetoes.
    fn settle(
        &self,
        view: &View<'_, SchedulesConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        let verb = match operation {
            SET_SCHEDULE => "set a schedule of",
            PAUSE_SCHEDULE => "pause a schedule of",
            RESUME_SCHEDULE => "resume a schedule of",
            CANCEL_SCHEDULE => "cancel a schedule of",
            REPORT_RUN => "report a run of a schedule of",
            LIST_SCHEDULES => "list the schedules of",
            _ => return Ok(None),
        };
        let config = view.config();
        let set = match operation {
            SET_SCHEDULE => Some(set_of(view, params)?),
            REPORT_RUN => {
                let outcome = text_of(params, "outcome");
                if !config.outcomes.iter().any(|known| *known == outcome) {
                    return Err(bad_param(
                        view,
                        operation,
                        vec![SchemaIssue::new(
                            "/outcome",
                            format!(
                                "\"{outcome}\" is not an outcome of {}'s Schedules ({})",
                                view.type_name(),
                                config.outcomes.join(", ")
                            ),
                        )],
                    ));
                }
                None
            }
            _ => None,
        };
        if let Some(permission) = &config.permission {
            if !view.can(permission)? {
                let who = match view.caller().subject.as_str() {
                    "" => "a caller with no subject",
                    subject => subject,
                };
                return Err(view.forbidden(format!(
                    "{who} may not {verb} {}: it needs permission {permission}",
                    row(view)
                )));
            }
        }
        if operation == LIST_SCHEDULES {
            return Ok(None);
        }
        let schedules = schedules_of(&view.sub_rows(SCHEDULES)?);
        let name = text_of(params, "name");
        let held = schedules.iter().find(|schedule| schedule.name == name);
        if let Some(set) = set {
            if let Some(live) = held.filter(|schedule| schedule.is_live()) {
                if live.kind == set.kind.name()
                    && live.spec == set.spec
                    && live.time_zone == set.zone.as_ref().map(|zone| zone.name().to_owned())
                {
                    return Ok(Some(flagged(live.view(), "unchanged")));
                }
            }
            ended(view)?;
            if held.is_none_or(|schedule| !schedule.is_live()) {
                let live: Vec<&str> = schedules
                    .iter()
                    .filter(|schedule| schedule.is_live())
                    .map(|schedule| schedule.name.as_str())
                    .collect();
                if live.len() as i64 >= config.max {
                    let mut details = Map::new();
                    details.insert("max".into(), Value::from(config.max));
                    details.insert("live".into(), json!(live));
                    return Err(view.veto(
                        Veto::new(format!(
                            "it holds {} live schedules ({}), and its max is {}: cancel one to set another",
                            live.len(),
                            live.join(", "),
                            config.max
                        ))
                        .with_code("schedule_limit")
                        .with_details(details),
                    ));
                }
            }
            return Ok(None);
        }
        let Some(schedule) = held else {
            let names: Vec<&str> = schedules
                .iter()
                .map(|schedule| schedule.name.as_str())
                .collect();
            let mut details = Map::new();
            details.insert("name".into(), Value::from(name));
            details.insert("schedules".into(), json!(names));
            return Err(view.veto(
                Veto::new(format!(
                    "it has no schedule named \"{name}\" (its schedules: {})",
                    if names.is_empty() {
                        "none".to_owned()
                    } else {
                        names.join(", ")
                    }
                ))
                .with_code("no_schedule")
                .with_details(details),
            ));
        };
        match operation {
            PAUSE_SCHEDULE => match schedule.state.as_str() {
                PAUSED => Ok(Some(flagged(schedule.view(), "unchanged"))),
                ACTIVE => Ok(None),
                _ => Err(finished(view, schedule)),
            },
            RESUME_SCHEDULE => {
                ended(view)?;
                match schedule.state.as_str() {
                    ACTIVE => Ok(Some(flagged(schedule.view(), "unchanged"))),
                    PAUSED => Ok(None),
                    _ => Err(finished(view, schedule)),
                }
            }
            CANCEL_SCHEDULE => {
                Ok((!schedule.is_live()).then(|| flagged(schedule.view(), "unchanged")))
            }
            _ => {
                let run = params.get("run").and_then(whole).unwrap_or_default();
                let attempt = params.get("attempt").and_then(whole);
                let outcome = text_of(params, "outcome");
                if run > schedule.runs {
                    let mut details = Map::new();
                    details.insert("name".into(), Value::from(name));
                    details.insert("run".into(), Value::from(run));
                    details.insert("runs".into(), Value::from(schedule.runs));
                    return Err(view.veto(
                        Veto::new(format!(
                            "its schedule \"{name}\" has had {}, and no run {run} to report",
                            match schedule.runs {
                                1 => "1 run".to_owned(),
                                runs => format!("{runs} runs"),
                            }
                        ))
                        .with_code("no_run")
                        .with_details(details),
                    ));
                }
                // An older run; the latest run of a schedule this one
                // replaced, since this one has not fired; or an attempt
                // other than the latest: a late report, which changes
                // nothing.
                if run < schedule.runs
                    || schedule.last_at.is_none()
                    || attempt.is_some_and(|attempt| schedule.attempt != Some(attempt))
                {
                    return Ok(Some(json!({
                        "name": name, "run": run, "outcome": outcome, "unchanged": true
                    })));
                }
                if schedule.last_outcome.as_deref() == Some(outcome) {
                    let mut result = report(name, run, outcome, schedule.retry_at);
                    result.insert("unchanged".into(), Value::Bool(true));
                    return Ok(Some(Value::Object(result)));
                }
                Ok(None)
            }
        }
    }

    fn operation(&self, cx: &Cx<'_, SchedulesConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            SET_SCHEDULE => set_schedule(cx, params),
            PAUSE_SCHEDULE => pause_schedule(cx, params),
            RESUME_SCHEDULE => resume_schedule(cx, params),
            CANCEL_SCHEDULE => cancel_schedule(cx, params),
            REPORT_RUN => report_run(cx, params),
            LIST_SCHEDULES => list_schedules(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // The fire: see the module's header.
    fn deadline(&self, cx: &Cx<'_, SchedulesConfig>, name: &str, _payload: &Value) -> Hook<()> {
        fire(cx, name)
    }

    // endOn on every write through the core but a create and a delete: a
    // Workflow transition, an update, a deadline, a heard write's own.
    fn after_change(&self, cx: &Cx<'_, SchedulesConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Create | Change::Delete) {
            return Ok(());
        }
        end_on(cx)
    }

    // endOn on a write of the row its change queue carries: a raw SQL
    // statement's, a bulk write's, the core's own (which finds none live).
    fn heard(&self, cx: &Cx<'_, SchedulesConfig>, heard: &Heard<'_>) -> Hook<()> {
        if !matches!(heard, Heard::Own { .. }) {
            return Ok(());
        }
        end_on(cx)
    }
}

// ---- a schedule's row ----

/// A schedule as its sub-table row holds it.
#[derive(Clone, Debug, PartialEq)]
struct Schedule {
    name: String,
    kind: String,
    spec: String,
    time_zone: Option<String>,
    state: String,
    set_at: Option<i64>,
    next_at: Option<i64>,
    retry_at: Option<i64>,
    runs: i64,
    attempt: Option<i64>,
    last_at: Option<i64>,
    last_outcome: Option<String>,
    finished_at: Option<i64>,
    ended_by: Option<String>,
}

impl Schedule {
    // of reads a sub-table row.
    fn of(row: &SubRow) -> Schedule {
        let text = |field: &str| row.value(field).and_then(Value::as_str).map(str::to_owned);
        let at = |field: &str| millis(row.value(field));
        Schedule {
            name: row.key.clone(),
            kind: text("kind").unwrap_or_default(),
            spec: text("spec").unwrap_or_default(),
            time_zone: text("timeZone"),
            state: text("state").unwrap_or_default(),
            set_at: at("setAt"),
            next_at: at("nextAt"),
            retry_at: at("retryAt"),
            runs: row.value("runs").and_then(integer).unwrap_or(0),
            attempt: row.value("attempt").and_then(integer),
            last_at: at("lastAt"),
            last_outcome: text("lastOutcome"),
            finished_at: at("finishedAt"),
            ended_by: text("endedBy"),
        }
    }

    /// Whether it counts toward max: active or paused.
    fn is_live(&self) -> bool {
        self.state == ACTIVE || self.state == PAUSED
    }

    /// When its deadline falls due: the retry of its latest run when one is
    /// armed, else its next run.
    fn due(&self) -> Option<i64> {
        self.retry_at.or(self.next_at)
    }

    /// Its values as its sub-table row holds them, by declared field, null
    /// for a field with no value.
    fn values(&self) -> Map<String, Value> {
        let mut values = Map::new();
        let text = |value: &Option<String>| value.as_deref().map_or(Value::Null, Value::from);
        let at = |value: Option<i64>| value.map_or(Value::Null, Value::from);
        values.insert("kind".into(), Value::from(self.kind.as_str()));
        values.insert("spec".into(), Value::from(self.spec.as_str()));
        values.insert("timeZone".into(), text(&self.time_zone));
        values.insert("state".into(), Value::from(self.state.as_str()));
        values.insert("setAt".into(), at(self.set_at));
        values.insert("nextAt".into(), at(self.next_at));
        values.insert("retryAt".into(), at(self.retry_at));
        values.insert("runs".into(), Value::from(self.runs));
        values.insert("attempt".into(), at(self.attempt));
        values.insert("lastAt".into(), at(self.last_at));
        values.insert("lastOutcome".into(), text(&self.last_outcome));
        values.insert("finishedAt".into(), at(self.finished_at));
        values.insert("endedBy".into(), text(&self.ended_by));
        values
    }

    /// Its view, what every operation but reportRun answers: its values, a
    /// field with no value left out.
    fn view(&self) -> Map<String, Value> {
        let mut view = Map::new();
        view.insert("name".into(), Value::from(self.name.as_str()));
        for (field, value) in self.values() {
            if !value.is_null() {
                view.insert(field, value);
            }
        }
        view
    }

    /// Finishes it, by what finished it, at a time.
    fn finish(&mut self, by: &str, now: i64) {
        self.state = FINISHED.to_owned();
        self.ended_by = Some(by.to_owned());
        self.finished_at = Some(now);
        self.next_at = None;
        self.retry_at = None;
    }
}

// schedules_of reads a sub-table's rows, by name in code point order.
fn schedules_of(rows: &[SubRow]) -> Vec<Schedule> {
    let mut schedules: Vec<Schedule> = rows.iter().map(Schedule::of).collect();
    schedules.sort_by(|a, b| a.name.cmp(&b.name));
    schedules
}

// write writes a schedule's row: an update of the row of its name, or an
// insert of one its values with no value left out.
fn write(cx: &Cx<'_, SchedulesConfig>, schedule: &Schedule, exists: bool) -> Hook<()> {
    if exists {
        return cx.update_row(SCHEDULES, &schedule.name, schedule.values());
    }
    let values = schedule
        .values()
        .into_iter()
        .filter(|(_, value)| !value.is_null())
        .collect();
    cx.insert_row(SCHEDULES, &schedule.name, values)
}

// read is the schedule of a name, as the run has the row's sub-table.
fn read(cx: &Cx<'_, SchedulesConfig>, name: &str) -> Hook<Option<Schedule>> {
    Ok(schedules_of(&cx.sub_rows(SCHEDULES)?)
        .into_iter()
        .find(|schedule| schedule.name == name))
}

// held is the schedule an operation names, which settle found.
fn held(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Schedule> {
    let name = text_of(params, "name");
    read(cx, name)?
        .ok_or_else(|| cx.defect(format!("{name} reached its handler without its settle")))
}

// ---- setSchedule ----

/// A set's schedule, as its parameters and the config give it.
struct Set {
    kind: SchedulesKind,
    /// The delay or interval's digits, or the line normalized.
    spec: String,
    /// The zone a cron line is read in; None for after and every.
    zone: Option<Zone>,
    /// Its first run.
    next_at: i64,
}

// set_of reads a setSchedule's parameters against the config, its first
// group of rules that applies refusing it as invalid parameters: the kinds
// named, a kind the config leaves out, a zone given to an after or an
// every, a delay or an interval out of bounds, then a cron line and its
// zone, and the line's runs against the bounds.
fn set_of(view: &View<'_, SchedulesConfig>, params: &Value) -> Hook<Set> {
    let config = view.config();
    let refuse = |path: &str, message: String| {
        bad_param(view, SET_SCHEDULE, vec![SchemaIssue::new(path, message)])
    };
    let named: Vec<SchedulesKind> = KINDS
        .iter()
        .filter(|kind| params.get(**kind).is_some())
        .filter_map(|kind| SchedulesKind::named(kind))
        .collect();
    let kind = match named.as_slice() {
        [] => {
            return Err(refuse(
                "",
                "names none of after, every and cron: a schedule is one of them".to_owned(),
            ))
        }
        [kind] => *kind,
        several => {
            let names: Vec<&str> = several.iter().map(|kind| kind.name()).collect();
            return Err(refuse(
                "",
                format!(
                    "names {}: a schedule is one of after, every and cron",
                    both(&names)
                ),
            ));
        }
    };
    if !config.takes(kind) {
        return Err(refuse(
            &format!("/{}", kind.name()),
            format!(
                "{}'s Schedules takes no {} schedule: its kinds are {}",
                view.type_name(),
                kind.name(),
                config.listed_kinds()
            ),
        ));
    }
    let given_zone = params.get("timeZone").and_then(Value::as_str);
    if kind != SchedulesKind::Cron {
        if given_zone.is_some() {
            return Err(refuse(
                "/timeZone",
                format!(
                    "names the zone a cron line is read in, and an {} schedule reads none",
                    kind.name()
                ),
            ));
        }
        let n = params.get(kind.name()).and_then(whole).unwrap_or_default();
        if n < config.min_ms || n > config.max_ms {
            return Err(refuse(
                &format!("/{}", kind.name()),
                format!(
                    "{n} is outside minMs..maxMs ({}..{}): {} lies between them",
                    config.min_ms,
                    config.max_ms,
                    if kind == SchedulesKind::After {
                        "a delay"
                    } else {
                        "an interval"
                    }
                ),
            ));
        }
        return Ok(Set {
            kind,
            spec: n.to_string(),
            zone: None,
            next_at: view.now().saturating_add(n),
        });
    }
    let line = text_of(params, "cron");
    let cron = Cron::parse(line);
    let zone = match given_zone {
        Some(name) => Zone::named(name),
        None => Ok(config.time_zone.clone()),
    };
    let (cron, zone) = match (cron, zone) {
        (Ok(cron), Ok(zone)) => (cron, zone),
        (cron, zone) => {
            let mut issues = Vec::new();
            if let Err(message) = cron {
                issues.push(SchemaIssue::new("/cron", message));
            }
            if let Err(message) = zone {
                issues.push(SchemaIssue::new("/timeZone", message));
            }
            return Err(bad_param(view, SET_SCHEDULE, issues));
        }
    };
    let closest = cron.closest_ms();
    if closest < config.min_ms {
        return Err(refuse(
            "/cron",
            format!(
                "its runs come {closest} ms apart at their closest, under minMs ({}): a schedule's runs are at least minMs apart",
                config.min_ms
            ),
        ));
    }
    let Some(next_at) = cron.next_after(&zone, view.now()) else {
        return Err(refuse("/cron", "it has no run within ten years".to_owned()));
    };
    if next_at - view.now() > config.max_ms {
        return Err(refuse(
            "/cron",
            format!(
                "its next run, {}, is more than maxMs ({} ms) away: a schedule's next run is within maxMs when it is set",
                instant(next_at),
                config.max_ms
            ),
        ));
    }
    Ok(Set {
        kind,
        spec: cron.line().to_owned(),
        zone: Some(zone),
        next_at,
    })
}

// set_schedule writes the schedule a set names, a new one or the one it
// replaces, and arms its first run. The name's runs count on from its row's,
// a replaced or a finished schedule's, so no run number of the name comes
// twice while its row lasts.
fn set_schedule(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let set = set_of(cx, params)?;
    let name = text_of(params, "name");
    let before = read(cx, name)?;
    let runs = before.as_ref().map_or(0, |before| before.runs);
    let schedule = Schedule {
        name: name.to_owned(),
        kind: set.kind.name().to_owned(),
        spec: set.spec,
        time_zone: set.zone.map(|zone| zone.name().to_owned()),
        state: ACTIVE.to_owned(),
        set_at: Some(cx.now()),
        next_at: Some(set.next_at),
        retry_at: None,
        runs,
        attempt: None,
        last_at: None,
        last_outcome: None,
        finished_at: None,
        ended_by: None,
    };
    write(cx, &schedule, before.is_some())?;
    cx.arm(name, set.next_at, json!({}))?;
    let mut view = schedule.view();
    if before.is_some_and(|before| before.is_live()) {
        view.insert("replaced".into(), Value::Bool(true));
    }
    Ok(Value::Object(view))
}

// ---- pause, resume and cancel ----

fn pause_schedule(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let mut schedule = held(cx, params)?;
    schedule.state = PAUSED.to_owned();
    write(cx, &schedule, true)?;
    cx.clear(&schedule.name)?;
    Ok(Value::Object(schedule.view()))
}

fn resume_schedule(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let mut schedule = held(cx, params)?;
    schedule.state = ACTIVE.to_owned();
    write(cx, &schedule, true)?;
    if let Some(due) = schedule.due() {
        cx.arm(&schedule.name, due, json!({}))?;
    }
    Ok(Value::Object(schedule.view()))
}

fn cancel_schedule(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let mut schedule = held(cx, params)?;
    schedule.finish(BY_CANCEL, cx.now());
    write(cx, &schedule, true)?;
    cx.clear(&schedule.name)?;
    prune(cx)?;
    Ok(Value::Object(schedule.view()))
}

// ---- reportRun ----

// report_run records the latest run's outcome, which settle held to be new:
// the retry outcome tries the run again, another cancels a try armed.
fn report_run(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let mut schedule = held(cx, params)?;
    let run = params.get("run").and_then(whole).unwrap_or_default();
    let outcome = text_of(params, "outcome");
    let now = cx.now();
    schedule.last_outcome = Some(outcome.to_owned());
    let retry = config
        .retry
        .as_ref()
        .filter(|retry| retry.outcome == outcome);
    let mut finished = false;
    match retry {
        Some(retry) => {
            let retry_at = now.saturating_add(retry.after_ms);
            // A retry the next regular run comes first of is not armed.
            let first = schedule.next_at.is_none_or(|next_at| retry_at < next_at);
            match schedule.state.as_str() {
                ACTIVE | PAUSED => {
                    let had = schedule.retry_at.is_some();
                    schedule.retry_at = first.then_some(retry_at);
                    if schedule.state == ACTIVE {
                        if first {
                            cx.arm(&schedule.name, retry_at, json!({}))?;
                        } else if had {
                            rearm(cx, &schedule)?;
                        }
                    }
                }
                // An after, or a cron line that ran out, revives for the
                // try, unless the row is at max or ended.
                _ if schedule.ended_by.as_deref() == Some(BY_RUN) => {
                    let schedules = schedules_of(&cx.sub_rows(SCHEDULES)?);
                    let live = schedules.iter().filter(|held| held.is_live()).count() as i64;
                    if live < config.max && !is_ended(cx) {
                        schedule.state = ACTIVE.to_owned();
                        schedule.finished_at = None;
                        schedule.ended_by = None;
                        schedule.retry_at = Some(retry_at);
                        cx.arm(&schedule.name, retry_at, json!({}))?;
                    }
                }
                _ => {}
            }
        }
        None if schedule.retry_at.is_some() => {
            schedule.retry_at = None;
            if schedule.state == ACTIVE {
                if schedule.next_at.is_some() {
                    rearm(cx, &schedule)?;
                } else {
                    // A revived after, or line, finishes again.
                    schedule.finish(BY_RUN, now);
                    cx.clear(&schedule.name)?;
                    finished = true;
                }
            }
        }
        None => {}
    }
    write(cx, &schedule, true)?;
    if finished {
        prune(cx)?;
    }
    Ok(Value::Object(report(
        &schedule.name,
        run,
        outcome,
        schedule.retry_at,
    )))
}

// report is what reportRun answers: the schedule, the run and the outcome,
// and the retry armed, when one is.
fn report(name: &str, run: i64, outcome: &str, retry_at: Option<i64>) -> Map<String, Value> {
    let mut result = Map::new();
    result.insert("name".into(), Value::from(name));
    result.insert("run".into(), Value::from(run));
    result.insert("outcome".into(), Value::from(outcome));
    if let Some(retry_at) = retry_at {
        result.insert("retryAt".into(), Value::from(retry_at));
    }
    result
}

// rearm arms an active schedule's deadline at its due, or clears it when it
// has none.
fn rearm(cx: &Cx<'_, SchedulesConfig>, schedule: &Schedule) -> Hook<()> {
    match schedule.due() {
        Some(due) => cx.arm(&schedule.name, due, json!({})),
        None => cx.clear(&schedule.name),
    }
}

// ---- listSchedules ----

fn list_schedules(cx: &Cx<'_, SchedulesConfig>, params: &Value) -> Hook<Value> {
    let only = params.get("name").and_then(Value::as_str);
    let items: Vec<Value> = schedules_of(&cx.sub_rows(SCHEDULES)?)
        .iter()
        .filter(|schedule| only.is_none_or(|name| name == schedule.name))
        .map(|schedule| Value::Object(schedule.view()))
        .collect();
    Ok(json!({ "items": items }))
}

// ---- the fire ----

// fire runs a schedule's deadline: a stale arming does nothing, an ended row
// finishes its schedules, a due a raw write moved later is armed again, and
// a due run or retry fires, records its run and emits it.
fn fire(cx: &Cx<'_, SchedulesConfig>, name: &str) -> Hook<()> {
    let Some(mut schedule) = read(cx, name)? else {
        return Ok(());
    };
    if schedule.state != ACTIVE {
        return Ok(());
    }
    if is_ended(cx) {
        return end_on(cx);
    }
    let Some(due) = schedule.due() else {
        return Ok(());
    };
    let now = cx.now();
    if due > now {
        return cx.arm(name, due, json!({}));
    }
    // A retry whose run's next regular one is due too gives way to it.
    let retrying = schedule.retry_at.is_some() && schedule.next_at.is_none_or(|next| next > now);
    let (due_at, run, attempt) = if retrying {
        // The run's next attempt; its next regular run stays.
        let attempt = schedule.attempt.unwrap_or(1).saturating_add(1);
        (schedule.retry_at.unwrap_or(due), schedule.runs, attempt)
    } else {
        let due_at = schedule.next_at.unwrap_or(due);
        schedule.runs = schedule.runs.saturating_add(1);
        schedule.next_at = next_run(&schedule, now);
        (due_at, schedule.runs, 1)
    };
    schedule.retry_at = None;
    schedule.attempt = Some(attempt);
    schedule.last_at = Some(now);
    schedule.last_outcome = None;
    let finished = schedule.next_at.is_none();
    if finished {
        schedule.finish(BY_RUN, now);
    }
    let mut payload = Map::new();
    payload.insert("name".into(), Value::from(name));
    payload.insert("run".into(), Value::from(run));
    payload.insert("attempt".into(), Value::from(attempt));
    payload.insert("kind".into(), Value::from(schedule.kind.as_str()));
    payload.insert("spec".into(), Value::from(schedule.spec.as_str()));
    if schedule.kind == SchedulesKind::Cron.name() {
        if let Some(zone) = &schedule.time_zone {
            payload.insert("timeZone".into(), Value::from(zone.as_str()));
        }
    }
    // A set writes setAt; a row a raw write left without it has its run's
    // due instead, which every delivery of the event carries alike.
    payload.insert(
        "setAt".into(),
        Value::from(schedule.set_at.unwrap_or(due_at)),
    );
    payload.insert("dueAt".into(), Value::from(due_at));
    payload.insert("firedAt".into(), Value::from(now));
    if let Some(next_at) = schedule.next_at {
        payload.insert("nextAt".into(), Value::from(next_at));
    }
    write(cx, &schedule, true)?;
    match schedule.next_at {
        Some(next_at) => cx.arm(name, next_at, json!({}))?,
        None => cx.clear(name)?,
    }
    cx.emit(RUN, Value::Object(payload))?;
    if finished {
        prune(cx)?;
    }
    Ok(())
}

// next_run is a schedule's next regular run after a fire at a time: none for
// an after; an every's first point of its grid from setAt after the fire; a
// cron line's first run after it, none within the horizon. A spec or a zone
// a raw write made unreadable has none.
fn next_run(schedule: &Schedule, now: i64) -> Option<i64> {
    match SchedulesKind::named(&schedule.kind)? {
        SchedulesKind::After => None,
        SchedulesKind::Every => {
            let every: i64 = schedule.spec.parse().ok().filter(|every| *every > 0)?;
            let set_at = schedule.set_at.unwrap_or(now);
            let past = now.saturating_sub(set_at).max(0) / every;
            Some(set_at.saturating_add(every.saturating_mul(past + 1)))
        }
        SchedulesKind::Cron => {
            let cron = Cron::parse(&schedule.spec).ok()?;
            let zone = match &schedule.time_zone {
                Some(name) => Zone::named(name).ok()?,
                None => Zone::utc(),
            };
            cron.next_after(&zone, now)
        }
    }
}

// ---- endOn and pruning ----

// status is the status of the Workflow endOn reads, as the run has it.
fn status<C>(view: &View<'_, C>, use_id: &str) -> Option<String> {
    view.get_of(use_id, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// is_ended says whether endOn names the row's status.
fn is_ended(view: &View<'_, SchedulesConfig>) -> bool {
    let config = view.config();
    config
        .status
        .as_deref()
        .and_then(|use_id| status(view, use_id))
        .is_some_and(|status| config.ends_in(&status))
}

// ended refuses a set or a resume on a row whose status endOn names.
fn ended(view: &View<'_, SchedulesConfig>) -> Hook<()> {
    let config = view.config();
    let Some(status) = config
        .status
        .as_deref()
        .and_then(|use_id| status(view, use_id))
        .filter(|status| config.ends_in(status))
    else {
        return Ok(());
    };
    let mut details = Map::new();
    details.insert("status".into(), Value::from(status.as_str()));
    Err(view.veto(
        Veto::new(format!(
            "its status is {status}, which ends its schedules (endOn): it takes no live schedule"
        ))
        .with_code("schedules_ended")
        .with_details(details),
    ))
}

// end_on finishes every live schedule of a row whose status endOn names,
// clearing their deadlines, then prunes; a row with none live writes
// nothing.
fn end_on(cx: &Cx<'_, SchedulesConfig>) -> Hook<()> {
    if !is_ended(cx) {
        return Ok(());
    }
    let mut ended = false;
    for mut schedule in schedules_of(&cx.sub_rows(SCHEDULES)?) {
        if !schedule.is_live() {
            continue;
        }
        schedule.finish(BY_END_ON, cx.now());
        write(cx, &schedule, true)?;
        cx.clear(&schedule.name)?;
        ended = true;
    }
    if ended {
        prune(cx)?;
    }
    Ok(())
}

// prune deletes the finished schedules beyond max, those finished longest
// ago first, ties by name.
fn prune(cx: &Cx<'_, SchedulesConfig>) -> Hook<()> {
    let max = usize::try_from(cx.config().max).unwrap_or(0);
    let mut finished: Vec<Schedule> = schedules_of(&cx.sub_rows(SCHEDULES)?)
        .into_iter()
        .filter(|schedule| !schedule.is_live())
        .collect();
    if finished.len() <= max {
        return Ok(());
    }
    finished.sort_by(|a, b| {
        a.finished_at
            .unwrap_or(i64::MIN)
            .cmp(&b.finished_at.unwrap_or(i64::MIN))
            .then_with(|| a.name.cmp(&b.name))
    });
    let over = finished.len() - max;
    for schedule in finished.iter().take(over) {
        cx.delete_row(SCHEDULES, &schedule.name)?;
    }
    Ok(())
}

// ---- refusals and values ----

// finished is the veto of a pause or a resume of a finished schedule.
fn finished(view: &View<'_, SchedulesConfig>, schedule: &Schedule) -> Stop {
    let mut details = Map::new();
    details.insert("name".into(), Value::from(schedule.name.as_str()));
    view.veto(
        Veto::new(format!(
            "its schedule \"{}\" is finished: set it again to run it",
            schedule.name
        ))
        .with_code("schedule_finished")
        .with_details(details),
    )
}

// bad_param refuses an operation's parameters, as invalid arguments.
fn bad_param<C>(view: &View<'_, C>, operation: &str, issues: Vec<SchemaIssue>) -> Stop {
    Refusal::operation_params(view.behavior(), view.use_id(), operation, issues).into()
}

// flagged is a view with a flag of the answer set: unchanged or replaced.
fn flagged(mut view: Map<String, Value>, flag: &str) -> Value {
    view.insert(flag.into(), Value::Bool(true));
    Value::Object(view)
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// both lists names as a refusal says them: `a and b`, `a, b and c`.
fn both(names: &[&str]) -> String {
    match names {
        [] => String::new(),
        [one] => (*one).to_owned(),
        [rest @ .., last] => format!("{} and {last}", rest.join(", ")),
    }
}

fn text_of<'p>(params: &'p Value, key: &str) -> &'p str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

// strings reads a list of strings a schema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
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

// integer reads a whole number as a row holds it: a JSON number, or a
// decimal string, as an adapter may read a BIGINT.
fn integer(value: &Value) -> Option<i64> {
    match value {
        Value::String(text) => text.parse().ok(),
        other => whole(other),
    }
}

// millis reads a date-time as a row holds it: epoch milliseconds, a decimal
// string of them, or ISO 8601 text.
fn millis(value: Option<&Value>) -> Option<i64> {
    match value? {
        Value::String(text) => text.parse().ok().or_else(|| parse_date_time(text)),
        other => whole(other),
    }
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
    for key in ["max", "kinds", "outcomes"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(max) = config.get("max") {
        schema::integer_issues("/max", max, Some(1), Some(MAX_MAX), &mut issues);
    }
    for key in ["minMs", "maxMs"] {
        if let Some(bound) = config.get(key) {
            schema::integer_issues(
                &schema::member("", key),
                bound,
                Some(MIN_BOUND_MS),
                None,
                &mut issues,
            );
        }
    }
    if let Some(kinds) = config.get("kinds") {
        kinds_issues(kinds, &mut issues);
    }
    if let Some(outcomes) = config.get("outcomes") {
        outcomes_issues(outcomes, &mut issues);
    }
    if let Some(retry) = config.get("retry") {
        retry_issues(retry, &mut issues);
    }
    if let Some(end_on) = config.get("endOn") {
        end_on_issues(end_on, &mut issues);
    }
    for key in ["timeZone", "status", "permission"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    issues
}

fn kinds_issues(kinds: &Value, issues: &mut Issues) {
    let at = "/kinds";
    let Some(items) = kinds.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        if !item.as_str().is_some_and(|kind| KINDS.contains(&kind)) {
            issues.push(
                format!("{at}/{i}"),
                "enum",
                format!("must be one of {}", KINDS.map(schema::quoted).join(", ")),
            );
        }
    }
    if let Some((j, i)) = schema::duplicate_value(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

fn outcomes_issues(outcomes: &Value, issues: &mut Issues) {
    let at = "/outcomes";
    let Some(items) = outcomes.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if items.len() > MAX_OUTCOMES {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {MAX_OUTCOMES} items"),
        );
    }
    for (i, item) in items.iter().enumerate() {
        let at = format!("{at}/{i}");
        let Some(outcome) = item.as_str() else {
            issues.push(at, "type", "must be string");
            continue;
        };
        if schema::length(outcome) > OUTCOME_MAX_LENGTH {
            issues.push(
                &at,
                "maxLength",
                format!("must NOT have more than {OUTCOME_MAX_LENGTH} characters"),
            );
        }
        if !is_outcome(outcome) {
            issues.push(
                &at,
                "pattern",
                format!("must match pattern \"{OUTCOME_PATTERN}\""),
            );
        }
    }
    if let Some((j, i)) = schema::duplicate_value(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// is_outcome says whether an outcome matches OUTCOME_PATTERN.
fn is_outcome(outcome: &str) -> bool {
    let mut chars = outcome.chars();
    chars.next().is_some_and(|first| first.is_ascii_lowercase())
        && chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
}

fn retry_issues(retry: &Value, issues: &mut Issues) {
    let at = "/retry";
    let Some(members) = retry.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &RETRY_KEYS, issues);
    for key in ["outcome", "afterMs"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(outcome) = members.get("outcome") {
        schema::text_issues(&schema::member(at, "outcome"), outcome, issues);
    }
    if let Some(after_ms) = members.get("afterMs") {
        schema::integer_issues(
            &schema::member(at, "afterMs"),
            after_ms,
            Some(MIN_BOUND_MS),
            None,
            issues,
        );
    }
}

fn end_on_issues(end_on: &Value, issues: &mut Issues) {
    let at = "/endOn";
    let Some(members) = end_on.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &END_ON_KEYS, issues);
    let Some(enters) = members.get("enters") else {
        issues.push(at, "required", "must have required property 'enters'");
        return;
    };
    let at = "/endOn/enters";
    let Some(items) = enters.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate_value(items) {
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
// paramsSchema, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let (keys, required): (&[&str], &[&str]) = match operation {
        SET_SCHEDULE => (&["after", "cron", "every", "name", "timeZone"], &["name"]),
        PAUSE_SCHEDULE | RESUME_SCHEDULE | CANCEL_SCHEDULE => (&["name"], &["name"]),
        REPORT_RUN => (
            &["attempt", "name", "outcome", "run"],
            &["name", "run", "outcome"],
        ),
        LIST_SCHEDULES => (&["name"], &[]),
        _ => return issues,
    };
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    for key in required {
        if !given.contains_key(*key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    for (key, value) in given {
        let at = schema::member("", key);
        match (operation, key.as_str()) {
            (SET_SCHEDULE, "name") => match value.as_str() {
                Some(name) => {
                    for (keyword, message) in schema::name_issues(name) {
                        issues.push(&at, keyword, message);
                    }
                }
                None => issues.push(&at, "type", "must be string"),
            },
            (SET_SCHEDULE, "after" | "every") | (REPORT_RUN, "run" | "attempt") => {
                schema::integer_issues(&at, value, Some(1), None, &mut issues);
            }
            (_, "name" | "cron" | "timeZone" | "outcome") => {
                schema::text_issues(&at, value, &mut issues);
            }
            _ => {}
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_are_listed_as_a_refusal_says_them() {
        assert_eq!(both(&["after"]), "after");
        assert_eq!(both(&["after", "every"]), "after and every");
        assert_eq!(both(&["after", "every", "cron"]), "after, every and cron");
    }

    #[test]
    fn a_rows_values_are_read_as_an_adapter_gives_them() {
        assert_eq!(integer(&json!("12")), Some(12));
        assert_eq!(integer(&json!(12.0)), Some(12));
        assert_eq!(
            millis(Some(&json!(1_772_323_200_000_i64))),
            Some(1_772_323_200_000)
        );
        assert_eq!(
            millis(Some(&json!("1772323200000"))),
            Some(1_772_323_200_000)
        );
        assert_eq!(
            millis(Some(&json!("2026-03-01T00:00:00.000Z"))),
            Some(1_772_323_200_000)
        );
        assert_eq!(millis(Some(&Value::Null)), None);
        assert!(is_outcome("told") && is_outcome("not_told2"));
        assert!(!is_outcome("Told") && !is_outcome("2x") && !is_outcome(""));
    }

    #[test]
    fn an_every_arms_the_first_point_of_its_grid_after_a_fire() {
        let schedule = Schedule::of(&SubRow::new(
            "hourly",
            json!({"kind": "every", "spec": "3600000", "state": "active", "setAt": 1000, "runs": 0})
                .as_object()
                .cloned()
                .unwrap_or_default(),
        ));
        assert_eq!(
            next_run(&schedule, 1000 + 3_600_000),
            Some(1000 + 7_200_000)
        );
        assert_eq!(
            next_run(&schedule, 1000 + 5 * 3_600_000 + 1),
            Some(1000 + 6 * 3_600_000)
        );
        let after = Schedule {
            kind: "after".to_owned(),
            ..schedule.clone()
        };
        assert_eq!(next_run(&after, 5000), None);
    }
}
