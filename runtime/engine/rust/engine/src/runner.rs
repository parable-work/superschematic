//! The runner, the port of `src/runner/runner.ts` (D16, amended; D58):
//! work that runs after a commit, from the event log, as one principal the
//! deployment names.
//!
//! A subscription is one behavior's reactions on one schema that composes
//! it, in one namespace. It hears that schema's instance events and those
//! of the schemas the behavior's `watches` names, from the publish that
//! made the schema compose the behavior, and keeps the cursor of the last
//! event it handled in `engine_subscriptions`. A `watches` that returns
//! `None` for the schema's config turns the reactions off there; a later
//! version whose config turns them on starts the subscription at its
//! publish. It handles one event at a time, in log order: the reaction runs
//! in a savepoint of a batch's transaction, and the cursor's advance
//! commits with it. A reaction that fails rolls its savepoint back; the
//! batch commits what came before it and records the failure. The event
//! runs again after a backoff, and after `max_attempts` failures the
//! subscription halts at it until [`Runner::resume`]. So a reaction's
//! database effects happen once per event, whatever fails or crashes before
//! the commit.
//!
//! A schedule is one behavior's named timed work on one schema that
//! composes it, in one namespace, with the time of its next run in
//! `engine_schedules`. A run and that time commit together. A schedule that
//! came due while the runner was stopped runs once; missed ticks are not
//! replayed. A failing run is retried with backoff, never later than its
//! next tick, and a schedule never halts. An interval the config gives that
//! is no valid interval fails the schedule on that schema as a failing run
//! does; one that is `None` turns it off there.
//!
//! Both run as schema-level work on a chain whose principal is the
//! runner's and whose cause each event they write records: the behavior,
//! the event or the schedule, and a depth one more than the cause's. A
//! reaction does not run for an event at the depth limit: its subscription
//! passes over it and counts it as skipped. An archived namespace runs
//! nothing until it is unarchived.
//!
//! The Rust runner is driven explicitly or by a thread (D58): a host or a
//! test calls [`Runner::run_due`], which runs everything due at the
//! engine's clock, and [`Runner::next_wake`] for when to call it next; or
//! [`Runner::start`] starts a driver thread that waits on the storage's
//! notifier (a commit in any process, on Postgres) and on a timer for the
//! next retry, schedule or prune, and [`Runner::stop`] stops it. On
//! Postgres the runner works only while its process holds the runner's
//! lease, a session advisory lock ([`superschematic_engine_storage::Lease`]):
//! a second process's started runner stays idle, and takes the lease once
//! the first lets go of it; `run_due` there is refused (`busy`) while
//! another process holds it. A runner looks at its lease before each pass,
//! so when the lease's session ends under a pass (a crash, a lost
//! connection) another process's runner can take it while that pass
//! finishes, and two passes overlap for a moment; each reads a
//! subscription's cursor and a schedule's next run again under the
//! namespace's lock before it acts, so a reaction still runs once per event
//! and a schedule once per due time.
//!
//! With the engine's retention ([`crate::RetentionOptions`]), the runner
//! also prunes the event log: at its first pass, then every `every_ms`, a
//! batch per transaction, never past a namespace's hold (the cursor of the
//! least advanced subscription there). A subscription whose cursor is
//! behind its namespace's floor halts with `cursor_expired`; resume with
//! skip moves it to the floor. Pruning acts for no principal, so
//! [`Runner::prune`] runs without the runner's.

use std::collections::{HashMap, VecDeque};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError, Weak};
use std::thread::{self, JoinHandle, ThreadId};
use std::time::Duration;

use serde::Serialize;
use serde_json::Value;

use superschematic_engine_storage::{Lease, Lock, Row, SqlValue, Tx};

use crate::behaviors::behavior::MIN_SCHEDULE_MS;
use crate::behaviors::composition::{refs_of, BoundBehavior};
use crate::behaviors::execution::{Chain, Execution, Own};
use crate::behaviors::registry::BehaviorRegistry;
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::events::{log_floor, log_head, to_event, EngineEvent, EventCause, EVENT_COLUMNS};
use crate::instances::reach::Reach;
use crate::json::compare_utf16;
use crate::namespaces::NamespaceView;
use crate::principal::{check_principal, Principal};
use crate::registry::document::check_schema_name;
use crate::registry::{SchemaRecord, VersionRuntime, Which};
use crate::retention::{floors_of, NamespaceHold, ResolvedRetention, Retention};

/// How the deployment runs the runner (`EngineOptions.runner`).
#[derive(Clone, Debug, PartialEq)]
pub struct RunnerOptions {
    /// The principal reactions and schedules act as. The access policy is
    /// asked as it at every read and invoke, and the events they write
    /// record its subject as their actor.
    pub principal: Principal,
    /// A reaction runs for an event below this depth; 8 by default.
    pub max_depth: Option<i64>,
    /// Failed attempts at one event before its subscription halts; 5 by
    /// default.
    pub max_attempts: Option<i64>,
    /// The delay before the first retry, in milliseconds, doubling for each
    /// after; 1000 by default.
    pub retry_initial_ms: Option<i64>,
    /// The longest delay between retries, in milliseconds; 60000 by
    /// default.
    pub retry_max_ms: Option<i64>,
    /// Events a subscription handles in one transaction; 100 by default, at
    /// most 500.
    pub batch_size: Option<i64>,
    /// How often a started runner whose process does not hold the runner's
    /// lease (Postgres) tries to take it, in milliseconds; 1000 by default.
    /// The Rust engine's own (D58).
    pub lease_retry_ms: Option<i64>,
}

impl RunnerOptions {
    /// Options with every default, acting as `principal`.
    pub fn new(principal: Principal) -> Self {
        RunnerOptions {
            principal,
            max_depth: None,
            max_attempts: None,
            retry_initial_ms: None,
            retry_max_ms: None,
            batch_size: None,
            lease_retry_ms: None,
        }
    }

    /// Reads the options from the JSON the TypeScript engine takes
    /// (`{ principal, maxDepth?, maxAttempts?, retryInitialMs?,
    /// retryMaxMs?, batchSize? }`), refused in its words.
    pub fn from_json(value: &Value) -> Result<Self> {
        let Value::Object(members) = value else {
            return Err(Error::usage(
                "runner is { principal, maxDepth?, maxAttempts?, retryInitialMs?, retryMaxMs?, batchSize? }",
            ));
        };
        let given = members.get("principal").unwrap_or(&Value::Null);
        let refused = || {
            Error::usage(
                "runner.principal is the principal reactions and schedules act as: { subject, permissions }",
            )
        };
        if !given
            .get("subject")
            .and_then(Value::as_str)
            .is_some_and(|subject| !subject.is_empty())
        {
            return Err(refused());
        }
        if !given.get("permissions").is_some_and(Value::is_array) {
            return Err(Error::usage(
                "runner.principal.permissions is a list of permissions",
            ));
        }
        let principal = crate::calls::principal_from_json(given).map_err(|_| refused())?;
        let member = |key: &str| -> Result<Option<i64>> {
            match members.get(key) {
                None => Ok(None),
                Some(given) => integer_of(given).map(Some).ok_or_else(|| {
                    let (min, max) = range_of(key);
                    Error::usage(format!(
                        "runner.{key} is an integer from {min} to {max}, got {}",
                        crate::calls::js_string(given)
                    ))
                }),
            }
        };
        Ok(RunnerOptions {
            principal,
            max_depth: member("maxDepth")?,
            max_attempts: member("maxAttempts")?,
            retry_initial_ms: member("retryInitialMs")?,
            retry_max_ms: member("retryMaxMs")?,
            batch_size: member("batchSize")?,
            lease_retry_ms: member("leaseRetryMs")?,
        })
    }
}

/// A reaction runs for an event below this depth unless configured.
pub const DEFAULT_MAX_DEPTH: i64 = 8;
/// Failed attempts at one event before its subscription halts unless
/// configured.
pub const DEFAULT_MAX_ATTEMPTS: i64 = 5;
/// The first retry's delay unless configured, in milliseconds.
pub const DEFAULT_RETRY_INITIAL_MS: i64 = 1_000;
/// The longest delay between retries unless configured, in milliseconds.
pub const DEFAULT_RETRY_MAX_MS: i64 = 60_000;
/// Events a subscription handles in one transaction unless configured.
pub const DEFAULT_BATCH_SIZE: i64 = 100;
/// How often an idle runner tries the lease unless configured.
pub const DEFAULT_LEASE_RETRY_MS: i64 = 1_000;

// The longest a driver waits before it looks again.
const MAX_TIMER_MS: i64 = 60_000;
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

fn range_of(key: &str) -> (i64, i64) {
    match key {
        "maxDepth" | "maxAttempts" => (1, 1_000),
        "batchSize" => (1, 500),
        _ => (1, MAX_SAFE_INTEGER),
    }
}

fn integer_of(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = crate::json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= MAX_SAFE_INTEGER as f64)
        .then_some(read as i64)
}

// The runner's options with their defaults, checked.
#[derive(Clone, Copy, Debug)]
struct Settings {
    max_depth: i64,
    max_attempts: i64,
    retry_initial_ms: i64,
    retry_max_ms: i64,
    batch_size: i64,
    lease_retry_ms: i64,
}

