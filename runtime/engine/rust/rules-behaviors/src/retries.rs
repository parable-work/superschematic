//! Retries, logical retries per failure class on a row (D16), ported onto
//! the pure core (D103) from the engine's
//! (`runtime/engine-workqueue/rust/src/retries.rs`), with retries that wait
//! (D104): a backoff after a failure, a time to give up after, a cap of the
//! attempts in a window, and a field whose value keys the counts. A worker
//! that ran records the attempt with `recordAttempt`: a success, with no
//! failure, or a failure of one of the config's classes. A class has its
//! own cap of failures, an optional hint, which recordAttempt returns for a
//! failure of the class to steer the worker's next attempt, and an optional
//! backoff, or is terminal, and every class counts against `totalAttempts`.
//! The row's `limitsField` may narrow or widen the caps for it; an unknown
//! or terminal class, and a value that is not a valid cap, is ignored. Once
//! the row exists, limitsField changes only with `limitsPermission`, and
//! never by the holder of its active lease, read from Lease's owned fields:
//! a worker does not raise its own caps.
//!
//! Retries passes D103's test: its state is its counts, its exhaustion,
//! when the row may run again and since when it has failed, the key its
//! counts were made under, its `attempts` sub-table and the deadline its
//! `then` arms; it has rules over how that state changes (an attempt counted
//! against caps, a wait that grows, a window that holds the next start, a
//! new key that starts them again) and over the moves and takes it holds;
//! and it recurs, as a sandbox tried once per version, an agent start
//! retried with a doubling wait for half an hour, a record that resumes at
//! most three times in thirty minutes (D104).
//!
//! The rules for a failure, the engine's, in order: a terminal class counts
//! the failure and exhausts the row; a failure of a class with no room
//! left, its own cap or the total, is not counted and exhausts the row;
//! otherwise it counts, and exhausts the row when the total reaches its cap,
//! or when its class reaches its cap and no other class has room. Then the
//! result: a success's is kept; a failure's only with `keepBest`, when it
//! has a score above the best kept score by at least `minDelta` and no
//! `neverRegress` predicate that held for the last kept attempt fails for
//! it. Then stuck detection, with `stuckAfter`: a streak of failures that
//! are not kept and carry the same signature exhausts the row as stuck.
//! Then `giveUpAfterMs` (D104): a failure that long or longer after the
//! first failure since the row's last success (`failingSince`) exhausts the
//! row once it has counted. On exhaustion the status moves to
//! `exhaustedState` through Workflow's transition (`Cx::call`), only from
//! the config's from states; a veto leaves the status and the row exhausted
//! all the same. Once exhausted, the guard refuses every transition but
//! into exhaustedState, Lease's acquire and Queue's claim, and recordAttempt
//! refuses further attempts.
//!
//! The waits (D104). A class may set `backoff: { firstMs, maxMs, factor? }`:
//! a failure of it that does not exhaust the row sets the owned `retryAt` to
//! now plus `firstMs × factor^(n-1)`, at most `maxMs`, `n` the failures of
//! the class since the row's last success (factor 2 when absent); a success,
//! a failure of a class without one, an exhausting failure and a new key
//! clear it. Until then a transition into a state of `retryInto` and Lease's
//! acquire are vetoed `backing_off`, with `retryAt` in the details, whoever
//! calls; recording an attempt never waits. With `then`, a deadline `retry`
//! armed at `retryAt` moves the row through Workflow's transition, while its
//! status is one of then's from; a status that enters one of them once
//! retryAt has passed (the deadline having fallen due while the row was
//! elsewhere, say) arms it again at once. The move is the request of the
//! worker that runs the deadline, which the core tells each guard (D103),
//! so another principal's active lease holds it as it holds Timers' fire:
//! a guard's veto refuses the deadline's command, which the worker releases
//! to fall due a minute on, a move into retryInto that the window holds
//! arms the deadline again at the window's until, and a move the worker may
//! not make leaves the status. With `window:
//! { attempts, ms }`, while the window holds `attempts` recorded attempts a
//! start, a transition into retryInto or Lease's acquire, is vetoed
//! `throttled` until the oldest of them leaves it, `until` in the details.
//! With `keyField`, the row's own field whose value keys every class's
//! count, a row whose field holds another value than the one its counts
//! were made under (`keyedBy`) reads as one with no attempts, and an update
//! through the core that changes the field resets the counts, exhaustion and
//! the wait at once; so `{ attempts: 1 }` with keyField `version` is one
//! failure per version. A class takes backoff, since D104 says a class may
//! set one; the config takes the rest, since a row has one retryAt, one
//! first failure since its last success, one window and one key. Each is
//! refused at define when nothing waits for it.
//!
//! The counts, the flags, the best kept score and the waits are owned
//! columns (`classAttempts` a JSON object of every class's count); the
//! `attempts` sub-table keeps every attempt, as the engine's history did,
//! its detail among it, keyed by its number in ten digits, which the
//! window, the streak, the last kept predicates and a backoff's count read.
//! A field with no value reads as the engine's default (a total of 0, no
//! class counted, neither exhausted nor stuck), so a row that predates its
//! use starts with no attempts.
//!
//! `resultField`, which the engine wrote a kept result to through an
//! update, is refused on a table, last of the rules: the core runs no update
//! an operation makes until Revisions (D103, step 4). A write around the
//! core (raw SQL) of keyField is read through the key rule at the next
//! decision, and the columns reset at the next attempt: Retries hears no
//! change.
//!
//! Which Workflow exhaustedState, from, retryInto and then name states of
//! is the config's `status` (D104), as StateRules' is: the named Workflow
//! use, else the type's one.
//!
//! configChange: any config may change. Retries can be added to a type that
//! has rows, which start with no attempts, and cannot be removed from one:
//! the attempts its rows recorded would stay behind.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest,
    Hook, Refusal, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::declared;
use crate::flow::{flow_of, leads};
use crate::lease;
use crate::moves::{self, StatusMove};
use crate::schema::{self, Issues};
use crate::values::{date_time_text, equal};
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Retries";

/// The sub-table of the attempts a row recorded.
pub const ATTEMPTS: &str = "attempts";

/// The operation that records an attempt.
pub const RECORD_ATTEMPT: &str = "recordAttempt";

/// The deadline then arms, at retryAt.
pub const RETRY: &str = "retry";

/// The IR types limitsField may have: an object field, held to the build's
/// list by its test.
pub const LIMITS_TYPES: [&str; 1] = ["Generic.JSON"];

/// Retries' declaration, the file `make behaviors` writes for the work
/// queue's crate, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-workqueue/rust/declarations/Retries.behavior.json");

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 16] = [
    "classes",
    "exhaustedState",
    "from",
    "giveUpAfterMs",
    "keepBest",
    "keyField",
    "limitsField",
    "limitsPermission",
    "permission",
    "resultField",
    "retryInto",
    "status",
    "stuckAfter",
    "then",
    "totalAttempts",
    "window",
];
const CLASS_KEYS: [&str; 3] = ["attempts", "backoff", "hint"];
const BACKOFF_KEYS: [&str; 3] = ["factor", "firstMs", "maxMs"];
const KEEP_BEST_KEYS: [&str; 2] = ["minDelta", "neverRegress"];
const WINDOW_KEYS: [&str; 2] = ["attempts", "ms"];
const PARAMS_KEYS: [&str; 6] = [
    "detail",
    "failure",
    "predicates",
    "result",
    "score",
    "signature",
];

// What a class's name and a predicate's name are held to.
const CLASS_NAME: &str = "^[A-Za-z][A-Za-z0-9_.-]{0,63}$";
const MAX_HINT: usize = 2000;
const MAX_SIGNATURE: usize = 512;

/// An attempt's key is its number in this many digits, zero-padded.
const NUMBER_WIDTH: usize = 10;

/// The operations an exhausted row refuses besides a transition: taking it
/// again.
const TAKES: [(&str, &str); 2] = [(lease::NAME, lease::ACQUIRE), ("Queue", "claim")];

/// How long a failure of a class waits before the row runs again (D104).
#[derive(Clone, Debug, PartialEq)]
pub struct RetryBackoff {
    pub first_ms: i64,
    pub max_ms: i64,
    /// 2 when the config gives none.
    pub factor: f64,
}

impl RetryBackoff {
    /// The wait after the `n`th failure of the class since the row's last
    /// success: `firstMs × factor^(n-1)`, at most `maxMs`, computed as a
    /// double and capped before it becomes a whole number.
    pub fn wait(&self, n: i64) -> i64 {
        let exponent = i32::try_from((n - 1).max(0)).unwrap_or(i32::MAX);
        let wait = (self.first_ms as f64 * self.factor.powi(exponent)).floor();
        if wait >= self.max_ms as f64 {
            self.max_ms
        } else {
            wait as i64
        }
    }
}

/// A failure class: a cap of failures with an optional hint for the next
/// attempt and an optional backoff, or terminal.
#[derive(Clone, Debug, PartialEq)]
pub enum RetryClass {
    Capped {
        attempts: i64,
        hint: Option<String>,
        backoff: Option<RetryBackoff>,
    },
    Terminal,
}

impl RetryClass {
    fn backoff(&self) -> Option<&RetryBackoff> {
        match self {
            RetryClass::Capped { backoff, .. } => backoff.as_ref(),
            RetryClass::Terminal => None,
        }
    }
}

/// keepBest, parsed: minDelta filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct RetriesKeepBest {
    pub min_delta: f64,
    pub never_regress: Vec<String>,
}

/// window, parsed (D104).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RetriesWindow {
    pub attempts: i64,
    pub ms: i64,
}