fn resolve(options: Option<&RunnerOptions>) -> Result<Settings> {
    if let Some(options) = options {
        if check_principal(&options.principal).is_err() {
            return Err(Error::usage(
                "runner.principal is the principal reactions and schedules act as: { subject, permissions }",
            ));
        }
    }
    let pick = |key: &str, value: Option<i64>, fallback: i64| -> Result<i64> {
        let Some(value) = value else {
            return Ok(fallback);
        };
        let (min, max) = range_of(key);
        if value < min || value > max {
            return Err(Error::usage(format!(
                "runner.{key} is an integer from {min} to {max}, got {value}"
            )));
        }
        Ok(value)
    };
    let settings = Settings {
        max_depth: pick(
            "maxDepth",
            options.and_then(|o| o.max_depth),
            DEFAULT_MAX_DEPTH,
        )?,
        max_attempts: pick(
            "maxAttempts",
            options.and_then(|o| o.max_attempts),
            DEFAULT_MAX_ATTEMPTS,
        )?,
        retry_initial_ms: pick(
            "retryInitialMs",
            options.and_then(|o| o.retry_initial_ms),
            DEFAULT_RETRY_INITIAL_MS,
        )?,
        retry_max_ms: pick(
            "retryMaxMs",
            options.and_then(|o| o.retry_max_ms),
            DEFAULT_RETRY_MAX_MS,
        )?,
        batch_size: pick(
            "batchSize",
            options.and_then(|o| o.batch_size),
            DEFAULT_BATCH_SIZE,
        )?,
        lease_retry_ms: pick(
            "leaseRetryMs",
            options.and_then(|o| o.lease_retry_ms),
            DEFAULT_LEASE_RETRY_MS,
        )?,
    };
    if settings.retry_max_ms < settings.retry_initial_ms {
        return Err(Error::usage(format!(
            "runner.retryMaxMs ({}) is less than runner.retryInitialMs ({})",
            settings.retry_max_ms, settings.retry_initial_ms
        )));
    }
    Ok(settings)
}

/// Names one subscription: a behavior's reactions on a schema in a
/// namespace.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SubscriptionKey {
    pub behavior: String,
    pub namespace: String,
    pub schema: String,
}

impl SubscriptionKey {
    fn id(&self) -> String {
        format!(
            "{}\u{0}{}\u{0}{}",
            self.behavior, self.namespace, self.schema
        )
    }
}

/// A subscription's state: `active` handles events as they come;
/// `retrying` failed at its next event and runs again at `retryAt`;
/// `halted` failed `max_attempts` times, or retention pruned past its
/// cursor, and waits for resume; `off` its behavior's `watches` turns its
/// reactions off for the live version's config; `archived` its namespace is
/// archived; `inactive` the schema's live version no longer composes the
/// behavior, or its implementation is not registered.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum SubscriptionState {
    Active,
    Retrying,
    Halted,
    Off,
    Archived,
    Inactive,
}

/// A subscription's last failure.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SubscriptionFailure {
    /// The event it failed at; `None` for a failure of `watches`.
    pub cursor: Option<i64>,
    pub at: i64,
    pub error: String,
}

/// The event a subscription last passed over.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SubscriptionSkip {
    pub cursor: i64,
    /// `depth` or `resume`.
    pub reason: String,
}

/// A subscription as the status lists it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SubscriptionStatus {
    pub behavior: String,
    pub namespace: String,
    pub schema: String,
    pub state: SubscriptionState,
    /// The cursor of the last event it handled or passed over.
    pub cursor: i64,
    /// Failed attempts at the event after its cursor since its last
    /// success.
    pub attempts: i64,
    /// When it tries again, for a retrying subscription.
    pub retry_at: Option<i64>,
    pub failure: Option<SubscriptionFailure>,
    /// Events it passed over: at the depth limit, or skipped by resume.
    pub skipped: i64,
    pub last_skip: Option<SubscriptionSkip>,
}

impl SubscriptionStatus {
    /// The subscription's key.
    pub fn key(&self) -> SubscriptionKey {
        SubscriptionKey {
            behavior: self.behavior.clone(),
            namespace: self.namespace.clone(),
            schema: self.schema.clone(),
        }
    }
}

/// A schedule's state: `retrying` its last run failed, or its interval
/// could not be had; `off` its interval is `None` for the schema's config;
/// `archived` its namespace is archived; `inactive` no live version
/// composes it now.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum ScheduleState {
    Active,
    Retrying,
    Off,
    Archived,
    Inactive,
}

/// A schedule as the status lists it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleStatus {
    pub behavior: String,
    pub schedule: String,
    pub namespace: String,
    pub schema: String,
    pub state: ScheduleState,
    /// Its interval on the schema; `None` for an inactive or off one and
    /// one whose interval fails there.
    pub every_ms: Option<i64>,
    /// When its last run committed.
    pub previous: Option<i64>,
    /// When it runs next; `None` for an off one.
    pub next: Option<i64>,
    /// Failed runs since its last success.
    pub failures: i64,
    pub error: Option<String>,
}

/// How far retention has pruned one namespace, and what holds it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetentionNamespaceStatus {
    pub namespace: String,
    /// The last cursor pruned of its events.
    pub floor: i64,
    /// Events pruned of it so far.
    pub pruned: i64,
    /// The cursor of the least advanced subscription, past which nothing
    /// is pruned.
    pub held_at: Option<i64>,
    pub held_by: Option<SubscriptionKey>,
    pub held_state: Option<SubscriptionState>,
    /// When the oldest event the hold keeps was appended.
    pub held_since: Option<i64>,
    /// For a subscription that does not advance, under `max_hold_ms`: how
    /// long its hold lasts.
    pub held_until: Option<i64>,
}

/// What retention keeps and how far it has pruned.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetentionStatus {
    pub max_age_ms: Option<i64>,
    pub max_events: Option<i64>,
    pub max_hold_ms: Option<i64>,
    pub every_ms: i64,
    /// When the runner last finished pruning.
    pub previous: Option<i64>,
    /// When it prunes next.
    pub next: Option<i64>,
    pub namespaces: Vec<RetentionNamespaceStatus>,
}

/// The runner's state: whether it is started, as whom it acts, the log's
/// head, every subscription and schedule, retention, and its own last
/// error.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RunnerStatus {
    pub running: bool,
    /// The subject of the runner's principal; `None` when the engine has
    /// none.
    pub principal: Option<String>,
    /// The log's last cursor, 0 for a log that never held one.
    pub head: i64,
    pub subscriptions: Vec<SubscriptionStatus>,
    pub schedules: Vec<ScheduleStatus>,
    /// Absent for an engine opened without retention.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub retention: Option<RetentionStatus>,
    /// The runner's own last error, outside any reaction or schedule.
    pub error: Option<String>,
}

/// What one `run_due` did.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct RunnerPass {
    /// Events a reaction handled, committed.
    pub handled: i64,
    /// Events passed over at the depth limit.
    pub skipped: i64,
    /// Failed attempts, of reactions and schedules.
    pub failed: i64,
    /// Schedule runs, committed.
    pub scheduled: i64,
    /// Events retention pruned; absent for an engine opened without
    /// retention.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pruned: Option<i64>,
}

/// What one `prune` did.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct PruneResult {
    pub pruned: i64,
}

#[derive(Default)]
struct Totals {
    handled: i64,
    skipped: i64,
    failed: i64,
    scheduled: i64,
    pruned: i64,
    moved: bool,
}

// One behavior on one schema's live version in one namespace.
struct Unit {
    behavior: String,
    namespace: String,
    schema: String,
    record: SchemaRecord,
    runtime: Arc<VersionRuntime>,
    /// The behavior's position on the type.
    index: usize,
    archived: bool,
}

impl Unit {
    fn key(&self) -> SubscriptionKey {
        SubscriptionKey {
            behavior: self.behavior.clone(),
            namespace: self.namespace.clone(),
            schema: self.schema.clone(),
        }
    }

    fn bound(&self) -> &BoundBehavior {
        &self.runtime.composition.behaviors[self.index]
    }
}

struct ReactionUnit {
    unit: Unit,
    /// The cursor it starts after.
    start: i64,
    /// Whether watches turns the reactions off for the live config.
    off: bool,
}

enum Interval {
    Every(i64),
    Off,
    Error(String),
}

struct ScheduleUnit {
    unit: Unit,
    name: String,
    every: Interval,
}

// A subscription that holds retention, or would once it has run.
struct Holder {
    key: SubscriptionKey,
    start: i64,
    unregistered: bool,
}

struct Discovery {
    key: String,
    reactions: Vec<ReactionUnit>,
    schedules: Vec<ScheduleUnit>,
    holders: Vec<Holder>,
}

// What holds a namespace's events.
struct Hold {
    cursor: i64,
    subscription: SubscriptionKey,
    state: SubscriptionState,
    hold: NamespaceHold,
}

#[derive(Default)]
struct State {
    discovery: Option<Arc<Discovery>>,
    starts: HashMap<String, i64>,
    /// When the next retry or schedule comes due, as the last pass saw it.
    due: Option<i64>,
    last_error: Option<String>,
    next_prune: i64,
    last_prune: Option<i64>,
    /// A retention pass under way: the namespaces it has yet to prune.
    pruning: Option<VecDeque<String>>,
}