/// Retries' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq)]
pub struct RetriesConfig {
    /// By name, by code point, as the engine read its canonical document.
    pub classes: Vec<(String, RetryClass)>,
    pub total_attempts: i64,
    pub limits_field: Option<String>,
    pub limits_permission: Option<String>,
    pub keep_best: Option<RetriesKeepBest>,
    pub stuck_after: Option<i64>,
    pub exhausted_state: String,
    /// The states exhaustion moves the status from.
    pub from: Vec<String>,
    pub permission: Option<String>,
    /// The Workflow use the states name (D104).
    pub status: String,
    pub retry_into: Vec<String>,
    pub then: Option<StatusMove>,
    pub give_up_after_ms: Option<i64>,
    pub window: Option<RetriesWindow>,
    pub key_field: Option<String>,
    /// Whether the type has a Lease use, whose holder does not change
    /// limitsField and whose acquire waits.
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

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the engine's rules in its order and words, then the
    /// waits' (D104), then what a table's Retries refuses.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<RetriesConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let text = |name: &str| raw.get(name).and_then(Value::as_str).map(str::to_owned);
        let type_name = target.type_name();
        let field_of = |at: &str, field: &str| -> Result<String, ConfigError> {
            let fields = target.fields();
            match fields.iter().find(|known| known.name == field) {
                Some(found) => Ok(found.type_name.clone()),
                None => {
                    let names: Vec<&str> = fields.iter().map(|known| known.name.as_str()).collect();
                    Err(format!(
                        "{at} \"{field}\" is not a field of {type_name} (its fields: {})",
                        if names.is_empty() {
                            "none".to_owned()
                        } else {
                            names.join(", ")
                        }
                    )
                    .into())
                }
            }
        };
        let limits_field = text("limitsField");
        if let Some(field) = &limits_field {
            if !LIMITS_TYPES.contains(&field_of("limitsField", field)?.as_str()) {
                return Err(format!(
                    "limitsField \"{field}\" is not an object field of {type_name}"
                )
                .into());
            }
        }
        let result_field = text("resultField");
        if let Some(field) = &result_field {
            field_of("resultField", field)?;
        }
        let flow = flow_of(target, raw.get("status").and_then(Value::as_str))?;
        let states = flow.listed();
        let exhausted_state = text("exhaustedState").unwrap_or_default();
        let to = exhausted_state.as_str();
        if flow.lacks(to) {
            return Err(format!(
                "exhaustedState \"{to}\" is not a state of the type's Workflow ({states})"
            )
            .into());
        }
        let from = match raw.get("from") {
            None => flow
                .states
                .iter()
                .filter(|state| *state != to && !moves::is_terminal(&flow, state))
                .cloned()
                .collect(),
            Some(given) => {
                let given = moves::strings(Some(given));
                for state in &given {
                    if flow.lacks(state) {
                        return Err(format!(
                            "from names \"{state}\", which is not a state of the type's Workflow ({states})"
                        )
                        .into());
                    }
                    if state == to {
                        return Err(format!(
                            "from names exhaustedState \"{to}\", which exhaustion cannot move the status from"
                        )
                        .into());
                    }
                    if !flow.states.is_empty() && !leads(&flow.transitions, state, to) {
                        return Err(format!(
                            "from: no transition of the type's Workflow leads from \"{state}\" to \"{to}\""
                        )
                        .into());
                    }
                }
                given
            }
        };
        let key_field = text("keyField");
        if let Some(field) = &key_field {
            if field_of("keyField", field)? == "Generic.JSON" {
                return Err(format!(
                    "keyField \"{field}\" is a Generic.JSON field of {type_name}: the value that keys the counts is one scalar"
                )
                .into());
            }
        }
        let empty = Map::new();
        let raw_classes = raw
            .get("classes")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        // The classes in the order the engine read them, its canonical
        // document's: by name, by code point.
        let mut named: Vec<(&String, &Value)> = raw_classes.iter().collect();
        named.sort_by(|a, b| a.0.cmp(b.0));
        let mut classes = Vec::new();
        for (name, spec) in named {
            let class = match spec {
                Value::String(_) => RetryClass::Terminal,
                spec => {
                    let backoff = spec.get("backoff").map(|backoff| RetryBackoff {
                        first_ms: backoff.get("firstMs").and_then(whole).unwrap_or(0),
                        max_ms: backoff.get("maxMs").and_then(whole).unwrap_or(0),
                        factor: backoff.get("factor").and_then(Value::as_f64).unwrap_or(2.0),
                    });
                    if let Some(backoff) = &backoff {
                        if backoff.max_ms < backoff.first_ms {
                            return Err(format!(
                                "classes.{name}.backoff: maxMs ({}) is shorter than firstMs ({})",
                                backoff.max_ms, backoff.first_ms
                            )
                            .into());
                        }
                    }
                    RetryClass::Capped {
                        attempts: spec.get("attempts").and_then(whole).unwrap_or(0),
                        hint: spec.get("hint").and_then(Value::as_str).map(str::to_owned),
                        backoff,
                    }
                }
            };
            classes.push((name.clone(), class));
        }
        let retry_into = moves::strings(raw.get("retryInto"));
        for state in &retry_into {
            if flow.lacks(state) {
                return Err(format!(
                    "retryInto names \"{state}\", which is not a state of the type's Workflow ({states})"
                )
                .into());
            }
            if *state == exhausted_state {
                return Err(format!(
                    "retryInto names exhaustedState \"{state}\", which an exhausted row moves to and stays in"
                )
                .into());
            }
        }
        let then = raw.get("then").map(StatusMove::parse);
        if let Some(then) = &then {
            moves::check_move(&flow, "then", then)?;
        }
        let window = raw.get("window").map(|window| RetriesWindow {
            attempts: window.get("attempts").and_then(whole).unwrap_or(0),
            ms: window.get("ms").and_then(whole).unwrap_or(0),
        });
        let leased = target
            .uses()
            .iter()
            .any(|entry| entry.behavior == lease::NAME);
        // What each wait waits for.
        let backed_off = classes
            .iter()
            .find(|(_, class)| class.backoff().is_some())
            .map(|(name, _)| name.as_str());
        if then.is_some() && backed_off.is_none() {
            return Err(
                "then moves the row when its backoff ends, and no class sets a backoff".into(),
            );
        }
        if !retry_into.is_empty() && backed_off.is_none() && window.is_none() {
            return Err(
                "retryInto names the states an attempt starts in, which wait for a backoff or the window, and the config sets neither"
                    .into(),
            );
        }
        if let (Some(class), true, None, false) = (backed_off, retry_into.is_empty(), &then, leased)
        {
            return Err(format!(
                "classes.{class}.backoff sets when the row runs again (retryAt), which a move into retryInto, then and Lease's acquire wait for: the config names no retryInto and no then, and the type has no Lease"
            )
            .into());
        }
        if window.is_some() && retry_into.is_empty() && !leased {
            return Err(
                "window caps the attempts that start, a move into retryInto or Lease's acquire: the config names no retryInto, and the type has no Lease"
                    .into(),
            );
        }
        // What a table's Retries refuses, after every rule.
        if result_field.is_some() {
            return Err(
                "resultField writes a kept result to a field of the row through an update an operation makes, which waits for the core to run one (D103, step 4: Revisions)"
                    .into(),
            );
        }
        Ok(RetriesConfig {
            classes,
            total_attempts: raw.get("totalAttempts").and_then(whole).unwrap_or(0),
            limits_field,
            limits_permission: text("limitsPermission"),
            keep_best: raw.get("keepBest").map(|keep| RetriesKeepBest {
                min_delta: keep.get("minDelta").and_then(Value::as_f64).unwrap_or(0.0),
                never_regress: moves::strings(keep.get("neverRegress")),
            }),
            stuck_after: raw.get("stuckAfter").and_then(whole),
            exhausted_state,
            from,
            permission: text("permission"),
            status: flow.use_id,
            retry_into,
            then,
            give_up_after_ms: raw.get("giveUpAfterMs").and_then(whole),
            window,
            key_field,
            leased,
        })
    }
}

/// The row's counts, its exhaustion and its waits, as every hook reads
/// them: the owned fields, a field with no value its default, and the
/// default state whole for a row whose keyField holds another value than
/// the one its counts were made under.
#[derive(Clone, Debug, Default, PartialEq)]
struct State {
    total: i64,
    /// Every class of the config, in its order.
    counted: Vec<(String, i64)>,
    best_score: Option<f64>,
    exhausted: bool,
    stuck: bool,
    retry_at: Option<i64>,
    failing_since: Option<i64>,
}

// key_text is a keyField value's text: a string as it is, a whole number's
// digits, a boolean's true or false, anything else its JSON text; None for
// no value.
fn key_text(value: Option<&Value>) -> Option<String> {
    match value? {
        Value::Null => None,
        Value::String(text) => Some(text.clone()),
        Value::Bool(flag) => Some(flag.to_string()),
        Value::Number(number) => Some(match whole(&Value::Number(number.clone())) {
            Some(n) => n.to_string(),
            None => number.to_string(),
        }),
        other => Some(serde_json::to_string(other).unwrap_or_default()),
    }
}

// current_key is the text of the row's own keyField value, the key its
// next attempt counts under; None without keyField.
fn current_key(view: &View<'_, RetriesConfig>) -> Option<String> {
    let field = view.config().key_field.as_ref()?;
    key_text(view.own_field(field).as_ref())
}

fn zeros(config: &RetriesConfig) -> Vec<(String, i64)> {
    config
        .classes
        .iter()
        .map(|(name, _)| (name.clone(), 0))
        .collect()
}

fn state_of(view: &View<'_, RetriesConfig>) -> State {
    let config = view.config();
    let fresh = State {
        counted: zeros(config),
        ..State::default()
    };
    if config.key_field.is_some() {
        let held = view
            .get("keyedBy")
            .and_then(|key| key.as_str().map(str::to_owned));
        if held != current_key(view) {
            return fresh;
        }
    }
    let flag = |field: &str| matches!(view.get(field), Some(Value::Bool(true)));
    let held_counts = view.get("classAttempts");
    let counted = config
        .classes
        .iter()
        .map(|(name, _)| {
            let count = held_counts
                .as_ref()
                .and_then(|counts| counts.get(name))
                .and_then(whole)
                .unwrap_or(0);
            (name.clone(), count)
        })
        .collect();
    State {
        total: view.get("total").as_ref().and_then(whole).unwrap_or(0),
        counted,
        best_score: view.get("bestScore").and_then(|score| score.as_f64()),
        exhausted: flag("exhausted"),
        stuck: flag("stuck"),
        retry_at: view.get("retryAt").as_ref().and_then(whole),
        failing_since: view.get("failingSince").as_ref().and_then(whole),
    }
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

/// The caps a row's attempts count against: the config's, narrowed or
/// widened by the row's limitsField.
struct Caps<'c> {
    config: &'c RetriesConfig,
    limits: Option<Map<String, Value>>,
    total: i64,
}

// cap reads a cap of at least `least`: a safe whole number; `None` for any
// other value.
fn cap(value: Option<&Value>, least: i64) -> Option<i64> {
    let number = value?.as_f64()?;
    (number.fract() == 0.0 && number.abs() <= 9_007_199_254_740_991.0 && number >= least as f64)
        .then_some(number as i64)
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

// caps_of reads the caps. An unknown or terminal class, and a value that is
// not a valid cap, is ignored.
fn caps_of<'c>(view: &View<'c, RetriesConfig>) -> Caps<'c> {
    let config = view.config();
    let limits = match &config.limits_field {
        Some(field) => match view.own_field(field) {
            Some(Value::Object(limits)) => Some(limits),
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
    Caps {
        config,
        limits,
        total,
    }
}

// room reports whether a class other than failure may still fail once more.
fn room(config: &RetriesConfig, caps: &Caps<'_>, counted: &[(String, i64)], failure: &str) -> bool {
    config.classes.iter().any(|(name, class)| {
        name != failure
            && *class != RetryClass::Terminal
            && counted_of(counted, name) < caps.of(name)
    })
}

/// The attempts a row recorded, as the decisions read them.
struct History {
    rows: Vec<SubRow>,
    /// The key the next attempt counts under, None without keyField.
    key: Option<String>,
}

impl History {
    fn of(view: &View<'_, RetriesConfig>) -> Hook<History> {
        Ok(History {
            rows: view.sub_rows(ATTEMPTS)?,
            key: current_key(view),
        })
    }

    // keyed is the rows counted under the current key, newest first.
    fn keyed(&self, keyed: bool) -> impl Iterator<Item = &SubRow> {
        let key = self.key.clone();
        self.rows.iter().rev().filter(move |row| {
            !keyed || row.value("keyedBy").and_then(Value::as_str) == key.as_deref()
        })
    }

    // last_kept is the predicates of the newest kept attempt under the
    // current key, which a kept result never loses one of.
    fn last_kept(&self, keyed: bool) -> Option<Map<String, Value>> {
        self.keyed(keyed)
            .find(|row| row.value("kept") == Some(&Value::Bool(true)))
            .and_then(|row| row.value("predicates").and_then(Value::as_object).cloned())
    }

    // streak is how many attempts in a row, newest first under the current
    // key, are failures not kept with a signature, the new one's included.
    fn streak(&self, keyed: bool, signature: &str) -> i64 {
        1 + self
            .keyed(keyed)
            .take_while(|row| {
                row.value("failure").is_some()
                    && row.value("kept") != Some(&Value::Bool(true))
                    && row.value("signature").and_then(Value::as_str) == Some(signature)
            })
            .count() as i64
    }

    // failures_since_success is the failures of a class back to the newest
    // success under the current key, the new one's included: a backoff's n.
    fn failures_since_success(&self, keyed: bool, class: &str) -> i64 {
        1 + self
            .keyed(keyed)
            .take_while(|row| row.value("failure").is_some())
            .filter(|row| row.value("failure").and_then(Value::as_str) == Some(class))
            .count() as i64
    }

    // in_window is the times of the attempts recorded after now - ms, in
    // the order they were recorded, whatever their key.
    fn in_window(&self, now: i64, ms: i64) -> Vec<i64> {
        self.rows
            .iter()
            .filter_map(|row| row.value("at").and_then(whole))
            .filter(|at| *at > now - ms)
            .collect()
    }
}

// keeps decides whether a failed attempt's result is kept: only with
// keepBest, and only one that beats the best kept score by minDelta and
// loses no neverRegress predicate.
fn keeps(
    config: &RetriesConfig,
    state: &State,
    history: &History,
    score: Option<f64>,
    predicates: Option<&Map<String, Value>>,
) -> bool {
    let (Some(keep_best), Some(score)) = (&config.keep_best, score) else {
        return false;
    };
    if let Some(best) = state.best_score {
        if !(score > best && score - best >= keep_best.min_delta) {
            return false;
        }
    }
    if !keep_best.never_regress.is_empty() {
        if let Some(baseline) = history.last_kept(config.key_field.is_some()) {
            let held = |predicates: Option<&Map<String, Value>>, name: &str| {
                predicates.and_then(|predicates| predicates.get(name)) == Some(&Value::Bool(true))
            };
            if keep_best
                .never_regress
                .iter()
                .any(|name| held(Some(&baseline), name) && !held(predicates, name))
            {
                return false;
            }
        }
    }
    true
}

// iso is an instant as a refusal names it: JavaScript's toISOString, the
// epoch digits where it gives none.
fn iso(ms: i64) -> String {
    date_time_text(ms).unwrap_or_else(|| ms.to_string())
}

fn said(reason: impl Into<String>, code: &str, details: Option<Value>) -> Veto {
    let veto = Veto::new(reason).with_code(code);
    match details {
        Some(Value::Object(members)) => veto.with_details(members),
        _ => veto,
    }
}

// target names the row a message is about: its type and its key.
fn target<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// limits_guard holds a change of limitsField to limitsPermission, and keeps
// it from the holder of the row's active lease, read from Lease's owned
// fields. An update that leaves the field as it was passes.
fn limits_guard(view: &View<'_, RetriesConfig>, after: &Map<String, Value>) -> Hook<Option<Veto>> {
    let config = view.config();
    let Some(field) = &config.limits_field else {
        return Ok(None);
    };
    if equal(view.own_field(field).as_ref(), after.get(field)) {
        return Ok(None);
    }
    let Some(permission) = &config.limits_permission else {
        return Ok(Some(said(
            format!("{field} holds the instance's caps, which change only with limitsPermission, and the config names none"),
            "not_configured",
            None,
        )));
    };
    if !view.can(permission)? {
        return Err(view.forbidden(format!(
            "{} may not change {field}, the caps of {}: it needs permission {permission}",
            view.caller().subject,
            target(view)
        )));
    }
    if config.leased && holds_lease(view) {
        return Ok(Some(said(
            format!("{field} holds the caps its holder's attempts count against, so the holder of its lease does not change it"),
            "limits_fixed",
            None,
        )));
    }
    Ok(None)
}

// holds_lease says whether the caller holds the row's active lease, by
// Lease's rule over its owned fields (D104): a holder, and now before
// expiresAt, which Lease keeps at most the longest hold.
fn holds_lease(view: &View<'_, RetriesConfig>) -> bool {
    let holder = view.get_of(lease::NAME, "holder");
    let expires = view
        .get_of(lease::NAME, "expiresAt")
        .as_ref()
        .and_then(whole);
    holder.as_ref().and_then(Value::as_str) == Some(view.caller().subject.as_str())
        && expires.is_some_and(|expires| view.now() < expires)
}

// waits is the veto of a start the row's backoff or window holds: a
// transition into a state of retryInto, or Lease's acquire (D104).
fn waits(view: &View<'_, RetriesConfig>, state: &State) -> Hook<Option<Veto>> {
    let now = view.now();
    if let Some(retry_at) = state.retry_at.filter(|retry_at| now < *retry_at) {
        return Ok(Some(said(
            format!(
                "it waits until {} before its next attempt: its last one failed in a class with a backoff",
                iso(retry_at)
            ),
            "backing_off",
            Some(json!({ "retryAt": retry_at })),
        )));
    }
    if let (Some(window), Some((count, until))) = (&view.config().window, throttled(view)?) {
        return Ok(Some(said(
            format!(
                "it has had {count} attempts in the last {} ms, and its window allows {}: its next attempt waits until {}",
                window.ms,
                window.attempts,
                iso(until)
            ),
            "throttled",
            Some(json!({ "until": until })),
        )));
    }
    Ok(None)
}

// throttled is, while the window holds as many attempts as it allows, how
// many it holds and when the oldest of those it counts leaves it; None
// without a window or while it has room.
fn throttled(view: &View<'_, RetriesConfig>) -> Hook<Option<(i64, i64)>> {
    let Some(window) = &view.config().window else {
        return Ok(None);
    };
    let times = History::of(view)?.in_window(view.now(), window.ms);
    let count = times.len() as i64;
    if count < window.attempts {
        return Ok(None);
    }
    let oldest = times[usize::try_from(count - window.attempts).unwrap_or(0)];
    Ok(Some((count, oldest + window.ms)))
}

// status_of is the status of the Workflow the states name, as the run has
// it.
fn status_of(view: &View<'_, RetriesConfig>) -> Option<String> {
    view.get_of(&view.config().status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
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

// score_value is a double as JavaScript writes it: a whole number by its
// digits.
fn score_value(score: f64) -> Value {
    if score.fract() == 0.0 && score.abs() < 9_007_199_254_740_992.0 {
        Value::from(score as i64)
    } else {
        Value::from(score)
    }
}

/// Retries: logical retries per failure class on each row, with waits.
#[derive(Clone, Copy, Debug, Default)]
pub struct Retries;

impl Behavior for Retries {
    type Config = RetriesConfig;

    // The declaration file names its owned fields, its attempts sub-table
    // and that it arms deadlines (D103, D104).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<RetriesConfig, ConfigError> {
        RetriesConfig::parse(raw, target)
    }

    // Any config may change; Retries joins a type with rows and does not
    // leave one, whose attempts would stay behind.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&RetriesConfig>,
        after: Option<&RetriesConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none())
            .then(|| "the attempts its instances recorded would stay behind".to_owned()))
    }

    fn check_params(
        &self,
        _config: &RetriesConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != RECORD_ATTEMPT {
            return Vec::new();
        }
        params_issues(params).into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, RetriesConfig>, _params: &Value) -> Hook<()> {
        let config = cx.config();
        cx.set("total", 0)?;
        cx.set("classAttempts", counts_json(&zeros(config)))?;
        cx.set("exhausted", false)?;
        cx.set("stuck", false)?;
        if let Some(key) = current_key(cx) {
            cx.set("keyedBy", key)?;
        }
        Ok(())
    }

    // guard: once exhausted, the row moves only to exhaustedState and is not
    // taken again; a start waits for the backoff and the window; limitsField
    // changes only as a limit does. A create sets the caps it is created
    // with, which no permission guards.
    fn guard(
        &self,
        view: &View<'_, RetriesConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        match request {
            GuardRequest::Update { after, .. } => limits_guard(view, after),
            GuardRequest::Operation {
                use_id,
                behavior,
                operation,
                params,
                ..
            } => {
                let transition = *use_id == config.status && *operation == workflow::TRANSITION;
                if !transition && !TAKES.contains(&(*behavior, *operation)) {
                    return Ok(None);
                }
                let state = state_of(view);
                let to = params.get("to").and_then(Value::as_str);
                if state.exhausted {
                    if transition {
                        let into = &config.exhausted_state;
                        return Ok((to != Some(into.as_str())).then(|| {
                            said(
                                format!(
                                    "its retries are exhausted, so its status moves only to {into}"
                                ),
                                "exhausted",
                                None,
                            )
                        }));
                    }
                    return Ok(Some(said(
                        "its retries are exhausted, so it is not taken again",
                        "exhausted",
                        None,
                    )));
                }
                let starts = if transition {
                    to.is_some_and(|to| config.retry_into.iter().any(|state| state == to))
                } else {
                    (*behavior, *operation) == (lease::NAME, lease::ACQUIRE)
                };
                if starts {
                    return waits(view, &state);
                }
                Ok(None)
            }
            _ => Ok(None),
        }
    }

    fn operation(&self, cx: &Cx<'_, RetriesConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            RECORD_ATTEMPT => record_attempt(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // then's deadline, at retryAt: a row exhausted since, or with no wait
    // left, moves nothing, and one whose wait moved past now arms again.
    // A row whose status is not one of then's from moves nothing; its
    // status's next entry into one arms the deadline again (after_change).
    // A move into retryInto that the window holds arms it again at the
    // window's until. Else it moves the row through Workflow's transition,
    // which the core tells each guard is the worker's request, so another
    // principal's active lease holds it as it holds Timers' fire: a guard's
    // veto refuses the command, which the worker releases to fall due a
    // minute on, and a move the worker may not make leaves the status.
    fn deadline(&self, cx: &Cx<'_, RetriesConfig>, name: &str, _payload: &Value) -> Hook<()> {
        if name != RETRY {
            return Err(cx.defect(format!(
                "deadline {name} fell due, and Retries arms only {RETRY}"
            )));
        }
        let config = cx.config();
        let state = state_of(cx);
        let (Some(retry_at), Some(then), false) = (state.retry_at, &config.then, state.exhausted)
        else {
            return Ok(());
        };
        if cx.now() < retry_at {
            return cx.arm(RETRY, retry_at, Value::Null);
        }
        if !status_of(cx).is_some_and(|status| then.moves_from(&status)) {
            return Ok(());
        }
        if config.retry_into.contains(&then.transition) {
            if let Some((_, until)) = throttled(cx)? {
                return cx.arm(RETRY, until, Value::Null);
            }
        }
        let to = json!({ "to": then.transition });
        match cx.call(&config.status, workflow::TRANSITION, &to) {
            Ok(_) => Ok(()),
            Err(Stop::Refuse(refusal)) if refusal.code == Code::Forbidden => Ok(()),
            Err(stop) => Err(stop),
        }
    }

    // A change of keyField through the core starts the counts, exhaustion
    // and the wait again at once (D104). With then, a write through the
    // core that moves the status into one of then's from once retryAt has
    // passed (the deadline having fallen due while the row was elsewhere,
    // say) arms it again now.
    fn after_change(&self, cx: &Cx<'_, RetriesConfig>, change: &Change<'_>) -> Hook<()> {
        if let Some(then) = &cx.config().then {
            rearm(cx, then, change)?;
        }
        let Change::Update { before, .. } = change else {
            return Ok(());
        };
        let config = cx.config();
        let Some(field) = &config.key_field else {
            return Ok(());
        };
        let now = cx.own();
        if key_text(before.get(field)) == key_text(now.get(field)) {
            return Ok(());
        }
        cx.set("total", 0)?;
        cx.set("classAttempts", counts_json(&zeros(config)))?;
        cx.set("exhausted", false)?;
        cx.set("stuck", false)?;
        cx.set("bestScore", Value::Null)?;
        cx.set("retryAt", Value::Null)?;
        cx.set("failingSince", Value::Null)?;
        cx.set(
            "keyedBy",
            key_text(now.get(field)).map_or(Value::Null, Value::from),
        )?;
        if config.then.is_some() {
            cx.clear(RETRY)?;
        }
        Ok(())
    }
}