struct Driver {
    stop: Arc<AtomicBool>,
    thread: Option<JoinHandle<()>>,
    id: ThreadId,
}

/// The runner's part of the engine: its options, its state, the pass the
/// engine runs one at a time, the driver thread and the lease.
pub(crate) struct RunnerCore {
    settings: Settings,
    principal: Option<Principal>,
    retention: Option<Retention>,
    state: Mutex<State>,
    pass: Mutex<()>,
    owner: Mutex<Option<ThreadId>>,
    driver: Mutex<Option<Driver>>,
    lease: Mutex<Option<Lease>>,
    closed: AtomicBool,
}

impl RunnerCore {
    /// The runner of an engine opened with these options, checked before
    /// the storage opens.
    pub fn new(
        options: Option<&RunnerOptions>,
        retention: Option<&crate::retention::RetentionOptions>,
    ) -> Result<Self> {
        let settings = resolve(options)?;
        let retention: Option<ResolvedRetention> = retention.map(|r| r.resolve()).transpose()?;
        Ok(RunnerCore {
            settings,
            principal: options.map(|options| options.principal.clone()),
            retention: retention.map(Retention::new),
            state: Mutex::new(State::default()),
            pass: Mutex::new(()),
            owner: Mutex::new(None),
            driver: Mutex::new(None),
            lease: Mutex::new(None),
            closed: AtomicBool::new(false),
        })
    }

    fn state(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn due_at(&self, at: i64) {
        let mut state = self.state();
        state.due = Some(state.due.map_or(at, |due| due.min(at)));
    }

    /// Stops the driver for good; the engine's shutdown and drop call it.
    pub fn close(&self, notifier: &superschematic_engine_storage::Notifier) {
        self.closed.store(true, Ordering::SeqCst);
        self.stop_driver(Some(notifier));
        *self.lease.lock().unwrap_or_else(PoisonError::into_inner) = None;
    }

    // stop_driver stops the driver thread and waits for it, unless this is
    // the driver's own thread, which ends after its pass.
    fn stop_driver(&self, notifier: Option<&superschematic_engine_storage::Notifier>) {
        let driver = self
            .driver
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
        let Some(mut driver) = driver else {
            return;
        };
        driver.stop.store(true, Ordering::SeqCst);
        if let Some(notifier) = notifier {
            notifier.bump();
        }
        if driver.id != thread::current().id() {
            if let Some(thread) = driver.thread.take() {
                let _ = thread.join();
            }
        }
    }

    fn driver_running(&self) -> bool {
        self.driver
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .is_some()
    }
}

// A pass under way: the thread that runs it, cleared when it ends.
struct PassGuard<'a> {
    _pass: MutexGuard<'a, ()>,
    owner: &'a Mutex<Option<ThreadId>>,
}

impl Drop for PassGuard<'_> {
    fn drop(&mut self) {
        *self.owner.lock().unwrap_or_else(PoisonError::into_inner) = None;
    }
}

/// The runner of reactions and schedules (`engine.runner`).
pub struct Runner<'e> {
    pub(crate) engine: &'e Engine,
}

impl<'e> Runner<'e> {
    fn core(&self) -> &'e RunnerCore {
        &self.engine.runner
    }

    /// Whether the driver thread is started.
    pub fn running(&self) -> bool {
        self.core().driver_running()
    }

    fn ready(&self, what: &str) -> Result<&'e Principal> {
        if self.core().closed.load(Ordering::SeqCst) {
            return Err(Error::internal("the engine is closed"));
        }
        self.core().principal.as_ref().ok_or_else(|| {
            Error::usage(format!(
                "runner.{what} needs a principal: open the engine with runner: {{ principal }}"
            ))
        })
    }

    /// Starts the driver thread: it runs what is due, then whatever the
    /// commits after it make due, until [`Runner::stop`]. It needs the
    /// engine's runner principal and a shared engine ([`Engine::shared`]).
    /// Starting a started runner does nothing.
    pub fn start(&self) -> Result<()> {
        self.ready("start")?;
        let core = self.core();
        let mut driver = core.driver.lock().unwrap_or_else(PoisonError::into_inner);
        if driver.is_some() {
            return Ok(());
        }
        let Some(shared) = self.engine.weak() else {
            return Err(Error::usage(
                "runner.start runs the runner on a thread of its own, which needs a shared engine: open it with Engine::shared",
            ));
        };
        let stop = Arc::new(AtomicBool::new(false));
        let notifier = Arc::clone(self.engine.db.notifier());
        let thread = {
            let stop = Arc::clone(&stop);
            thread::Builder::new()
                .name("engine-runner".to_owned())
                .spawn(move || drive(&shared, &notifier, &stop))
                .map_err(|error| {
                    Error::internal(format!("could not start the runner's thread: {error}"))
                })?
        };
        let id = thread.thread().id();
        *driver = Some(Driver {
            stop,
            thread: Some(thread),
            id,
        });
        Ok(())
    }

    /// Stops the driver: no pass starts after it, and start resumes from
    /// the saved cursors.
    pub fn stop(&self) {
        self.core().stop_driver(Some(self.engine.db.notifier()));
        // Not started, the process holds no lease between run_due calls.
        *self
            .core()
            .lease
            .lock()
            .unwrap_or_else(PoisonError::into_inner) = None;
    }

    /// When the runner next has work: the earliest retry, schedule or
    /// prune the last pass found, the engine's time now while a prune is
    /// under way; `None` when nothing is timed (a commit still wakes it).
    pub fn next_wake(&self) -> Option<i64> {
        let state = self.core().state();
        if state.pruning.is_some() {
            return Some(self.engine.clock.now());
        }
        state.due
    }

    /// Runs every reaction and schedule that is due now, the reactions to
    /// what they write included, and returns what it did. It needs the
    /// runner principal and no open transaction, and runs whether or not
    /// the runner is started; on Postgres, while another process holds the
    /// runner's lease, it is refused (`busy`).
    pub fn run_due(&self) -> Result<RunnerPass> {
        self.ready("runDue")?;
        let leased = self.leased()?;
        if !leased {
            return Err(EngineError::new(
                EngineErrorCode::Busy,
                "another process's runner holds the runner's lease on this database; this one runs once it lets go",
            )
            .into());
        }
        let result = self.run_all();
        if !self.core().driver_running() {
            *self
                .core()
                .lease
                .lock()
                .unwrap_or_else(PoisonError::into_inner) = None;
        }
        result
    }

    fn run_all(&self) -> Result<RunnerPass> {
        let mut total = RunnerPass {
            pruned: self.core().retention.as_ref().map(|_| 0),
            ..RunnerPass::default()
        };
        loop {
            let pass = self.pass()?;
            total.handled += pass.handled;
            total.skipped += pass.skipped;
            total.failed += pass.failed;
            total.scheduled += pass.scheduled;
            if let Some(pruned) = total.pruned.as_mut() {
                *pruned += pass.pruned;
            }
            if !pass.moved {
                return Ok(total);
            }
        }
    }

    // leased is whether this process holds the runner's lease, taking it
    // when it is free.
    fn leased(&self) -> Result<bool> {
        let mut lease = self
            .core()
            .lease
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        if let Some(held) = lease.as_mut() {
            if held.held() {
                return Ok(true);
            }
            *lease = None;
        }
        *lease = self.engine.db.try_lease(&["runner"])?;
        Ok(lease.is_some())
    }

    /// Runs retention now, to the end: every namespace's events that
    /// retention lets go and no subscription holds, a batch per
    /// transaction. It acts for no principal; it refuses inside a pass or
    /// a transaction, and on an engine without retention.
    pub fn prune(&self) -> Result<PruneResult> {
        if self.core().closed.load(Ordering::SeqCst) {
            return Err(Error::internal("the engine is closed"));
        }
        if self.core().retention.is_none() {
            return Err(Error::usage(
                "runner.prune needs retention: open the engine with retention: { maxAgeMs?, maxEvents? }",
            ));
        }
        let _guard = self.enter(
            "the runner is already running: prune cannot run inside a reaction or a schedule",
            "the runner prunes outside any transaction",
        )?;
        self.core().state().pruning = None;
        let mut pruned = 0;
        loop {
            let (step, done) = self.prune_step()?;
            pruned += step;
            if done {
                return Ok(PruneResult { pruned });
            }
        }
    }

    /// Lists every subscription and schedule, with their cursors, retries
    /// and failures.
    pub fn status(&self) -> Result<RunnerStatus> {
        let discovery = self.discover()?;
        let engine = self.engine;
        let (mut subscriptions, mut schedules, head) = engine.read(|tx| {
            let mut subscriptions: Vec<(String, SubscriptionStatus)> = Vec::new();
            for row in tx.all(
                format!("SELECT {SUBSCRIPTION_COLUMNS} FROM engine_subscriptions"),
                &[],
            )? {
                let mut status = subscription_status(&row)?;
                status.state = SubscriptionState::Inactive;
                status.retry_at = None;
                subscriptions.push((status.key().id(), status));
            }
            for reaction in &discovery.reactions {
                let unit = &reaction.unit;
                let id = unit.key().id();
                let row = subscription_row(tx, &unit.key())?;
                let status = if reaction.off {
                    let Some(row) = row else {
                        continue;
                    };
                    let mut status = subscription_status(&row)?;
                    status.state = if unit.archived {
                        SubscriptionState::Archived
                    } else {
                        SubscriptionState::Off
                    };
                    status.retry_at = None;
                    status
                } else {
                    let mut status = match row {
                        Some(row) => {
                            let mut status = subscription_status(&row)?;
                            status.cursor = status.cursor.max(reaction.start);
                            status
                        }
                        None => fresh(unit, reaction.start),
                    };
                    if unit.archived {
                        status.state = SubscriptionState::Archived;
                        status.retry_at = None;
                    }
                    status
                };
                put(&mut subscriptions, id, status);
            }
            let mut schedules: Vec<(String, ScheduleStatus)> = Vec::new();
            for row in tx.all(
                format!("SELECT {SCHEDULE_COLUMNS} FROM engine_schedules"),
                &[],
            )? {
                let mut status = schedule_status(&row, None)?;
                status.state = ScheduleState::Inactive;
                schedules.push((schedule_id(&status), status));
            }
            for schedule in &discovery.schedules {
                let unit = &schedule.unit;
                let mut status = off_status(unit, &schedule.name);
                let id = schedule_id(&status);
                if matches!(schedule.every, Interval::Off) {
                    put(&mut schedules, id, status);
                    continue;
                }
                let every_ms = match schedule.every {
                    Interval::Every(ms) => Some(ms),
                    _ => None,
                };
                match schedule_row(tx, unit, &schedule.name)? {
                    Some(row) => {
                        let mut found = schedule_status(&row, every_ms)?;
                        if unit.archived {
                            found.state = ScheduleState::Archived;
                        }
                        put(&mut schedules, id, found);
                    }
                    None if unit.archived => {
                        status.state = ScheduleState::Archived;
                        status.every_ms = every_ms;
                        put(&mut schedules, id, status);
                    }
                    None => {}
                }
            }
            Ok((subscriptions, schedules, log_head(tx)?))
        })?;
        subscriptions.sort_by(|a, b| compare_utf16(&a.0, &b.0));
        schedules.sort_by(|a, b| compare_utf16(&a.0, &b.0));
        let retention = match &self.core().retention {
            Some(retention) => Some(self.retention_status(retention, &discovery)?),
            None => None,
        };
        Ok(RunnerStatus {
            running: self.running(),
            principal: self.core().principal.as_ref().map(|p| p.subject.clone()),
            head,
            subscriptions: subscriptions.into_iter().map(|(_, s)| s).collect(),
            schedules: schedules.into_iter().map(|(_, s)| s).collect(),
            retention,
            error: self.core().state().last_error.clone(),
        })
    }

    fn retention_status(
        &self,
        retention: &Retention,
        discovery: &Discovery,
    ) -> Result<RetentionStatus> {
        let options = retention.options;
        let holds = self.holds(discovery)?;
        let mut namespaces = self.engine.read(|tx| {
            let mut by_namespace: Vec<RetentionNamespaceStatus> = floors_of(tx)?
                .into_iter()
                .map(|floor| RetentionNamespaceStatus {
                    namespace: floor.namespace,
                    floor: floor.floor,
                    pruned: floor.pruned,
                    held_at: None,
                    held_by: None,
                    held_state: None,
                    held_since: None,
                    held_until: None,
                })
                .collect();
            for (namespace, hold) in &holds {
                let oldest = tx.get(
                    "SELECT at FROM engine_events WHERE namespace = ? AND cursor > ? ORDER BY cursor LIMIT 1",
                    &[SqlValue::from(namespace.as_str()), SqlValue::Integer(hold.cursor)],
                )?;
                let held_since = oldest.map(|row| row.i64("at")).transpose()?;
                let stuck = matches!(
                    hold.state,
                    SubscriptionState::Halted
                        | SubscriptionState::Archived
                        | SubscriptionState::Inactive
                );
                let held_until = match (stuck, held_since, options.max_hold_ms) {
                    (true, Some(since), Some(bound)) => Some(since + bound),
                    _ => None,
                };
                let index = match by_namespace
                    .iter()
                    .position(|entry| &entry.namespace == namespace)
                {
                    Some(index) => index,
                    None => {
                        by_namespace.push(RetentionNamespaceStatus {
                            namespace: namespace.clone(),
                            floor: 0,
                            pruned: 0,
                            held_at: None,
                            held_by: None,
                            held_state: None,
                            held_since: None,
                            held_until: None,
                        });
                        by_namespace.len() - 1
                    }
                };
                let entry = &mut by_namespace[index];
                entry.held_at = Some(hold.cursor);
                entry.held_by = Some(hold.subscription.clone());
                entry.held_state = Some(hold.state);
                entry.held_since = held_since;
                entry.held_until = held_until;
            }
            Ok(by_namespace)
        })?;
        namespaces.sort_by(|a, b| compare_utf16(&a.namespace, &b.namespace));
        let state = self.core().state();
        Ok(RetentionStatus {
            max_age_ms: options.max_age_ms,
            max_events: options.max_events,
            max_hold_ms: options.max_hold_ms,
            every_ms: options.every_ms,
            previous: state.last_prune,
            next: if state.pruning.is_some() {
                Some(self.engine.clock.now())
            } else if state.next_prune == 0 {
                None
            } else {
                Some(state.next_prune)
            },
            namespaces,
        })
    }

    /// Clears a subscription's failure and runs it again from its cursor.
    /// With skip, it first passes over the event that failed and records
    /// the skip. A subscription with no failed event cannot skip.
    pub fn resume(&self, key: &SubscriptionKey, skip: bool) -> Result<SubscriptionStatus> {
        if self.core().closed.load(Ordering::SeqCst) {
            return Err(Error::internal("the engine is closed"));
        }
        let engine = self.engine;
        let row = engine.read(|tx| subscription_row(tx, key))?;
        let Some(row) = row else {
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!(
                    "no subscription of {} on {} in namespace {} has run",
                    key.behavior, key.schema, key.namespace
                ),
            )
            .into());
        };
        if skip && (row.opt_i64("failed_cursor")?.is_none() || row.i64("attempts")? == 0) {
            return Err(EngineError::invalid_argument(format!(
                "the subscription of {} on {} in namespace {} has no failed event to skip",
                key.behavior, key.schema, key.namespace
            ))
            .into());
        }
        engine.write(Lock::Namespace(&key.namespace), |tx| {
            let params = key_params(key);
            if skip {
                tx.run(
                    "UPDATE engine_subscriptions SET cursor = failed_cursor, skipped = skipped + 1, skipped_cursor = failed_cursor, skipped_reason = 'resume'
                     WHERE behavior = ? AND namespace = ? AND schema = ?",
                    &params,
                )?;
            }
            tx.run(
                "UPDATE engine_subscriptions SET halted = 0, attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
                 WHERE behavior = ? AND namespace = ? AND schema = ?",
                &params,
            )?;
            Ok(())
        })?;
        self.wake();
        let id = key.id();
        self.status()?
            .subscriptions
            .into_iter()
            .find(|status| status.key().id() == id)
            .ok_or_else(|| Error::internal("a resumed subscription is not in the status"))
    }

    // wake has a started driver look again now.
    fn wake(&self) {
        if self.core().driver_running() {
            self.engine.db.notifier().bump();
        }
    }

    // enter starts a pass: refused on the thread that runs one, and inside
    // a transaction; another thread's pass is waited for.
    fn enter(&self, running: &str, transaction: &str) -> Result<PassGuard<'e>> {
        let core = self.core();
        let me = thread::current().id();
        if *core.owner.lock().unwrap_or_else(PoisonError::into_inner) == Some(me) {
            return Err(Error::internal(running));
        }
        if self.engine.db.in_transaction() {
            return Err(Error::internal(transaction));
        }
        let pass = core.pass.lock().unwrap_or_else(PoisonError::into_inner);
        *core.owner.lock().unwrap_or_else(PoisonError::into_inner) = Some(me);
        Ok(PassGuard {
            _pass: pass,
            owner: &core.owner,
        })
    }

    // pass runs one batch of every due subscription and every due schedule.
    fn pass(&self) -> Result<Totals> {
        let _guard = self.enter(
            "the runner is already running: runDue cannot run inside a reaction or a schedule",
            "the runner runs outside any transaction",
        )?;
        let mut totals = Totals::default();
        self.core().state().due = None;
        let discovery = self.discover()?;
        for reaction in &discovery.reactions {
            if !reaction.unit.archived {
                self.react(reaction, &mut totals)?;
            }
        }
        for schedule in &discovery.schedules {
            if !schedule.unit.archived {
                self.schedule(schedule, &mut totals)?;
            }
        }
        self.retain(&mut totals)?;
        self.core().state().last_error = None;
        Ok(totals)
    }

    // pass_driven is one pass of the driver: whether more may be due now.
    fn pass_driven(&self) -> bool {
        match self.pass() {
            Ok(totals) => totals.moved,
            Err(error) => {
                // The runner's own failure, not a reaction's: a busy
                // database, say. It tries again after the first retry delay.
                let at = self.engine.clock.now() + self.core().settings.retry_initial_ms;
                let mut state = self.core().state();
                state.last_error = Some(describe(&error));
                state.due = Some(at);
                false
            }
        }
    }

    // retain prunes one batch of the log when retention is due or a pass
    // of it is under way, and has the runner come back while more is left.
    fn retain(&self, totals: &mut Totals) -> Result<()> {
        if self.core().retention.is_none() {
            return Ok(());
        }
        let now = self.engine.clock.now();
        let (pruning, next_prune) = {
            let state = self.core().state();
            (state.pruning.is_some(), state.next_prune)
        };
        if !pruning && now < next_prune {
            self.core().due_at(next_prune);
            return Ok(());
        }
        let (pruned, done) = self.prune_step()?;
        totals.pruned += pruned;
        if done {
            let next = self.core().state().next_prune;
            self.core().due_at(next);
        } else {
            totals.moved = true;
        }
        Ok(())
    }

    // prune_step prunes one batch of the retention pass under way, starting
    // one when none is, and reports whether the pass is over.
    fn prune_step(&self) -> Result<(i64, bool)> {
        let Some(retention) = &self.core().retention else {
            return Ok((0, true));
        };
        if self.core().state().pruning.is_none() {
            let queue: VecDeque<String> = retention.namespaces_with_events(self.engine)?.into();
            self.core().state().pruning = Some(queue);
        }
        let discovery = self.discover()?;
        let holds = self.holds(&discovery)?;
        loop {
            let next = self
                .core()
                .state()
                .pruning
                .as_ref()
                .and_then(|queue| queue.front().cloned());
            let Some(namespace) = next else {
                break;
            };
            let hold = holds
                .iter()
                .find(|(each, _)| *each == namespace)
                .map(|(_, hold)| hold.hold)
                .unwrap_or_default();
            let pruned = retention.batch(self.engine, &namespace, hold)?;
            if pruned < retention.options.batch_size {
                if let Some(queue) = self.core().state().pruning.as_mut() {
                    queue.pop_front();
                }
            }
            if pruned > 0 {
                return Ok((pruned, false));
            }
        }
        let now = self.engine.clock.now();
        let mut state = self.core().state();
        state.pruning = None;
        state.last_prune = Some(now);
        state.next_prune = now + retention.options.every_ms;
        Ok((0, true))
    }

    // holds is, by namespace, what holds its events: the least advanced of
    // its subscriptions, at its cursor, or at its start before it has run,
    // with its state, and the least advanced cursors of the ones that
    // advance and of the ones that do not.
    fn holds(&self, discovery: &Discovery) -> Result<Vec<(String, Hold)>> {
        let engine = self.engine;
        engine.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let mut saved: HashMap<String, Row> = HashMap::new();
            for row in tx.all(
                "SELECT behavior, namespace, schema, cursor, halted, attempts FROM engine_subscriptions",
                &[],
            )? {
                let key = SubscriptionKey {
                    behavior: row.text("behavior")?.to_owned(),
                    namespace: row.text("namespace")?.to_owned(),
                    schema: row.text("schema")?.to_owned(),
                };
                saved.insert(key.id(), row);
            }
            let mut holds: Vec<(String, Hold)> = Vec::new();
            for holder in &discovery.holders {
                let row = saved.get(&holder.key.id());
                if holder.unregistered && row.is_none() {
                    continue;
                }
                let current = match row {
                    Some(row) if row.i64("cursor")? >= holder.start => Some(row),
                    _ => None,
                };
                let cursor = match current {
                    Some(row) => row.i64("cursor")?,
                    None => holder.start,
                };
                let state = if holder.unregistered {
                    SubscriptionState::Inactive
                } else if view.archived(&holder.key.namespace) {
                    SubscriptionState::Archived
                } else if current.map(|row| row.i64("halted")).transpose()? == Some(1) {
                    SubscriptionState::Halted
                } else if current.map(|row| row.i64("attempts")).transpose()?.unwrap_or(0) > 0 {
                    SubscriptionState::Retrying
                } else {
                    SubscriptionState::Active
                };
                let advances =
                    matches!(state, SubscriptionState::Active | SubscriptionState::Retrying);
                let found = holds
                    .iter_mut()
                    .find(|(namespace, _)| *namespace == holder.key.namespace);
                match found {
                    None => holds.push((
                        holder.key.namespace.clone(),
                        Hold {
                            cursor,
                            subscription: holder.key.clone(),
                            state,
                            hold: NamespaceHold {
                                advancing: advances.then_some(cursor),
                                stuck: (!advances).then_some(cursor),
                            },
                        },
                    )),
                    Some((_, held)) => {
                        if cursor < held.cursor {
                            held.cursor = cursor;
                            held.subscription = holder.key.clone();
                            held.state = state;
                        }
                        if advances {
                            held.hold.advancing =
                                Some(held.hold.advancing.map_or(cursor, |c| c.min(cursor)));
                        } else {
                            held.hold.stuck =
                                Some(held.hold.stuck.map_or(cursor, |c| c.min(cursor)));
                        }
                    }
                }
            }
            Ok(holds)
        })
    }

    // react runs one batch of a subscription's events, if it is due and
    // its reactions are on.
    fn react(&self, reaction: &ReactionUnit, totals: &mut Totals) -> Result<()> {
        if reaction.off {
            return Ok(());
        }
        let engine = self.engine;
        let unit = &reaction.unit;
        let key = unit.key();
        let settings = self.core().settings;
        let now = engine.clock.now();
        let (before, floor, head) = engine.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            Ok((
                subscription_row(tx, &key)?,
                log_floor(tx, &view, &unit.namespace, false)?,
                log_head(tx)?,
            ))
        })?;
        // A row from before the schema last came to compose the behavior
        // starts over at the new start, whatever it held.
        let current = match before {
            Some(row) if row.i64("cursor")? >= reaction.start => Some(row),
            _ => None,
        };
        if let Some(row) = &current {
            if row.i64("halted")? == 1 {
                return Ok(());
            }
            if let Some(retry_at) = row.opt_i64("retry_at")? {
                if retry_at > now {
                    self.core().due_at(retry_at);
                    return Ok(());
                }
            }
        }
        let cursor = match &current {
            Some(row) => row.i64("cursor")?,
            None => reaction.start,
        };
        // Retention pruned past where it is: it halts, rather than skip
        // what it never handled.
        if cursor < floor {
            let error =
                describe(&EngineError::cursor_expired(&unit.namespace, cursor, floor, head).into());
            engine.write(Lock::Namespace(&unit.namespace), |tx| {
                ensure_subscription(tx, reaction)?;
                let mut params = vec![
                    SqlValue::Integer(settings.max_attempts),
                    SqlValue::Integer(floor),
                    SqlValue::Integer(now),
                    SqlValue::from(error.as_str()),
                ];
                params.extend(key_params(&key));
                tx.run(
                    "UPDATE engine_subscriptions SET halted = 1, attempts = ?, retry_at = NULL, failed_cursor = ?, failed_at = ?, error = ?
                     WHERE behavior = ? AND namespace = ? AND schema = ?",
                    &params,
                )?;
                Ok(())
            })?;
            totals.failed += 1;
            return Ok(());
        }
        let watched = match self.watched(unit) {
            Ok(watched) => watched,
            Err(error) => {
                engine.write(Lock::Namespace(&unit.namespace), |tx| {
                    // The attempts as the row holds them under the lock,
                    // which another process's runner may have counted.
                    let attempts = ensure_subscription(tx, reaction)?.i64("attempts")?;
                    self.fail(tx, unit, None, attempts + 1, now, &error)
                })?;
                totals.failed += 1;
                return Ok(());
            }
        };
        let rows = engine.read(|tx| {
            let marks = vec!["?"; watched.len()].join(", ");
            let mut params = vec![SqlValue::from(unit.namespace.as_str())];
            params.extend(watched.iter().map(|schema| SqlValue::from(schema.as_str())));
            params.push(SqlValue::Integer(cursor));
            params.push(SqlValue::Integer(settings.batch_size));
            Ok(tx.all(
                format!(
                    "SELECT {EVENT_COLUMNS} FROM engine_events
                     WHERE namespace = ? AND schema IN ({marks}) AND instance_id IS NOT NULL AND cursor > ?
                     ORDER BY cursor LIMIT ?"
                ),
                &params,
            )?)
        })?;
        if rows.is_empty() {
            // A failure with no event, of watches, is over once it works.
            if let Some(row) = &current {
                if row.i64("attempts")? > 0 && row.opt_i64("failed_cursor")?.is_none() {
                    engine.write(Lock::Namespace(&unit.namespace), |tx| {
                        tx.run(
                            "UPDATE engine_subscriptions SET attempts = 0, retry_at = NULL, failed_at = NULL, error = NULL
                             WHERE behavior = ? AND namespace = ? AND schema = ?",
                            &key_params(&key),
                        )?;
                        Ok(())
                    })?;
                }
            }
            return Ok(());
        }
        let events = rows.iter().map(to_event).collect::<Result<Vec<_>>>()?;
        engine.write(Lock::Namespace(&unit.namespace), |tx| {
            let row = ensure_subscription(tx, reaction)?;
            let mut at = row.i64("cursor")?;
            let mut attempts = row.i64("attempts")?;
            let mut handled = 0;
            let mut skipped = 0;
            let mut last_skip = None;
            let mut failure: Option<(i64, Error)> = None;
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            for event in &events {
                if event.cursor <= at {
                    continue;
                }
                let depth = event.cause.as_ref().map_or(0, |cause| cause.depth);
                if depth >= settings.max_depth {
                    skipped += 1;
                    last_skip = Some(event.cursor);
                    at = event.cursor;
                    continue;
                }
                let ran = tx.savepoint(|tx| self.handle(tx, &view, unit, event, depth));
                if let Err(error) = ran {
                    failure = Some((event.cursor, error));
                    break;
                }
                handled += 1;
                attempts = 0;
                at = event.cursor;
            }
            let key_values = key_params(&key);
            match last_skip {
                None => {
                    let mut params = vec![SqlValue::Integer(at), SqlValue::Integer(skipped)];
                    params.extend(key_values.iter().cloned());
                    tx.run(
                        "UPDATE engine_subscriptions SET cursor = ?, skipped = skipped + ?
                         WHERE behavior = ? AND namespace = ? AND schema = ?",
                        &params,
                    )?;
                }
                Some(skip) => {
                    let mut params = vec![
                        SqlValue::Integer(at),
                        SqlValue::Integer(skipped),
                        SqlValue::Integer(skip),
                    ];
                    params.extend(key_values.iter().cloned());
                    tx.run(
                        "UPDATE engine_subscriptions SET cursor = ?, skipped = skipped + ?, skipped_cursor = ?, skipped_reason = 'depth'
                         WHERE behavior = ? AND namespace = ? AND schema = ?",
                        &params,
                    )?;
                }
            }
            if let Some((cursor, error)) = &failure {
                self.fail(tx, unit, Some(*cursor), attempts + 1, now, error)?;
            } else if handled > 0 {
                tx.run(
                    "UPDATE engine_subscriptions SET attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
                     WHERE behavior = ? AND namespace = ? AND schema = ?",
                    &key_values,
                )?;
            }
            totals.handled += handled;
            totals.skipped += skipped;
            totals.failed += i64::from(failure.is_some());
            totals.moved |= handled + skipped > 0;
            Ok(())
        })
    }

    // handle runs a subscription's reaction to one event, on a chain whose
    // cause is the event, one deeper than it.
    fn handle(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        unit: &Unit,
        event: &EngineEvent,
        depth: i64,
    ) -> Result<()> {
        let principal = self
            .core()
            .principal
            .as_ref()
            .ok_or_else(|| Error::internal("the runner has no principal"))?;
        let chain = Chain::caused(
            principal,
            &unit.namespace,
            self.engine.clock.now(),
            EventCause {
                behavior: unit.behavior.clone(),
                event: Some(event.cursor),
                schedule: None,
                depth: depth + 1,
            },
        );
        let reach = Reach {
            engine: self.engine,
            tx,
            view,
            chain: &chain,
        };
        Execution::new(
            reach,
            Arc::clone(&unit.runtime),
            &unit.schema,
            unit.record.version.unwrap_or_default(),
            "",
            Own::None,
            true,
        )
        .react(unit.index, event)
    }

    // fail records a failed attempt at an event: a retry after the
    // backoff, or a halt after max_attempts.
    fn fail(
        &self,
        tx: &Tx<'_>,
        unit: &Unit,
        cursor: Option<i64>,
        attempts: i64,
        now: i64,
        error: &Error,
    ) -> Result<()> {
        let settings = self.core().settings;
        let halted = attempts >= settings.max_attempts;
        let retry_at = (!halted).then(|| now + self.backoff(attempts));
        if let Some(retry_at) = retry_at {
            self.core().due_at(retry_at);
        }
        let mut params = vec![
            SqlValue::Integer(i64::from(halted)),
            SqlValue::Integer(attempts),
            SqlValue::from(retry_at),
            SqlValue::from(cursor),
            SqlValue::Integer(now),
            SqlValue::from(describe(error)),
        ];
        params.extend(key_params(&unit.key()));
        tx.run(
            "UPDATE engine_subscriptions SET halted = ?, attempts = ?, retry_at = ?, failed_cursor = ?, failed_at = ?, error = ?
             WHERE behavior = ? AND namespace = ? AND schema = ?",
            &params,
        )?;
        Ok(())
    }

    // schedule runs a schedule once if it is due. A schedule found for the
    // first time is due an interval later. One that is off on the schema
    // runs nothing and keeps no row, so it is found anew once it is on.
    fn schedule(&self, schedule: &ScheduleUnit, totals: &mut Totals) -> Result<()> {
        let engine = self.engine;
        let unit = &schedule.unit;
        let now = engine.clock.now();
        let row = engine.read(|tx| schedule_row(tx, unit, &schedule.name))?;
        let key = [
            SqlValue::from(unit.behavior.as_str()),
            SqlValue::from(schedule.name.as_str()),
            SqlValue::from(unit.namespace.as_str()),
            SqlValue::from(unit.schema.as_str()),
        ];
        let every = match &schedule.every {
            Interval::Off => {
                if row.is_some() {
                    engine.write(Lock::Namespace(&unit.namespace), |tx| {
                        tx.run(
                            "DELETE FROM engine_schedules WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?",
                            &key,
                        )?;
                        Ok(())
                    })?;
                }
                return Ok(());
            }
            Interval::Error(error) => {
                return self.fail_interval(schedule, row.as_ref(), now, error, totals);
            }
            Interval::Every(every) => *every,
        };
        let Some(row) = row else {
            engine.write(Lock::Namespace(&unit.namespace), |tx| {
                let mut params = key.to_vec();
                params.push(SqlValue::Integer(now + every));
                tx.run(
                    "INSERT INTO engine_schedules (behavior, schedule, namespace, schema, next_run_at) VALUES (?, ?, ?, ?, ?)
                     ON CONFLICT (behavior, schedule, namespace, schema) DO NOTHING",
                    &params,
                )?;
                Ok(())
            })?;
            self.core().due_at(now + every);
            return Ok(());
        };
        let next_run_at = row.i64("next_run_at")?;
        if next_run_at > now {
            self.core().due_at(next_run_at);
            return Ok(());
        }
        let principal = self
            .core()
            .principal
            .as_ref()
            .ok_or_else(|| Error::internal("the runner has no principal"))?;
        engine.write(Lock::Namespace(&unit.namespace), |tx| {
            // Read again under the namespace's lock: another process's
            // runner may have run it for this due time since the read
            // above, as two can for a moment when a lease's session ends
            // under a pass (D58). Then it is not due, and runs once.
            let Some(row) = schedule_row(tx, unit, &schedule.name)? else {
                return Ok(());
            };
            let next_run_at = row.i64("next_run_at")?;
            if next_run_at > now {
                self.core().due_at(next_run_at);
                return Ok(());
            }
            let previous = row.opt_i64("last_run_at")?;
            let failures = row.i64("failures")?;
            let ran = tx.savepoint(|tx| {
                let view = NamespaceView::load(&engine.namespaces, tx)?;
                let chain = Chain::caused(
                    principal,
                    &unit.namespace,
                    now,
                    EventCause {
                        behavior: unit.behavior.clone(),
                        event: None,
                        schedule: Some(schedule.name.clone()),
                        depth: 1,
                    },
                );
                let reach = Reach {
                    engine,
                    tx,
                    view: &view,
                    chain: &chain,
                };
                Execution::new(
                    reach,
                    Arc::clone(&unit.runtime),
                    &unit.schema,
                    unit.record.version.unwrap_or_default(),
                    "",
                    Own::None,
                    true,
                )
                .schedule(unit.index, &schedule.name, previous)
            });
            match ran {
                Err(error) => {
                    let failures = failures + 1;
                    let next = now + self.backoff(failures).min(every);
                    let mut params = vec![
                        SqlValue::Integer(failures),
                        SqlValue::from(describe(&error)),
                        SqlValue::Integer(next),
                    ];
                    params.extend(key.iter().cloned());
                    tx.run(
                        "UPDATE engine_schedules SET failures = ?, error = ?, next_run_at = ?
                         WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?",
                        &params,
                    )?;
                    self.core().due_at(next);
                    totals.failed += 1;
                }
                Ok(()) => {
                    let mut params = vec![SqlValue::Integer(now), SqlValue::Integer(now + every)];
                    params.extend(key.iter().cloned());
                    tx.run(
                        "UPDATE engine_schedules SET last_run_at = ?, next_run_at = ?, failures = 0, error = NULL
                         WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?",
                        &params,
                    )?;
                    self.core().due_at(now + every);
                    totals.scheduled += 1;
                }
            }
            Ok(())
        })
    }

    // fail_interval records a schedule whose interval the config cannot
    // give as a failed run, when the runner first finds it and each time it
    // comes due after. Nothing runs and the schedule never halts.
    fn fail_interval(
        &self,
        schedule: &ScheduleUnit,
        row: Option<&Row>,
        now: i64,
        error: &str,
        totals: &mut Totals,
    ) -> Result<()> {
        if let Some(row) = row {
            let next = row.i64("next_run_at")?;
            if next > now {
                self.core().due_at(next);
                return Ok(());
            }
        }
        let unit = &schedule.unit;
        let recorded = self.engine.write(Lock::Namespace(&unit.namespace), |tx| {
            // Read again under the namespace's lock, as a run does: a
            // failure another process's runner recorded for this due time
            // is not recorded twice.
            let failures = match schedule_row(tx, unit, &schedule.name)? {
                Some(row) if row.i64("next_run_at")? > now => {
                    return Ok(Err(row.i64("next_run_at")?));
                }
                Some(row) => row.i64("failures")? + 1,
                None => 1,
            };
            let next = now + self.backoff(failures);
            tx.run(
                "INSERT INTO engine_schedules (behavior, schedule, namespace, schema, next_run_at, failures, error) VALUES (?, ?, ?, ?, ?, ?, ?)
                 ON CONFLICT (behavior, schedule, namespace, schema) DO UPDATE SET next_run_at = excluded.next_run_at, failures = excluded.failures, error = excluded.error",
                &[
                    SqlValue::from(unit.behavior.as_str()),
                    SqlValue::from(schedule.name.as_str()),
                    SqlValue::from(unit.namespace.as_str()),
                    SqlValue::from(unit.schema.as_str()),
                    SqlValue::Integer(next),
                    SqlValue::Integer(failures),
                    SqlValue::from(error),
                ],
            )?;
            Ok(Ok(next))
        })?;
        match recorded {
            Ok(next) => {
                self.core().due_at(next);
                totals.failed += 1;
            }
            Err(next) => self.core().due_at(next),
        }
        Ok(())
    }

    // watched lists the schemas a subscription hears: its own and the ones
    // its behavior's watches names for the schema's config.
    fn watched(&self, unit: &Unit) -> Result<Vec<String>> {
        let bound = unit.bound();
        let Some(answer) = bound
            .behavior
            .implementation
            .watches(&bound.config, &unit.schema)
        else {
            return Ok(vec![unit.schema.clone()]);
        };
        let Some(named) = answer? else {
            return Err(Error::behavior(
                &unit.behavior,
                "reactions.watches returns a list of schema names, or null to turn the reactions off",
            ));
        };
        let mut watched = vec![unit.schema.clone()];
        for schema in named {
            check_schema_name(&schema)?;
            if !watched.contains(&schema) {
                watched.push(schema);
            }
        }
        Ok(watched)
    }

    fn backoff(&self, attempts: i64) -> i64 {
        let settings = self.core().settings;
        let shift = (attempts - 1).clamp(0, 30);
        settings
            .retry_initial_ms
            .saturating_mul(1_i64 << shift)
            .min(settings.retry_max_ms)
    }

    // discover finds every behavior with reactions or schedules on every
    // schema's live version in every namespace, and every subscription
    // that holds retention. It looks again when a schema is published, a
    // behavior registered, or a namespace created, archived or unarchived.
    fn discover(&self) -> Result<Arc<Discovery>> {
        let engine = self.engine;
        let registry = engine.catalog.registry();
        engine.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let published = tx
                .get(
                    "SELECT MAX(published_cursor) AS head FROM engine_schemas",
                    &[],
                )?
                .map(|row| row.opt_i64("head"))
                .transpose()?
                .flatten()
                .unwrap_or(0);
            let namespaces: Vec<String> = view
                .names()
                .into_iter()
                .map(|name| {
                    let archived = view.archived(&name);
                    format!("{name}:{}", u8::from(archived))
                })
                .collect();
            let key = format!(
                "{published}\u{0}{}\u{0}{}",
                namespaces.join(","),
                registry.names().join(",")
            );
            if let Some(known) = &self.core().state().discovery {
                if known.key == key {
                    return Ok(Arc::clone(known));
                }
            }
            let discovery = Arc::new(self.find_work(tx, &view, &registry, key)?);
            self.core().state().discovery = Some(Arc::clone(&discovery));
            Ok(discovery)
        })
    }

    fn find_work(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        registry: &BehaviorRegistry,
        key: String,
    ) -> Result<Discovery> {
        let engine = self.engine;
        let mut reactions: Vec<ReactionUnit> = Vec::new();
        let mut schedules: Vec<ScheduleUnit> = Vec::new();
        let mut holders: Vec<Holder> = Vec::new();
        for namespace in view.names() {
            let archived = view.archived(&namespace);
            for summary in engine.catalog.list(tx, view, &namespace)? {
                if summary.live_version.is_none() {
                    continue;
                }
                let Some(record) =
                    engine
                        .catalog
                        .find(tx, view, &summary.name, &namespace, Which::Live)?
                else {
                    continue;
                };
                for name in composed(&record) {
                    let registered = registry.lookup(&name);
                    let reacts = registered.is_some_and(|behavior| behavior.hooks.reactions);
                    let watches = registered.is_some_and(|behavior| behavior.hooks.watches);
                    // Reactions the config turns off hold nothing: turned on,
                    // they start at the publish that does it.
                    if registered.is_none()
                        || (reacts && !(watches && self.off_in(tx, &record, &name)))
                    {
                        holders.push(Holder {
                            key: SubscriptionKey {
                                behavior: name.clone(),
                                namespace: namespace.clone(),
                                schema: record.name.clone(),
                            },
                            start: self.start_of(tx, view, &record, &name, watches)?,
                            unregistered: registered.is_none(),
                        });
                    }
                }
                let runtime = match engine.catalog.runtime_of(tx, &record) {
                    Ok(runtime) => runtime,
                    // A version whose behaviors this engine cannot run has
                    // no work; its subscriptions show as inactive.
                    Err(Error::Engine(refusal))
                        if refusal.code() == EngineErrorCode::Unavailable =>
                    {
                        continue
                    }
                    Err(error) => return Err(error),
                };
                for (index, bound) in runtime.composition.behaviors.iter().enumerate() {
                    let unit = || Unit {
                        behavior: bound.behavior.name.clone(),
                        namespace: namespace.clone(),
                        schema: record.name.clone(),
                        record: record.clone(),
                        runtime: Arc::clone(&runtime),
                        index,
                        archived,
                    };
                    let hooks = &bound.behavior.hooks;
                    if hooks.reactions {
                        let off = reactions_off(bound, &record.name);
                        let start = if off {
                            0
                        } else {
                            self.start_of(tx, view, &record, &bound.behavior.name, hooks.watches)?
                        };
                        reactions.push(ReactionUnit {
                            unit: unit(),
                            start,
                            off,
                        });
                    }
                    for (name, _) in &hooks.schedules {
                        schedules.push(ScheduleUnit {
                            unit: unit(),
                            name: name.clone(),
                            every: interval_of(bound, name),
                        });
                    }
                }
            }
        }
        reactions.sort_by(|a, b| compare_utf16(&a.unit.key().id(), &b.unit.key().id()));
        schedules.sort_by(|a, b| {
            compare_utf16(
                &schedule_key(&a.unit, &a.name),
                &schedule_key(&b.unit, &b.name),
            )
        });
        Ok(Discovery {
            key,
            reactions,
            schedules,
            holders,
        })
    }

    // off_in reports whether a published version's config turns the
    // behavior's reactions off. A version this engine cannot compose
    // counts as on.
    fn off_in(&self, tx: &Tx<'_>, record: &SchemaRecord, behavior: &str) -> bool {
        let Ok(runtime) = self.engine.catalog.runtime_of(tx, record) else {
            return false;
        };
        runtime
            .composition
            .bound(behavior)
            .is_some_and(|bound| reactions_off(bound, &record.name))
    }

    // start_of is the cursor a subscription starts after: the publish of
    // the earliest version of the run of versions, up to the live one, that
    // compose the behavior with its reactions on, as the version keeps it.
    fn start_of(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        live: &SchemaRecord,
        behavior: &str,
        watches: bool,
    ) -> Result<i64> {
        let version = live.version.unwrap_or_default();
        let id = format!(
            "{}\u{0}{}\u{0}{version}\u{0}{behavior}\u{0}{}",
            live.namespace,
            live.name,
            if watches { "watches" } else { "" }
        );
        if let Some(known) = self.core().state().starts.get(&id) {
            return Ok(*known);
        }
        let mut first = version;
        let mut older_version = version - 1;
        while older_version >= 1 {
            let older = self.engine.catalog.find(
                tx,
                view,
                &live.name,
                &live.namespace,
                Which::Version(older_version),
            )?;
            let Some(older) = older else {
                break;
            };
            if !composed(&older).iter().any(|name| name == behavior)
                || (watches && self.off_in(tx, &older, behavior))
            {
                break;
            }
            first = older_version;
            older_version -= 1;
        }
        let start = tx
            .get(
                "SELECT published_cursor FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?",
                &[
                    SqlValue::from(live.namespace.as_str()),
                    SqlValue::from(live.name.as_str()),
                    SqlValue::Integer(first),
                ],
            )?
            .map(|row| row.opt_i64("published_cursor"))
            .transpose()?
            .flatten()
            .unwrap_or(0);
        self.core().state().starts.insert(id, start);
        Ok(start)
    }
}