// rearm arms then's deadline at now when the change moved the status into
// one of then's from (from another state, read as the host read the row
// before the command) while the row is not exhausted and its retryAt has
// passed: the wait is over, as when the deadline fell due while the row
// was elsewhere. A create, a delete and a change that leaves the status
// where it was arm nothing, and the deadline's own command leaves the
// status in from only when its move was refused.
fn rearm(cx: &Cx<'_, RetriesConfig>, then: &StatusMove, change: &Change<'_>) -> Hook<()> {
    if matches!(change, Change::Create | Change::Delete) {
        return Ok(());
    }
    let Some(status) = status_of(cx).filter(|status| then.moves_from(status)) else {
        return Ok(());
    };
    let state = state_of(cx);
    let now = cx.now();
    if state.exhausted || !state.retry_at.is_some_and(|retry_at| retry_at <= now) {
        return Ok(());
    }
    // A row the run created has no status before it.
    let before = match (cx.key(), cx.version()) {
        (Some(key), Some(_)) => cx
            .read_row(cx.type_name(), key)?
            .and_then(|row| row.owned(&cx.config().status, workflow::STATUS))
            .and_then(Value::as_str),
        _ => None,
    };
    if before == Some(status.as_str()) {
        return Ok(());
    }
    cx.arm(RETRY, now, Value::Null)
}