// drive is the driver thread: a pass whenever a commit or a timer makes
// work due, while this process holds the runner's lease.
fn drive(
    engine: &Weak<Engine>,
    notifier: &superschematic_engine_storage::Notifier,
    stop: &AtomicBool,
) {
    loop {
        if stop.load(Ordering::SeqCst) {
            return;
        }
        let epoch = notifier.epoch();
        let wait = {
            let Some(engine) = engine.upgrade() else {
                return;
            };
            let runner = engine.runner();
            if runner.core().closed.load(Ordering::SeqCst) {
                return;
            }
            match runner.leased() {
                Ok(true) => {
                    if runner.pass_driven() {
                        continue;
                    }
                    let now = engine.clock.now();
                    match runner.next_wake() {
                        Some(at) => (at - now).clamp(0, MAX_TIMER_MS),
                        None => MAX_TIMER_MS,
                    }
                }
                Ok(false) => runner.core().settings.lease_retry_ms,
                Err(error) => {
                    runner.core().state().last_error = Some(describe(&error));
                    runner.core().settings.retry_initial_ms
                }
            }
        };
        if stop.load(Ordering::SeqCst) {
            return;
        }
        notifier.wait(epoch, Duration::from_millis(wait.max(0) as u64));
    }
}

const SUBSCRIPTION_COLUMNS: &str = "behavior, namespace, schema, cursor, halted, attempts, retry_at, failed_cursor, failed_at, error, skipped, skipped_cursor, skipped_reason";
const SCHEDULE_COLUMNS: &str =
    "behavior, schedule, namespace, schema, last_run_at, next_run_at, failures, error";

fn key_params(key: &SubscriptionKey) -> Vec<SqlValue> {
    vec![
        SqlValue::from(key.behavior.as_str()),
        SqlValue::from(key.namespace.as_str()),
        SqlValue::from(key.schema.as_str()),
    ]
}

fn subscription_row(tx: &Tx<'_>, key: &SubscriptionKey) -> Result<Option<Row>> {
    Ok(tx.get(
        format!(
            "SELECT {SUBSCRIPTION_COLUMNS} FROM engine_subscriptions WHERE behavior = ? AND namespace = ? AND schema = ?"
        ),
        &key_params(key),
    )?)
}

fn schedule_row(tx: &Tx<'_>, unit: &Unit, schedule: &str) -> Result<Option<Row>> {
    Ok(tx.get(
        format!(
            "SELECT {SCHEDULE_COLUMNS} FROM engine_schedules WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?"
        ),
        &[
            SqlValue::from(unit.behavior.as_str()),
            SqlValue::from(schedule),
            SqlValue::from(unit.namespace.as_str()),
            SqlValue::from(unit.schema.as_str()),
        ],
    )?)
}

// ensure_subscription returns a subscription's row, creating it at its
// start. A row from before the schema stopped composing the behavior and
// composed it again moves to the new start, with its failure cleared.
fn ensure_subscription(tx: &Tx<'_>, reaction: &ReactionUnit) -> Result<Row> {
    let key = reaction.unit.key();
    let mut params = key_params(&key);
    params.push(SqlValue::Integer(reaction.start));
    tx.run(
        "INSERT INTO engine_subscriptions (behavior, namespace, schema, cursor) VALUES (?, ?, ?, ?)
         ON CONFLICT (behavior, namespace, schema) DO NOTHING",
        &params,
    )?;
    let mut params = vec![SqlValue::Integer(reaction.start)];
    params.extend(key_params(&key));
    params.push(SqlValue::Integer(reaction.start));
    tx.run(
        "UPDATE engine_subscriptions SET cursor = ?, halted = 0, attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
         WHERE behavior = ? AND namespace = ? AND schema = ? AND cursor < ?",
        &params,
    )?;
    subscription_row(tx, &key)?
        .ok_or_else(|| Error::internal("a subscription's row is gone inside its transaction"))
}