fn params_refusal(view: &View<'_, RetriesConfig>, path: &str, message: String) -> Stop {
    Refusal::operation_params(
        view.behavior(),
        view.use_id(),
        RECORD_ATTEMPT,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

// admit holds an attempt to recordAttempt's refusals, in the engine's
// order: the config's permission, an exhausted row, a result with no
// resultField to keep it in, and a failure class the config lacks.
fn admit(cx: &Cx<'_, RetriesConfig>, state: &State, params: &Value) -> Hook<()> {
    let config = cx.config();
    if let Some(permission) = &config.permission {
        if !cx.can(permission)? {
            return Err(cx.forbidden(format!(
                "{} may not record an attempt of {}: it needs permission {permission}",
                cx.caller().subject,
                target(cx)
            )));
        }
    }
    if state.exhausted {
        let stuck = if state.stuck {
            ", stuck on one failure"
        } else {
            ""
        };
        return Err(cx.veto(said(
            format!("its retries are exhausted{stuck}"),
            "exhausted",
            None,
        )));
    }
    if params.get("result").is_some() {
        return Err(params_refusal(
            cx,
            "/result",
            "the config names no resultField to keep a result in".to_owned(),
        ));
    }
    if let Some(failure) = params.get("failure").and_then(Value::as_str) {
        if config.class(failure).is_none() {
            let names: Vec<&str> = config
                .classes
                .iter()
                .map(|(name, _)| name.as_str())
                .collect();
            return Err(params_refusal(
                cx,
                "/failure",
                format!(
                    "{failure} is not a failure class of {} (its classes: {})",
                    cx.type_name(),
                    names.join(", ")
                ),
            ));
        }
    }
    Ok(())
}

/// What a failure does to the counts: the engine's three rules.
struct Counted {
    counted: Vec<(String, i64)>,
    total: i64,
    exhausted: bool,
}

// count applies a failure to the counts, as the engine's rules have it: a
// terminal class counts and exhausts; a class with no room left, its own
// cap or the total's, counts nothing and exhausts; any other counts, and
// exhausts at the total's cap, or at its class's when no other has room.
fn count(config: &RetriesConfig, caps: &Caps<'_>, state: &State, failure: Option<&str>) -> Counted {
    let mut counted = state.counted.clone();
    let mut total = state.total;
    let Some(failure) = failure else {
        return Counted {
            counted,
            total,
            exhausted: false,
        };
    };
    let bump = |counted: &mut Vec<(String, i64)>| {
        if let Some(entry) = counted.iter_mut().find(|(name, _)| name == failure) {
            entry.1 += 1;
        }
    };
    let exhausted = if config.class(failure) == Some(&RetryClass::Terminal) {
        if total < caps.total {
            total += 1;
            bump(&mut counted);
        }
        true
    } else if total >= caps.total || counted_of(&counted, failure) >= caps.of(failure) {
        true
    } else {
        total += 1;
        bump(&mut counted);
        total >= caps.total
            || (counted_of(&counted, failure) >= caps.of(failure)
                && !room(config, caps, &counted, failure))
    };
    Counted {
        counted,
        total,
        exhausted,
    }
}

// attempt_row is an attempt's row of the attempts sub-table: when, its
// failure, score, whether it was kept, its signature, predicates and
// detail as given, who recorded it, and the key it counted under.
fn attempt_row(
    cx: &Cx<'_, RetriesConfig>,
    params: &Value,
    key: Option<&str>,
    kept: bool,
) -> Map<String, Value> {
    let mut values = Map::new();
    values.insert("at".into(), Value::from(cx.now()));
    if let Some(failure) = params.get("failure").filter(|failure| failure.is_string()) {
        values.insert("failure".into(), failure.clone());
    }
    if let Some(score) = params.get("score").filter(|score| score.is_number()) {
        values.insert("score".into(), score.clone());
    }
    values.insert("kept".into(), Value::Bool(kept));
    for field in ["signature", "predicates", "detail"] {
        if let Some(value) = params.get(field).filter(|value| !value.is_null()) {
            values.insert(field.into(), value.clone());
        }
    }
    values.insert("subject".into(), Value::from(cx.caller().subject.as_str()));
    if let Some(key) = key {
        values.insert("keyedBy".into(), Value::from(key));
    }
    values
}

fn record_attempt(cx: &Cx<'_, RetriesConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let state = state_of(cx);
    admit(cx, &state, params)?;
    let failure = params.get("failure").and_then(Value::as_str);
    let score_param = params.get("score").filter(|score| score.is_number());
    let score = score_param.and_then(Value::as_f64);
    let keyed = config.key_field.is_some();
    let history = History::of(cx)?;
    let Counted {
        counted,
        total,
        mut exhausted,
    } = count(config, &caps_of(cx), &state, failure);

    let predicates = params.get("predicates").and_then(Value::as_object);
    let kept = failure.is_none() || keeps(config, &state, &history, score, predicates);
    let stuck = match (
        failure,
        kept,
        params.get("signature").and_then(Value::as_str),
    ) {
        (Some(_), false, Some(signature)) => {
            let streak = history.streak(keyed, signature);
            config.stuck_after.is_some_and(|after| streak >= after)
        }
        _ => false,
    };
    exhausted |= stuck;

    let now = cx.now();
    // giveUpAfterMs: a failure that long after the first failure since the
    // last success exhausts the row once it has counted (D104).
    let failing_since = match (failure, config.give_up_after_ms) {
        (Some(_), Some(after)) => {
            let since = state.failing_since.unwrap_or(now);
            exhausted |= state.failing_since.is_some() && now - since >= after;
            Some(since)
        }
        _ => None,
    };
    let retry_at = match failure.and_then(|failure| config.class(failure)) {
        Some(class) if !exhausted => class.backoff().map(|backoff| {
            let n = history.failures_since_success(keyed, failure.unwrap_or_default());
            now + backoff.wait(n)
        }),
        _ => None,
    };
    let best_score = match (kept, score) {
        (true, Some(score)) => Some(score),
        _ => state.best_score,
    };
    cx.set("total", total)?;
    cx.set("classAttempts", counts_json(&counted))?;
    cx.set("exhausted", exhausted)?;
    cx.set("stuck", stuck)?;
    cx.set("bestScore", best_score.map_or(Value::Null, score_value))?;
    cx.set("retryAt", retry_at.map_or(Value::Null, Value::from))?;
    if config.give_up_after_ms.is_some() {
        cx.set(
            "failingSince",
            failing_since.map_or(Value::Null, Value::from),
        )?;
    }
    if keyed {
        cx.set(
            "keyedBy",
            history.key.clone().map_or(Value::Null, Value::from),
        )?;
    }
    let number = history
        .rows
        .iter()
        .filter_map(|row| row.key.parse::<i64>().ok())
        .max()
        .unwrap_or(0)
        + 1;
    let values = attempt_row(cx, params, history.key.as_deref(), kept);
    cx.insert_row(ATTEMPTS, &format!("{number:0NUMBER_WIDTH$}"), values)?;
    if config.then.is_some() {
        match retry_at {
            Some(at) => cx.arm(RETRY, at, Value::Null)?,
            None => cx.clear(RETRY)?,
        }
    }
    if exhausted {
        let rule = StatusMove {
            transition: config.exhausted_state.clone(),
            from: config.from.clone(),
        };
        let status = cx
            .get_of(&config.status, workflow::STATUS)
            .and_then(|status| status.as_str().map(str::to_owned));
        moves::apply(cx, &config.status, Some(&rule), status.as_deref())?;
    }
    let hint = match failure.and_then(|failure| config.class(failure)) {
        Some(RetryClass::Capped {
            hint: Some(hint), ..
        }) => Value::from(hint.as_str()),
        _ => Value::Null,
    };
    let mut out = json!({
        "failure": failure,
        "score": score_param.cloned().unwrap_or(Value::Null),
        "kept": kept,
        "total": total,
        "classAttempts": counts_json(&counted),
        "exhausted": exhausted,
        "stuck": stuck,
        "hint": hint,
    });
    if let Some(retry_at) = retry_at {
        out["retryAt"] = Value::from(retry_at);
    }
    Ok(out)
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
    for key in ["classes", "totalAttempts", "exhaustedState"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    for key in [
        "exhaustedState",
        "keyField",
        "limitsField",
        "limitsPermission",
        "permission",
        "resultField",
        "status",
    ] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    for (key, minimum) in [
        ("giveUpAfterMs", 1000),
        ("stuckAfter", 1),
        ("totalAttempts", 1),
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
    for key in ["from", "retryInto"] {
        if let Some(states) = config.get(key) {
            lease::state_list_issues(&schema::member("", key), states, &mut issues);
        }
    }
    if let Some(then) = config.get("then") {
        lease::move_issues("/then", then, &mut issues);
    }
    if let Some(window) = config.get("window") {
        window_issues(window, &mut issues);
    }
    if let Some(keep_best) = config.get("keepBest") {
        keep_best_issues(keep_best, &mut issues);
    }
    if let Some(classes) = config.get("classes") {
        classes_issues(classes, &mut issues);
    }
    issues
}

fn classes_issues(classes: &Value, issues: &mut Issues) {
    let at = "/classes";
    let Some(named) = classes.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (name, class) in named {
        // A key that is no class's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let mut broken = false;
        if name == "totalAttempts" {
            issues.push(at, "not", "must NOT be valid");
            broken = true;
        }
        if !is_class_name(name) {
            issues.push(
                at,
                "pattern",
                format!("must match pattern \"{CLASS_NAME}\""),
            );
            broken = true;
        }
        if broken {
            issues.push(at, "propertyNames", "property name must be valid");
        }
        class_issues(&schema::member(at, name), class, issues);
    }
}

// class_issues lists what a class breaks of its oneOf, as ajv reports it:
// nothing when exactly one branch holds; else every branch's issues and the
// oneOf's own.
fn class_issues(at: &str, class: &Value, issues: &mut Issues) {
    let mut capped = Issues::default();
    capped_issues(at, class, &mut capped);
    let terminal = class == &Value::String("terminal".to_owned());
    if capped.is_empty() != terminal {
        return;
    }
    issues.extend(capped);
    if !terminal {
        issues.push(at, "const", "must be equal to constant");
    }
    issues.push(at, "oneOf", "must match exactly one schema in oneOf");
}

fn capped_issues(at: &str, class: &Value, issues: &mut Issues) {
    let Some(members) = class.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &CLASS_KEYS, issues);
    match members.get("attempts") {
        None => issues.push(at, "required", "must have required property 'attempts'"),
        Some(attempts) => {
            schema::integer_issues(
                &schema::member(at, "attempts"),
                attempts,
                Some(1),
                None,
                issues,
            );
        }
    }
    match members.get("hint") {
        Some(Value::String(hint)) if hint.is_empty() => issues.push(
            schema::member(at, "hint"),
            "minLength",
            "must NOT have fewer than 1 characters",
        ),
        Some(Value::String(hint)) if schema::length(hint) > MAX_HINT => issues.push(
            schema::member(at, "hint"),
            "maxLength",
            format!("must NOT have more than {MAX_HINT} characters"),
        ),
        Some(Value::String(_)) | None => {}
        Some(_) => issues.push(schema::member(at, "hint"), "type", "must be string"),
    }
    if let Some(backoff) = members.get("backoff") {
        let at = schema::member(at, "backoff");
        let Some(members) = backoff.as_object() else {
            issues.push(&at, "type", "must be object");
            return;
        };
        unknown_keys(&at, members, &BACKOFF_KEYS, issues);
        for key in ["firstMs", "maxMs"] {
            match members.get(key) {
                None => issues.push(
                    &at,
                    "required",
                    format!("must have required property '{key}'"),
                ),
                Some(ms) => {
                    schema::integer_issues(&schema::member(&at, key), ms, Some(1), None, issues);
                }
            }
        }
        if let Some(factor) = members.get("factor") {
            number_issues(&schema::member(&at, "factor"), factor, 1.0, issues);
        }
    }
}

fn window_issues(window: &Value, issues: &mut Issues) {
    let at = "/window";
    let Some(members) = window.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &WINDOW_KEYS, issues);
    for (key, minimum) in [("attempts", 1), ("ms", 1000)] {
        match members.get(key) {
            None => issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            ),
            Some(value) => {
                schema::integer_issues(&schema::member(at, key), value, Some(minimum), None, issues)
            }
        }
    }
}

fn keep_best_issues(keep_best: &Value, issues: &mut Issues) {
    let at = "/keepBest";
    let Some(members) = keep_best.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &KEEP_BEST_KEYS, issues);
    if let Some(delta) = members.get("minDelta") {
        number_issues("/keepBest/minDelta", delta, 0.0, issues);
    }
    if let Some(names) = members.get("neverRegress") {
        let at = "/keepBest/neverRegress";
        let Some(items) = names.as_array() else {
            issues.push(at, "type", "must be array");
            return;
        };
        for (i, item) in items.iter().enumerate() {
            let at = format!("{at}/{i}");
            match item {
                Value::String(name) if !is_class_name(name) => issues.push(
                    at,
                    "pattern",
                    format!("must match pattern \"{CLASS_NAME}\""),
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
}

// number_issues lists what a value breaks of `{ type: number, minimum }`.
fn number_issues(at: &str, value: &Value, minimum: f64, issues: &mut Issues) {
    match value.as_f64() {
        None => issues.push(at, "type", "must be number"),
        Some(n) if n < minimum => {
            issues.push(
                at,
                "minimum",
                format!("must be >= {}", score_value(minimum)),
            );
        }
        Some(_) => {}
    }
}

// is_class_name says whether a name matches CLASS_NAME.
fn is_class_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && name.chars().count() <= 64
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | '-'))
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

// params_issues lists what recordAttempt's parameters break of its closed
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
    if let Some(failure) = given.get("failure") {
        schema::text_issues("/failure", failure, &mut issues);
    }
    if given.get("score").is_some_and(|score| !score.is_number()) {
        issues.push("/score", "type", "must be number");
    }
    match given.get("signature") {
        Some(Value::String(signature)) if signature.is_empty() => issues.push(
            "/signature",
            "minLength",
            "must NOT have fewer than 1 characters",
        ),
        Some(Value::String(signature)) if schema::length(signature) > MAX_SIGNATURE => issues.push(
            "/signature",
            "maxLength",
            format!("must NOT have more than {MAX_SIGNATURE} characters"),
        ),
        Some(Value::String(_)) | None => {}
        Some(_) => issues.push("/signature", "type", "must be string"),
    }
    if given
        .get("detail")
        .is_some_and(|detail| !detail.is_object())
    {
        issues.push("/detail", "type", "must be object");
    }
    if let Some(predicates) = given.get("predicates") {
        match predicates.as_object() {
            None => issues.push("/predicates", "type", "must be object"),
            Some(members) => {
                for (name, held) in members {
                    if !held.is_boolean() {
                        issues.push(
                            schema::member("/predicates", name),
                            "type",
                            "must be boolean",
                        );
                    }
                }
            }
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_backoff_doubles_up_to_its_cap() {
        let backoff = RetryBackoff {
            first_ms: 60_000,
            max_ms: 100_000,
            factor: 2.0,
        };
        assert_eq!(backoff.wait(1), 60_000);
        assert_eq!(backoff.wait(2), 100_000);
        assert_eq!(backoff.wait(1000), 100_000);
        let tripled = RetryBackoff {
            first_ms: 10_000,
            max_ms: 40_000,
            factor: 3.0,
        };
        assert_eq!(tripled.wait(2), 30_000);
        assert_eq!(tripled.wait(3), 40_000);
        let slow = RetryBackoff {
            first_ms: 1000,
            max_ms: 10_000,
            factor: 1.5,
        };
        assert_eq!(slow.wait(2), 1500);
        assert_eq!(slow.wait(3), 2250);
    }

    #[test]
    fn a_key_is_its_values_text() {
        assert_eq!(key_text(Some(&json!("v1"))), Some("v1".into()));
        assert_eq!(key_text(Some(&json!(2))), Some("2".into()));
        assert_eq!(key_text(Some(&json!(2.0))), Some("2".into()));
        assert_eq!(key_text(Some(&json!(true))), Some("true".into()));
        assert_eq!(key_text(Some(&json!([1]))), Some("[1]".into()));
        assert_eq!(key_text(Some(&Value::Null)), None);
        assert_eq!(key_text(None), None);
    }

    #[test]
    fn a_class_name_matches_its_pattern() {
        for name in ["timeout", "a.b-c_d", "X"] {
            assert!(is_class_name(name), "{name}");
        }
        for name in ["", "1st", "a b"] {
            assert!(!is_class_name(name), "{name}");
        }
    }
}