// composed lists the behaviors a version's instance type composes, by name.
fn composed(record: &SchemaRecord) -> Vec<String> {
    refs_of(&record.document, &record.instance_type)
        .into_iter()
        .map(|reference| reference.name.to_owned())
        .collect()
}

// reactions_off reports whether a behavior's watches turns its reactions
// off for a config. One that fails, or answers anything but None, leaves
// them on, and the subscription's pass records what is wrong.
fn reactions_off(bound: &BoundBehavior, schema: &str) -> bool {
    if !bound.behavior.hooks.watches {
        return false;
    }
    matches!(
        bound.behavior.implementation.watches(&bound.config, schema),
        Some(Ok(None))
    )
}

// interval_of is a schedule's interval on one schema: its fixed interval,
// or what its config gives, held to the rule registration holds a fixed
// one to, or off when the config gives none.
fn interval_of(bound: &BoundBehavior, name: &str) -> Interval {
    match bound.behavior.implementation.every(name, &bound.config) {
        None => Interval::Error(describe(&Error::behavior(
            &bound.behavior.name,
            format!("schedule {name}: no such schedule"),
        ))),
        Some(Err(error)) => Interval::Error(describe(&error)),
        Some(Ok(None)) => Interval::Off,
        Some(Ok(Some(ms))) if (MIN_SCHEDULE_MS..=MAX_SAFE_INTEGER).contains(&ms) => {
            Interval::Every(ms)
        }
        Some(Ok(Some(ms))) => Interval::Error(describe(&Error::behavior(
            &bound.behavior.name,
            format!(
                "schedule {name}: everyMs(config) returns an integer of at least {MIN_SCHEDULE_MS}, got {ms}"
            ),
        ))),
    }
}

/// An error as the runner's status names it: a refusal by its code, any
/// other by the TypeScript engine's class name.
pub fn describe(error: &Error) -> String {
    match error {
        Error::Engine(refusal) => format!("{}: {}", refusal.code().as_str(), refusal.message()),
        Error::Behavior(defect) => format!("BehaviorError: {defect}"),
        Error::Usage(message) => format!("TypeError: {message}"),
        Error::Config(message) => format!("BehaviorConfigError: {message}"),
        other => format!("Error: {other}"),
    }
}

fn subscription_status(row: &Row) -> Result<SubscriptionStatus> {
    let halted = row.i64("halted")? == 1;
    let attempts = row.i64("attempts")?;
    let error = row.opt_text("error")?;
    Ok(SubscriptionStatus {
        behavior: row.text("behavior")?.to_owned(),
        namespace: row.text("namespace")?.to_owned(),
        schema: row.text("schema")?.to_owned(),
        state: if halted {
            SubscriptionState::Halted
        } else if attempts > 0 {
            SubscriptionState::Retrying
        } else {
            SubscriptionState::Active
        },
        cursor: row.i64("cursor")?,
        attempts,
        retry_at: row.opt_i64("retry_at")?,
        failure: match error {
            None => None,
            Some(error) => Some(SubscriptionFailure {
                cursor: row.opt_i64("failed_cursor")?,
                at: row.opt_i64("failed_at")?.unwrap_or(0),
                error: error.to_owned(),
            }),
        },
        skipped: row.i64("skipped")?,
        last_skip: match row.opt_i64("skipped_cursor")? {
            None => None,
            Some(cursor) => Some(SubscriptionSkip {
                cursor,
                reason: row.opt_text("skipped_reason")?.unwrap_or("").to_owned(),
            }),
        },
    })
}

fn fresh(unit: &Unit, start: i64) -> SubscriptionStatus {
    SubscriptionStatus {
        behavior: unit.behavior.clone(),
        namespace: unit.namespace.clone(),
        schema: unit.schema.clone(),
        state: SubscriptionState::Active,
        cursor: start,
        attempts: 0,
        retry_at: None,
        failure: None,
        skipped: 0,
        last_skip: None,
    }
}

fn schedule_status(row: &Row, every_ms: Option<i64>) -> Result<ScheduleStatus> {
    let failures = row.i64("failures")?;
    Ok(ScheduleStatus {
        behavior: row.text("behavior")?.to_owned(),
        schedule: row.text("schedule")?.to_owned(),
        namespace: row.text("namespace")?.to_owned(),
        schema: row.text("schema")?.to_owned(),
        state: if failures > 0 {
            ScheduleState::Retrying
        } else {
            ScheduleState::Active
        },
        every_ms,
        previous: row.opt_i64("last_run_at")?,
        next: Some(row.i64("next_run_at")?),
        failures,
        error: row.opt_text("error")?.map(str::to_owned),
    })
}

// off_status is a schedule's status when nothing is kept and nothing due.
fn off_status(unit: &Unit, schedule: &str) -> ScheduleStatus {
    ScheduleStatus {
        behavior: unit.behavior.clone(),
        schedule: schedule.to_owned(),
        namespace: unit.namespace.clone(),
        schema: unit.schema.clone(),
        state: ScheduleState::Off,
        every_ms: None,
        previous: None,
        next: None,
        failures: 0,
        error: None,
    }
}

fn schedule_id(status: &ScheduleStatus) -> String {
    format!(
        "{}\u{0}{}\u{0}{}\u{0}{}",
        status.behavior, status.schedule, status.namespace, status.schema
    )
}

fn schedule_key(unit: &Unit, name: &str) -> String {
    format!(
        "{}\u{0}{name}\u{0}{}\u{0}{}",
        unit.behavior, unit.namespace, unit.schema
    )
}

// put sets an entry of a list kept in first-insertion order by its id.
fn put<T>(list: &mut Vec<(String, T)>, id: String, value: T) {
    match list.iter_mut().find(|(known, _)| *known == id) {
        Some(entry) => entry.1 = value,
        None => list.push((id, value)),
    }
}
