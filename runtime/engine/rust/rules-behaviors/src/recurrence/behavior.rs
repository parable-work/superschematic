//! Recurrence, a rule's dated occurrences on a row (D152): `implements
//! Recurrence<Filing, { anchors, timeZone?, horizonDays?, historyDays?,
//! adjust?, weekendDays?, holidays?, relative?, opensBefore?, remindBefore?,
//! overdueAfter?, permission?, managePermission? }>`. Like Schedules, only
//! the core implements it: no engine registers it, and its declaration is
//! the file `make behaviors` writes for `@superschematic/rules`, read in
//! place.
//!
//! It passes D103's test of a behavior. Its state is its own: the row's
//! recurrence (`recurrence`, an owned field), when its next occurrence
//! falls due and when it was last done (`nextDueAt`, `lastDoneAt`), and the
//! `occurrences` sub-table. Its rules say how a rule becomes occurrences
//! ([`super::expand()`]), when an occurrence opens, reminds and is missed,
//! that a rule change replans only what has not opened, that a missed
//! occurrence never blocks the next, and who may complete, skip or move
//! one. It recurs: a filing, a renewal, an inspection, a subscription's
//! payment, a digest.
//!
//! The state machine of an occurrence:
//!
//! ```text
//! upcoming --open--> open --overdue--> missed
//!     |                 \                 \
//!     +--skip--> skipped  +--complete--> done <--complete (late)
//! ```
//!
//! An occurrence opens at its `opensAt`: `opensBefore` its due, or, with no
//! `opensBefore`, the due of the occurrence before it (the first one when
//! it is planned). It is missed at its `overdueAt` (`overdueAfter` past its
//! due, its due when absent) unless it is done. It reminds at its
//! `remindAt` (`remindBefore` its due) while it is upcoming or open,
//! recording `remindedAt`, and falls due at its `dueAt` while it is upcoming
//! or open, recording `fellDueAt` (#601). Each is a deadline of the row on
//! the table's deadline queue, by name: `open` at the earliest upcoming
//! occurrence's `opensAt`, `remind` at the earliest `remindAt` not yet
//! reminded, `due` at the earliest `dueAt` of an upcoming or open one not
//! yet fallen due, `overdue` at the earliest open occurrence's
//! `overdueAt`, and `plan`,
//! when no occurrence is upcoming, at the time the horizon reaches the
//! next. Every fire runs the same plan, which catches up every occurrence
//! due by its time, so a worker that was down fires once. The payload is
//! the occurrence's key; the sub-table is the truth the fire reads.
//!
//! Planning: the occurrences the row's recurrence dates from the later of
//! when it was set and the latest occurrence that has opened, up to
//! `horizonDays` ahead, are kept: a new one is inserted upcoming, an
//! upcoming one is dated again (a holiday added moves it), and an upcoming
//! one the recurrence no longer dates is deleted. Open and finished ones
//! are never touched, so a rule change (`setRecurrence`) leaves what has
//! opened as it is. An afterDone recurrence keeps one occurrence ahead,
//! dated from the later of its last completion and its latest missed or
//! skipped occurrence's due, so a missed one never blocks the next. A
//! relative recurrence reads its date from the row the config's relation
//! names at each plan. A holiday is a row of the config's holiday table in
//! the calendar the row's field names, read at each plan. Finished
//! occurrences go `historyDays` after their due.
//!
//! The plan runs on every write through the core that may change it
//! (`setRecurrence`, `complete`, `skip`, `moveOne`, an update of the
//! holiday calendar's field or the relation), at every fire, and on a
//! write of those fields its change queue carries, a raw one among them.
//! A write of the row a relative recurrence reads its date from is none
//! of these: the row hears no write of another table's. `setRecurrence`
//! with the rule the row already holds is what follows it: for a relative
//! recurrence it plans again from the date as it is now, keeping the
//! recurrence as it was set, and answers `unchanged` only when the plan
//! dates what the row already holds.
//!
//! Each run that moves an occurrence on raises the event of the move
//! (D153): `opened`, `reminded`, `due` and `missed`, recorded on the
//! occurrence (`changedAt`, `remindedAt`, `fellDueAt`), so a later run
//! raises none of them again.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::OnceLock;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Heard, Hook, Refusal,
    SchemaIssue, Stop, SubRow, Veto, View,
};

use super::expand::{expand, span, wall, wall_text, weekdays, zone_of, ExpandInput, Occurrence};
use crate::ajv;
use crate::declared;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Recurrence";

/// Recurrence's declaration, the file `make behaviors` writes for the core
/// alone (no engine implements it), read in place.
pub const DECLARATION: &str = include_str!("../../declarations/Recurrence.behavior.json");

/// The sub-table of a row's occurrences, keyed by nominal wall date-time.
pub const OCCURRENCES: &str = "occurrences";

/// The owned fields.
pub const RECURRENCE: &str = "recurrence";
pub const NEXT_DUE_AT: &str = "nextDueAt";
pub const LAST_DONE_AT: &str = "lastDoneAt";

/// The operations, by their declared names.
pub const SET_RECURRENCE: &str = "setRecurrence";
pub const COMPLETE: &str = "complete";
pub const SKIP: &str = "skip";
pub const MOVE_ONE: &str = "moveOne";
pub const LIST_OCCURRENCES: &str = "listOccurrences";

/// The deadlines it arms, by name.
pub const OPEN: &str = "open";
pub const REMIND: &str = "remind";
pub const DUE: &str = "due";
pub const OVERDUE: &str = "overdue";
pub const PLAN: &str = "plan";

/// The states of an occurrence.
pub const UPCOMING: &str = "upcoming";
pub const OPENED: &str = "open";
pub const DONE: &str = "done";
pub const MISSED: &str = "missed";
pub const SKIPPED: &str = "skipped";

// The events a run emits as it moves an occurrence on (D153).
pub const EVENT_OPENED: &str = "opened";
pub const EVENT_REMINDED: &str = "reminded";
pub const EVENT_DUE: &str = "due";
pub const EVENT_MISSED: &str = "missed";

const DAY_MS: i64 = 86_400_000;

/// How many occurrences a plan keeps ahead at most, and a list answers.
const MAX_PLANNED: usize = 1000;
const MAX_LISTED: usize = 500;

/// How many pages of holidays a plan reads at most.
const MAX_HOLIDAY_PAGES: usize = 20;

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// The holiday table a config names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Holidays {
    pub table: String,
    pub calendar_field: String,
    pub date_field: String,
    pub field: String,
}

/// Where the relative anchor's date is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Relative {
    /// The relation, as the config names it (`contract`).
    pub via: String,
    /// The own field that holds the related row's key: the relation's
    /// field in the type's spec (a table's `contractId`), or `via` itself
    /// when the spec gives no fields.
    pub key_field: String,
    pub type_name: String,
    pub field: String,
}

/// Recurrence's config, parsed: the defaults filled in.
#[derive(Clone, Debug)]
pub struct RecurrenceConfig {
    pub anchors: Vec<String>,
    pub time_zone: String,
    pub horizon_days: i64,
    pub history_days: i64,
    pub adjust: String,
    pub weekend_days: Option<Vec<String>>,
    pub holidays: Option<Holidays>,
    pub relative: Option<Relative>,
    pub opens_before: Option<String>,
    pub remind_before: Option<String>,
    pub overdue_after: Option<String>,
    pub permission: Option<String>,
    pub manage_permission: Option<String>,
}

impl RecurrenceConfig {
    /// Parses and checks a config: as the declaration's configSchema holds
    /// it, then by Recurrence's rules: a known zone, spans of ISO 8601 that
    /// lie ahead, a relative config with the relative anchor and none
    /// without, and the fields it names among the type's.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<RecurrenceConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let text = |value: Option<&Value>| value.and_then(Value::as_str).map(str::to_owned);
        let anchors: Vec<String> = raw["anchors"]
            .as_array()
            .map(|all| {
                all.iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default();
        let time_zone = text(raw.get("timeZone")).unwrap_or_else(|| "UTC".to_owned());
        zone_of(Some(&time_zone)).map_err(|_| {
            format!("timeZone \"{time_zone}\" is not a time zone of the IANA database the core bundles: an IANA name, such as Europe/London")
        })?;
        for key in ["opensBefore", "remindBefore", "overdueAfter"] {
            if let Some(value) = text(raw.get(key)) {
                let parsed = span(&value, key).map_err(ConfigError::from)?;
                if parsed.is_negative() {
                    return Err(
                        format!("{key} \"{value}\" is negative: a lead time is a span").into(),
                    );
                }
            }
        }
        if let Some(days) = raw.get("weekendDays").and_then(Value::as_array) {
            let names: Vec<String> = days
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect();
            weekdays(&names).map_err(ConfigError::from)?;
        }
        let fields = target.fields();
        let has_field =
            |name: &str| fields.is_empty() || fields.iter().any(|field| field.name == name);
        let holidays = raw.get("holidays").map(|h| Holidays {
            table: text(h.get("table")).unwrap_or_default(),
            calendar_field: text(h.get("calendarField")).unwrap_or_default(),
            date_field: text(h.get("dateField")).unwrap_or_default(),
            field: text(h.get("field")).unwrap_or_default(),
        });
        if let Some(h) = &holidays {
            if !has_field(&h.field) {
                return Err(format!(
                    "holidays.field \"{}\" is not a field of {}",
                    h.field,
                    target.type_name()
                )
                .into());
            }
        }
        let mut relative = raw.get("relative").map(|r| {
            let via = text(r.get("via")).unwrap_or_default();
            Relative {
                key_field: via.clone(),
                via,
                type_name: text(r.get("type")).unwrap_or_default(),
                field: text(r.get("field")).unwrap_or_default(),
            }
        });
        let takes_relative = anchors.iter().any(|anchor| anchor == "relative");
        match &mut relative {
            None if takes_relative => {
                return Err(
                    "the relative anchor needs relative: the relation and the date field it reads"
                        .to_owned()
                        .into(),
                )
            }
            Some(_) if !takes_relative => {
                return Err("relative is given and anchors does not list relative"
                    .to_owned()
                    .into())
            }
            Some(r) if !fields.is_empty() => {
                // A table's spec names a to-one relation's field by the
                // column's field (contractId) and the relation by its own
                // name (contract).
                let Some(field) = fields.iter().find(|field| {
                    field
                        .relation
                        .as_ref()
                        .is_some_and(|relation| relation.name == r.via)
                        || field.name == r.via
                }) else {
                    return Err(format!(
                        "relative.via \"{}\" is not a field of {}",
                        r.via,
                        target.type_name()
                    )
                    .into());
                };
                if let Some(relation) = &field.relation {
                    if relation.type_name != r.type_name {
                        return Err(format!(
                            "relative.via \"{}\" points at {}, and relative.type is {}",
                            r.via, relation.type_name, r.type_name
                        )
                        .into());
                    }
                }
                r.key_field = field.name.clone();
            }
            _ => {}
        }
        let number =
            |key: &str, default: i64| raw.get(key).and_then(Value::as_i64).unwrap_or(default);
        Ok(RecurrenceConfig {
            anchors,
            time_zone,
            horizon_days: number("horizonDays", 400),
            history_days: number("historyDays", 1100),
            adjust: text(raw.get("adjust")).unwrap_or_else(|| "none".to_owned()),
            weekend_days: raw.get("weekendDays").and_then(Value::as_array).map(|all| {
                all.iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            }),
            holidays,
            relative,
            opens_before: text(raw.get("opensBefore")),
            remind_before: text(raw.get("remindBefore")),
            overdue_after: text(raw.get("overdueAfter")),
            permission: text(raw.get("permission")),
            manage_permission: text(raw.get("managePermission")),
        })
    }
}

// ---- the row's recurrence ----

/// The row's recurrence as its owned field holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Spec {
    anchor: String,
    rule: Option<String>,
    start: Option<String>,
    every: Option<String>,
    offset: Option<String>,
    time_zone: String,
    adjust: String,
    set_at: i64,
    revision: i64,
}

impl Spec {
    fn of(value: Option<Value>) -> Option<Spec> {
        let value = value?;
        let text = |key: &str| value.get(key).and_then(Value::as_str).map(str::to_owned);
        Some(Spec {
            anchor: text("anchor")?,
            rule: text("rule"),
            start: text("start"),
            every: text("every"),
            offset: text("offset"),
            time_zone: text("timeZone").unwrap_or_else(|| "UTC".to_owned()),
            adjust: text("adjust").unwrap_or_else(|| "none".to_owned()),
            set_at: value.get("setAt").and_then(whole).unwrap_or(0),
            revision: value.get("revision").and_then(whole).unwrap_or(0),
        })
    }

    // from_params is the recurrence a setRecurrence's parameters give,
    // the config's defaults filled in; its time and revision are the
    // caller's to set.
    fn from_params(config: &RecurrenceConfig, params: &Value) -> Spec {
        let text = |key: &str| params.get(key).and_then(Value::as_str).map(str::to_owned);
        Spec {
            anchor: text("anchor").unwrap_or_default(),
            rule: text("rule"),
            start: text("start"),
            every: text("every"),
            offset: text("offset"),
            time_zone: text("timeZone").unwrap_or_else(|| config.time_zone.clone()),
            adjust: text("adjust").unwrap_or_else(|| config.adjust.clone()),
            set_at: 0,
            revision: 0,
        }
    }

    // same_rule is whether two recurrences date alike: all but when they
    // were set and their revision.
    fn same_rule(&self, other: &Spec) -> bool {
        Spec {
            set_at: 0,
            revision: 0,
            ..self.clone()
        } == Spec {
            set_at: 0,
            revision: 0,
            ..other.clone()
        }
    }

    fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("anchor".into(), json!(self.anchor));
        for (key, value) in [
            ("rule", &self.rule),
            ("start", &self.start),
            ("every", &self.every),
            ("offset", &self.offset),
        ] {
            if let Some(value) = value {
                out.insert(key.into(), json!(value));
            }
        }
        out.insert("timeZone".into(), json!(self.time_zone));
        out.insert("adjust".into(), json!(self.adjust));
        out.insert("setAt".into(), json!(self.set_at));
        out.insert("revision".into(), json!(self.revision));
        Value::Object(out)
    }
}

// ---- an occurrence's row ----

/// An occurrence as its sub-table row holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Occ {
    key: String,
    due: String,
    due_at: i64,
    opens_at: i64,
    remind_at: Option<i64>,
    overdue_at: i64,
    state: String,
    revision: i64,
    shifted: Option<String>,
    moved: bool,
    changed_at: Option<i64>,
    reminded_at: Option<i64>,
    fell_due_at: Option<i64>,
    completed_at: Option<i64>,
    completed_by_table: Option<String>,
    completed_by_key: Option<String>,
    completed_by_principal: Option<String>,
    note: Option<String>,
}

impl Occ {
    fn of(row: &SubRow) -> Occ {
        let text = |key: &str| {
            row.values
                .get(key)
                .and_then(Value::as_str)
                .map(str::to_owned)
        };
        let number = |key: &str| row.values.get(key).and_then(whole);
        Occ {
            key: row.key.clone(),
            due: text("due").unwrap_or_default(),
            due_at: number("dueAt").unwrap_or_default(),
            opens_at: number("opensAt").unwrap_or_default(),
            remind_at: number("remindAt"),
            overdue_at: number("overdueAt").unwrap_or_default(),
            state: text("state").unwrap_or_else(|| UPCOMING.to_owned()),
            revision: number("revision").unwrap_or_default(),
            shifted: text("shifted"),
            moved: row
                .values
                .get("moved")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            changed_at: number("changedAt"),
            reminded_at: number("remindedAt"),
            fell_due_at: number("fellDueAt"),
            completed_at: number("completedAt"),
            completed_by_table: text("completedByTable"),
            completed_by_key: text("completedByKey"),
            completed_by_principal: text("completedByPrincipal"),
            note: text("note"),
        }
    }

    fn is_finished(&self) -> bool {
        matches!(self.state.as_str(), DONE | MISSED | SKIPPED)
    }

    // values are the row's values by declared field, an absent one null,
    // so an update clears it.
    fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        let opt = |v: &Option<i64>| v.map_or(Value::Null, Value::from);
        let opt_text = |v: &Option<String>| v.as_ref().map_or(Value::Null, |t| json!(t));
        out.insert("due".into(), json!(self.due));
        out.insert("dueAt".into(), json!(self.due_at));
        out.insert("opensAt".into(), json!(self.opens_at));
        out.insert("remindAt".into(), opt(&self.remind_at));
        out.insert("overdueAt".into(), json!(self.overdue_at));
        out.insert("state".into(), json!(self.state));
        out.insert("revision".into(), json!(self.revision));
        out.insert("shifted".into(), opt_text(&self.shifted));
        out.insert(
            "moved".into(),
            if self.moved { json!(true) } else { Value::Null },
        );
        out.insert("changedAt".into(), opt(&self.changed_at));
        out.insert("remindedAt".into(), opt(&self.reminded_at));
        out.insert("fellDueAt".into(), opt(&self.fell_due_at));
        out.insert("completedAt".into(), opt(&self.completed_at));
        out.insert(
            "completedByTable".into(),
            opt_text(&self.completed_by_table),
        );
        out.insert("completedByKey".into(), opt_text(&self.completed_by_key));
        out.insert(
            "completedByPrincipal".into(),
            opt_text(&self.completed_by_principal),
        );
        out.insert("note".into(), opt_text(&self.note));
        out
    }

    // view is the occurrence as an operation answers it.
    fn view(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("key".into(), json!(self.key));
        for (key, value) in self.values() {
            if value.is_null() || key == "completedByTable" || key == "completedByKey" {
                continue;
            }
            out.insert(key, value);
        }
        if let (Some(table), Some(key)) = (&self.completed_by_table, &self.completed_by_key) {
            out.insert("completedBy".into(), json!({ "table": table, "key": key }));
        }
        out
    }
}

// ---- the plan ----

/// A row's occurrences as a run has them, and what it read them as.
#[derive(Clone)]
struct Plan {
    occs: BTreeMap<String, Occ>,
    read: BTreeMap<String, Occ>,
    spec: Option<Spec>,
    last_done: Option<i64>,
    next_due: Option<i64>,
}

impl Plan {
    fn load(view: &View<'_, RecurrenceConfig>) -> Hook<Plan> {
        let occs: BTreeMap<String, Occ> = view
            .sub_rows(OCCURRENCES)?
            .iter()
            .map(Occ::of)
            .map(|occ| (occ.key.clone(), occ))
            .collect();
        let next_due = view.get(NEXT_DUE_AT).as_ref().and_then(whole);
        Ok(Plan {
            read: occs.clone(),
            occs,
            spec: Spec::of(view.get(RECURRENCE)),
            last_done: view.get(LAST_DONE_AT).as_ref().and_then(whole),
            next_due,
        })
    }

    // pending are the occurrences that are upcoming or open, by key: what
    // a plan dates, apart from the finished ones it only prunes.
    fn pending(&self) -> BTreeMap<&str, &Occ> {
        self.occs
            .iter()
            .filter(|(_, occ)| !occ.is_finished())
            .map(|(key, occ)| (key.as_str(), occ))
            .collect()
    }

    fn sorted(&self) -> Vec<&Occ> {
        let mut all: Vec<&Occ> = self.occs.values().collect();
        all.sort_by(|a, b| a.due_at.cmp(&b.due_at).then_with(|| a.key.cmp(&b.key)));
        all
    }

    // catch_up moves every occurrence on by the time: upcoming to open at
    // its opening, open to missed at its overdue time, and records a
    // reminder due and a due passed, the due before the miss it may fall
    // at. Answers whether any was missed.
    fn catch_up(&mut self, now: i64) -> bool {
        let mut missed = false;
        for occ in self.occs.values_mut() {
            if occ.state == UPCOMING && occ.opens_at <= now {
                occ.state = OPENED.to_owned();
                occ.changed_at = Some(now);
            }
            if matches!(occ.state.as_str(), UPCOMING | OPENED)
                && occ.fell_due_at.is_none()
                && occ.due_at <= now
            {
                occ.fell_due_at = Some(now);
            }
            if occ.state == OPENED && occ.overdue_at <= now {
                occ.state = MISSED.to_owned();
                occ.changed_at = Some(now);
                missed = true;
            }
            if matches!(occ.state.as_str(), UPCOMING | OPENED)
                && occ.reminded_at.is_none()
                && occ.remind_at.is_some_and(|at| at <= now)
            {
                occ.reminded_at = Some(now);
            }
        }
        missed
    }

    // base is when an afterDone recurrence's next occurrence is dated
    // from: the later of the last completion and the latest due of a
    // missed or skipped occurrence.
    fn base(&self) -> Option<i64> {
        let finished = self
            .occs
            .values()
            .filter(|occ| occ.state == MISSED || occ.state == SKIPPED)
            .map(|occ| occ.due_at)
            .max();
        match (self.last_done, finished) {
            (Some(a), Some(b)) => Some(a.max(b)),
            (a, b) => a.or(b),
        }
    }

    // desire dates the recurrence over [floor, now + horizon) and keeps
    // the occurrences: see the module's header. Answers when the next
    // occurrence past the horizon falls due, when none is upcoming.
    fn desire(&mut self, cx: &View<'_, RecurrenceConfig>, now: i64) -> Hook<Option<i64>> {
        let Some(spec) = self.spec.clone() else {
            return Ok(None);
        };
        let config = cx.config();
        let opened = self
            .occs
            .values()
            .filter(|occ| occ.state != UPCOMING)
            .map(|occ| occ.due_at)
            .max();
        let floor = spec.set_at.max(opened.map_or(i64::MIN, |due| due + 1));
        let to = now.saturating_add(config.horizon_days.saturating_mul(DAY_MS));
        let Some(mut input) = expand_input(cx, &spec, self.base())? else {
            self.drop_upcoming(&spec, &BTreeSet::new());
            return Ok(None);
        };
        input.from = Some(floor);
        input.to = Some(to);
        input.limit = Some(MAX_PLANNED);
        // A recurrence that dates nothing now (its relative date gone, a
        // rule whose COUNT ran out) plans nothing; a fire never refuses.
        let mut desired: Vec<Occurrence> =
            expand(&input).map(|e| e.occurrences).unwrap_or_default();
        if spec.anchor == "afterDone" {
            desired.truncate(1);
        }
        let keys: BTreeSet<String> = desired.iter().map(|o| o.key.clone()).collect();
        self.drop_upcoming(&spec, &keys);
        for occurrence in desired {
            match self.occs.get_mut(&occurrence.key) {
                None => {
                    self.occs
                        .insert(occurrence.key.clone(), new_occ(&occurrence, &spec, now));
                }
                Some(occ)
                    if occ.state == UPCOMING && (!occ.moved || occ.revision != spec.revision) =>
                {
                    occ.due = occurrence.due.clone();
                    occ.due_at = occurrence.due_at;
                    occ.remind_at = occurrence.remind_at;
                    occ.overdue_at = occurrence.overdue_at;
                    occ.shifted = occurrence.shifted.map(str::to_owned);
                    occ.revision = spec.revision;
                    occ.moved = false;
                    if let Some(at) = occurrence.opens_at {
                        occ.opens_at = at;
                    }
                }
                Some(_) => {}
            }
        }
        self.open_after_previous(cx, now);
        if self.occs.values().any(|occ| occ.state == UPCOMING) || spec.anchor == "afterDone" {
            return Ok(None);
        }
        // Nothing is upcoming within the horizon: when the next falls due
        // past it, the plan deadline comes back as the horizon reaches it.
        input.from = Some(to);
        input.to = None;
        input.limit = Some(1);
        Ok(expand(&input)
            .ok()
            .and_then(|e| e.occurrences.first().map(|o| o.due_at)))
    }

    // drop_upcoming deletes the upcoming occurrences the recurrence no
    // longer dates: every one a rule change left (a moved one among them),
    // and one a moved date or a holiday took off its key.
    fn drop_upcoming(&mut self, spec: &Spec, keys: &BTreeSet<String>) {
        self.occs.retain(|key, occ| {
            occ.state != UPCOMING
                || keys.contains(key)
                || (occ.moved && occ.revision == spec.revision)
        });
    }

    // open_after_previous dates the opening of each upcoming occurrence
    // with no opensBefore: the due of the occurrence before it, the first
    // when it is planned.
    fn open_after_previous(&mut self, cx: &View<'_, RecurrenceConfig>, now: i64) {
        if cx.config().opens_before.is_some() {
            return;
        }
        let order: Vec<(String, i64)> = self
            .sorted()
            .iter()
            .map(|occ| (occ.key.clone(), occ.due_at))
            .collect();
        let mut previous: Option<i64> = None;
        for (key, due_at) in order {
            if let Some(occ) = self.occs.get_mut(&key) {
                if occ.state == UPCOMING && !occ.moved {
                    occ.opens_at = match previous {
                        Some(at) => at.min(due_at),
                        None if self.read.contains_key(&key) => occ.opens_at.min(due_at),
                        None => now.min(due_at),
                    };
                }
            }
            previous = Some(due_at);
        }
    }

    // prune deletes the finished occurrences historyDays past their due.
    fn prune(&mut self, config: &RecurrenceConfig, now: i64) {
        let before = now.saturating_sub(config.history_days.saturating_mul(DAY_MS));
        self.occs
            .retain(|_, occ| !(occ.is_finished() && occ.due_at < before));
    }

    // write writes what changed of the sub-table and the owned fields,
    // and arms the deadlines.
    fn write(&self, cx: &Cx<'_, RecurrenceConfig>, plan_at: Option<i64>) -> Hook<()> {
        for (key, occ) in &self.occs {
            match self.read.get(key) {
                None => {
                    let values: Map<String, Value> = occ
                        .values()
                        .into_iter()
                        .filter(|(_, v)| !v.is_null())
                        .collect();
                    cx.insert_row(OCCURRENCES, key, values)?;
                }
                Some(before) if before != occ => cx.update_row(OCCURRENCES, key, occ.values())?,
                Some(_) => {}
            }
        }
        for key in self.read.keys() {
            if !self.occs.contains_key(key) {
                cx.delete_row(OCCURRENCES, key)?;
            }
        }
        let next = self
            .occs
            .values()
            .filter(|occ| occ.state == UPCOMING || occ.state == OPENED)
            .map(|occ| occ.due_at)
            .min();
        if next != self.next_due {
            cx.set(NEXT_DUE_AT, next.map_or(Value::Null, Value::from))?;
        }
        let earliest = |pick: &dyn Fn(&Occ) -> Option<i64>| -> Option<(i64, String)> {
            self.occs
                .values()
                .filter_map(|occ| pick(occ).map(|at| (at, occ.key.clone())))
                .min()
        };
        let arms = [
            (
                OPEN,
                earliest(&|occ| (occ.state == UPCOMING).then_some(occ.opens_at)),
            ),
            (
                REMIND,
                earliest(&|occ| {
                    (matches!(occ.state.as_str(), UPCOMING | OPENED) && occ.reminded_at.is_none())
                        .then_some(occ.remind_at)
                        .flatten()
                }),
            ),
            (
                DUE,
                earliest(&|occ| {
                    (matches!(occ.state.as_str(), UPCOMING | OPENED) && occ.fell_due_at.is_none())
                        .then_some(occ.due_at)
                }),
            ),
            (
                OVERDUE,
                earliest(&|occ| (occ.state == OPENED).then_some(occ.overdue_at)),
            ),
            (PLAN, plan_at.map(|at| (at, String::new()))),
        ];
        for (name, at) in arms {
            match at {
                Some((at, key)) => cx.arm(name, at, json!({ "key": key }))?,
                None => cx.clear(name)?,
            }
        }
        self.emit(cx)
    }

    // emit raises an event for each occurrence the run moved on (D153):
    // opened, reminded, due or missed, in key order, each once, since the
    // run that moves an occurrence is the one that writes the move.
    fn emit(&self, cx: &Cx<'_, RecurrenceConfig>) -> Hook<()> {
        for (key, occ) in &self.occs {
            let before = self.read.get(key);
            let was = |state: &str| before.is_some_and(|b| b.state == state);
            let payload = || json!({ "key": occ.key, "due": occ.due, "dueAt": occ.due_at, "overdueAt": occ.overdue_at });
            if occ.state == OPENED && !was(OPENED) {
                cx.emit(EVENT_OPENED, payload())?;
            }
            if occ.reminded_at.is_some() && before.is_none_or(|b| b.reminded_at.is_none()) {
                cx.emit(EVENT_REMINDED, payload())?;
            }
            if occ.fell_due_at.is_some() && before.is_none_or(|b| b.fell_due_at.is_none()) {
                cx.emit(EVENT_DUE, payload())?;
            }
            if occ.state == MISSED && !was(MISSED) {
                cx.emit(EVENT_MISSED, payload())?;
            }
        }
        Ok(())
    }
}

fn new_occ(occurrence: &Occurrence, spec: &Spec, now: i64) -> Occ {
    Occ {
        key: occurrence.key.clone(),
        due: occurrence.due.clone(),
        due_at: occurrence.due_at,
        opens_at: occurrence
            .opens_at
            .unwrap_or_else(|| now.min(occurrence.due_at)),
        remind_at: occurrence.remind_at,
        overdue_at: occurrence.overdue_at,
        state: UPCOMING.to_owned(),
        revision: spec.revision,
        shifted: occurrence.shifted.map(str::to_owned),
        moved: false,
        changed_at: Some(now),
        reminded_at: None,
        fell_due_at: None,
        completed_at: None,
        completed_by_table: None,
        completed_by_key: None,
        completed_by_principal: None,
        note: None,
    }
}

// expand_input is what the core expands for a row's recurrence: its own
// rule with the config's lead times, weekend and holidays, and, for a
// relative one, the date its relation's row holds; None when that row or
// its date is absent.
fn expand_input(
    view: &View<'_, RecurrenceConfig>,
    spec: &Spec,
    base: Option<i64>,
) -> Hook<Option<ExpandInput>> {
    let config = view.config();
    let mut input = ExpandInput {
        anchor: spec.anchor.clone(),
        time_zone: Some(spec.time_zone.clone()),
        rule: spec.rule.clone(),
        start: spec.start.clone(),
        every: spec.every.clone(),
        offset: spec.offset.clone(),
        adjust: Some(spec.adjust.clone()),
        weekend_days: config.weekend_days.clone(),
        holidays: holidays_of(view, config)?,
        opens_before: config.opens_before.clone(),
        remind_before: config.remind_before.clone(),
        overdue_after: config.overdue_after.clone(),
        ..ExpandInput::default()
    };
    if spec.anchor == "afterDone" {
        input.last_done = base;
    }
    if spec.anchor == "relative" {
        let Some(relative) = &config.relative else {
            return Ok(None);
        };
        let Some(key) = view
            .own_field(&relative.key_field)
            .and_then(|v| key_text(&v))
        else {
            return Ok(None);
        };
        let Some(row) = view.read_row(&relative.type_name, &key)? else {
            return Ok(None);
        };
        let date = match row.own.get(&relative.field) {
            Some(Value::String(text)) => Value::String(
                text.get(..19)
                    .unwrap_or(text)
                    .trim_end_matches('Z')
                    .to_owned(),
            ),
            Some(number @ Value::Number(_)) => number.clone(),
            _ => return Ok(None),
        };
        input.date = Some(date);
    }
    Ok(Some(input))
}

// holidays_of reads the dates of the holiday calendar the row's field
// names, page by page.
fn holidays_of(view: &View<'_, RecurrenceConfig>, config: &RecurrenceConfig) -> Hook<Vec<String>> {
    let Some(h) = &config.holidays else {
        return Ok(Vec::new());
    };
    let Some(calendar) = view.own_field(&h.field) else {
        return Ok(Vec::new());
    };
    let mut equal = Map::new();
    equal.insert(h.calendar_field.clone(), calendar);
    let mut dates = Vec::new();
    let mut after: Option<String> = None;
    for _ in 0..MAX_HOLIDAY_PAGES {
        let page = view.read_rows(&h.table, &equal, &[], after.as_deref())?;
        for row in &page.rows {
            match row.row.own.get(&h.date_field) {
                Some(Value::String(text)) if text.len() >= 10 => dates.push(text[..10].to_owned()),
                Some(value) => {
                    if let Some(ms) = whole(value) {
                        if let Some(text) = crate::values::date_time_text(ms) {
                            dates.push(text[..10].to_owned());
                        }
                    }
                }
                None => {}
            }
        }
        if !page.more {
            break;
        }
        after = page.rows.last().map(|row| row.key.clone());
    }
    Ok(dates)
}

/// Recurrence: a rule's dated occurrences on a row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Recurrence;

impl Behavior for Recurrence {
    type Config = RecurrenceConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<RecurrenceConfig, ConfigError> {
        RecurrenceConfig::parse(raw, target)
    }

    // Any change: the next plan reads the new config, and what has opened
    // stays as it is.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&RecurrenceConfig>,
        _after: Option<&RecurrenceConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &RecurrenceConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        ajv::params_issues(file(), operation, params)
    }

    // settle: the parameters a recurrence needs, the permission, the
    // occurrence and its state, and the answers that change nothing, before
    // any use's guard.
    fn settle(
        &self,
        view: &View<'_, RecurrenceConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        let (verb, permission) = match operation {
            SET_RECURRENCE => ("set the recurrence of", manage(view.config())),
            COMPLETE => (
                "complete an occurrence of",
                view.config().permission.as_deref(),
            ),
            SKIP => ("skip an occurrence of", manage(view.config())),
            MOVE_ONE => ("move an occurrence of", manage(view.config())),
            LIST_OCCURRENCES => (
                "list the occurrences of",
                view.config().permission.as_deref(),
            ),
            _ => return Ok(None),
        };
        let spec = if operation == SET_RECURRENCE {
            Some(spec_of_params(view, params)?)
        } else {
            None
        };
        if let Some(permission) = permission {
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
        let now = view.now();
        match operation {
            SET_RECURRENCE => {
                let spec = spec.unwrap_or_else(|| Spec::from_params(view.config(), params));
                let held = Spec::of(view.get(RECURRENCE));
                if held.as_ref().is_some_and(|held| held.same_rule(&spec)) {
                    let mut plan = Plan::load(view)?;
                    plan.catch_up(now);
                    // A relative recurrence's date is another row's, and no
                    // write of this row says it moved: the rule it holds
                    // plans again when a plan would now date its upcoming
                    // and open occurrences otherwise (set_recurrence).
                    if spec.anchor == "relative" {
                        let mut again = plan.clone();
                        settle_plan(view, &mut again)?;
                        if again.pending() != plan.pending() {
                            return Ok(None);
                        }
                    }
                    let kept = open_keys(&plan);
                    let mut result = set_result(&plan, held.as_ref().unwrap_or(&spec), &kept);
                    result.insert("unchanged".into(), json!(true));
                    return Ok(Some(Value::Object(result)));
                }
                Ok(None)
            }
            LIST_OCCURRENCES => Ok(None),
            _ => {
                let mut plan = Plan::load(view)?;
                plan.catch_up(now);
                let occ = target(view, &plan, operation, params)?;
                match (operation, occ.state.as_str()) {
                    (COMPLETE | SKIP, DONE | SKIPPED) => Ok(Some(flagged(occ.view()))),
                    (COMPLETE, UPCOMING) => {
                        let mut details = Map::new();
                        details.insert("key".into(), json!(occ.key));
                        details.insert("opensAt".into(), json!(occ.opens_at));
                        Err(view.veto(
                            Veto::new(format!(
                                "its occurrence {} has not opened: it opens at {}",
                                occ.key,
                                instant(occ.opens_at)
                            ))
                            .with_code("not_open")
                            .with_details(details),
                        ))
                    }
                    (MOVE_ONE, DONE | MISSED | SKIPPED) => {
                        let mut details = Map::new();
                        details.insert("key".into(), json!(occ.key));
                        details.insert("state".into(), json!(occ.state));
                        Err(view.veto(
                            Veto::new(format!("its occurrence {} is {}, and only one that is upcoming or open moves", occ.key, occ.state))
                                .with_code("occurrence_finished")
                                .with_details(details),
                        ))
                    }
                    (MOVE_ONE, _) => {
                        let due = text_of(params, "due");
                        let spec = plan
                            .spec
                            .clone()
                            .unwrap_or_else(|| Spec::from_params(view.config(), &json!({})));
                        moved_dates(view, &spec, due).map_err(|reason| {
                            bad_param(view, operation, vec![SchemaIssue::new("/due", reason)])
                        })?;
                        Ok(None)
                    }
                    _ => Ok(None),
                }
            }
        }
    }

    fn operation(&self, cx: &Cx<'_, RecurrenceConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            SET_RECURRENCE => set_recurrence(cx, params),
            COMPLETE => finish(cx, name, params),
            SKIP => finish(cx, name, params),
            MOVE_ONE => move_one(cx, params),
            LIST_OCCURRENCES => list_occurrences(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // Every deadline runs the plan: it catches up whatever fell due by its
    // time, and arms each name at its next.
    fn deadline(&self, cx: &Cx<'_, RecurrenceConfig>, _name: &str, _payload: &Value) -> Hook<()> {
        replan(cx)
    }

    // An update of the field that names the holiday calendar or of the
    // relation the relative anchor reads plans again.
    fn after_change(&self, cx: &Cx<'_, RecurrenceConfig>, change: &Change<'_>) -> Hook<()> {
        let Change::Update { patch, .. } = change else {
            return Ok(());
        };
        if watched(cx.config())
            .iter()
            .any(|field| patch.contains_key(*field))
        {
            return replan(cx);
        }
        Ok(())
    }

    // So does a write of them the change queue carries, a raw one among
    // them.
    fn heard(&self, cx: &Cx<'_, RecurrenceConfig>, heard: &Heard<'_>) -> Hook<()> {
        let Heard::Own { change } = heard else {
            return Ok(());
        };
        let (Some(before), Some(after)) = (&change.before, &change.after) else {
            return Ok(());
        };
        if watched(cx.config())
            .iter()
            .any(|field| before.own.get(*field) != after.own.get(*field))
        {
            return replan(cx);
        }
        Ok(())
    }
}

// watched are the own fields whose change plans again.
fn watched(config: &RecurrenceConfig) -> Vec<&str> {
    let mut fields = Vec::new();
    if let Some(h) = &config.holidays {
        fields.push(h.field.as_str());
    }
    if let Some(r) = &config.relative {
        fields.push(r.key_field.as_str());
    }
    fields
}

fn manage(config: &RecurrenceConfig) -> Option<&str> {
    config
        .manage_permission
        .as_deref()
        .or(config.permission.as_deref())
}

// replan loads, catches up, dates and writes: what a fire and a write of
// a watched field do.
fn replan(cx: &Cx<'_, RecurrenceConfig>) -> Hook<()> {
    let mut plan = Plan::load(cx)?;
    if plan.spec.is_none() && plan.occs.is_empty() {
        return Ok(());
    }
    let plan_at = settle_plan(cx, &mut plan)?;
    plan.write(cx, plan_at)
}

// settle_plan runs the plan to rest: catch up, date, and again while an
// afterDone recurrence's occurrence was missed, which dates the next. It
// reads and writes nothing of the row, so settle runs it on a view to ask
// what a plan would date.
fn settle_plan(cx: &View<'_, RecurrenceConfig>, plan: &mut Plan) -> Hook<Option<i64>> {
    let now = cx.now();
    plan.catch_up(now);
    let mut plan_at = plan.desire(cx, now)?;
    for _ in 0..64 {
        if !plan.catch_up(now) {
            break;
        }
        plan_at = plan.desire(cx, now)?;
    }
    plan.prune(cx.config(), now);
    let horizon = cx.config().horizon_days.saturating_mul(DAY_MS);
    Ok(plan_at.map(|next| next.saturating_sub(horizon).max(now + 60_000)))
}

// spec_of_params checks a setRecurrence's parameters: its anchor among the
// config's, what that anchor needs, and a dry expansion of it.
fn spec_of_params(view: &View<'_, RecurrenceConfig>, params: &Value) -> Hook<Spec> {
    let config = view.config();
    let spec = Spec::from_params(config, params);
    let issue = |path: &str, message: String| {
        bad_param(view, SET_RECURRENCE, vec![SchemaIssue::new(path, message)])
    };
    if !config.anchors.contains(&spec.anchor) {
        return Err(issue(
            "/anchor",
            format!(
                "\"{}\" is not an anchor {}'s Recurrence takes ({})",
                spec.anchor,
                view.type_name(),
                config.anchors.join(", ")
            ),
        ));
    }
    let needs: &[(&str, &Option<String>)] = match spec.anchor.as_str() {
        "calendar" => &[("rule", &spec.rule), ("start", &spec.start)],
        "afterDone" => &[("every", &spec.every), ("start", &spec.start)],
        _ => &[],
    };
    for (name, value) in needs {
        if value.is_none() {
            return Err(issue(
                &format!("/{name}"),
                format!("the {} anchor needs {name}", spec.anchor),
            ));
        }
    }
    for (name, value, anchors) in [
        ("rule", &spec.rule, &["calendar"][..]),
        ("every", &spec.every, &["afterDone", "relative"][..]),
        ("offset", &spec.offset, &["relative"][..]),
        ("start", &spec.start, &["calendar", "afterDone"][..]),
    ] {
        if value.is_some() && !anchors.contains(&spec.anchor.as_str()) {
            return Err(issue(
                &format!("/{name}"),
                format!("the {} anchor takes no {name}", spec.anchor),
            ));
        }
    }
    let input = ExpandInput {
        anchor: spec.anchor.clone(),
        time_zone: Some(spec.time_zone.clone()),
        rule: spec.rule.clone(),
        start: spec.start.clone(),
        every: spec.every.clone(),
        offset: spec.offset.clone(),
        adjust: Some(spec.adjust.clone()),
        date: (spec.anchor == "relative").then(|| json!("2000-01-01")),
        limit: Some(1),
        ..ExpandInput::default()
    };
    if let Err(reason) = expand(&input) {
        let path = ["rule", "start", "every", "offset", "timeZone"]
            .into_iter()
            .find(|name| reason.contains(name))
            .map_or(String::new(), |name| format!("/{name}"));
        return Err(issue(&path, reason));
    }
    Ok(spec)
}

// target is the occurrence an operation names, or, for complete with no
// key, the earliest open one, else the earliest missed one.
fn target<'p>(
    view: &View<'_, RecurrenceConfig>,
    plan: &'p Plan,
    operation: &str,
    params: &Value,
) -> Hook<&'p Occ> {
    if let Some(key) = params.get("key").and_then(Value::as_str) {
        return plan.occs.get(key).ok_or_else(|| {
            let mut details = Map::new();
            details.insert("key".into(), json!(key));
            view.veto(
                Veto::new(format!("it has no occurrence {key}"))
                    .with_code("no_occurrence")
                    .with_details(details),
            )
        });
    }
    let sorted = plan.sorted();
    let pick = |state: &str| sorted.iter().copied().find(|occ| occ.state == state);
    pick(OPENED).or_else(|| pick(MISSED)).ok_or_else(|| {
        let mut details = Map::new();
        if let Some(next) = sorted.iter().find(|occ| occ.state == UPCOMING) {
            details.insert("nextDueAt".into(), json!(next.due_at));
        }
        view.veto(
            Veto::new(match &plan.spec {
                None => format!("it has no recurrence to {operation}"),
                Some(_) => format!("it has no open or missed occurrence to {operation}"),
            })
            .with_code("no_recurrence")
            .with_details(details),
        )
    })
}

// set_result is setRecurrence's answer: kept are the occurrences that
// were open before the change.
fn set_result(plan: &Plan, spec: &Spec, kept: &[String]) -> Map<String, Value> {
    let planned: Vec<&str> = plan
        .sorted()
        .into_iter()
        .filter(|occ| occ.state == UPCOMING || occ.state == OPENED)
        .map(|occ| occ.key.as_str())
        .collect();
    let mut out = Map::new();
    out.insert("recurrence".into(), spec.to_json());
    out.insert("planned".into(), json!(planned));
    out.insert("kept".into(), json!(kept));
    let next = plan
        .occs
        .values()
        .filter(|o| o.state == UPCOMING || o.state == OPENED)
        .map(|o| o.due_at)
        .min();
    if let Some(next) = next {
        out.insert("nextDueAt".into(), json!(next));
    }
    out
}

// open_keys are the keys of the open occurrences, in due order.
fn open_keys(plan: &Plan) -> Vec<String> {
    plan.sorted()
        .into_iter()
        .filter(|occ| occ.state == OPENED)
        .map(|occ| occ.key.clone())
        .collect()
}

fn set_recurrence(cx: &Cx<'_, RecurrenceConfig>, params: &Value) -> Hook<Value> {
    let mut plan = Plan::load(cx)?;
    plan.catch_up(cx.now());
    let kept = open_keys(&plan);
    let mut spec = Spec::from_params(cx.config(), params);
    // The rule the row holds reaches here only for a relative recurrence
    // whose date has moved (settle answers every other as unchanged): the
    // plan runs again as a fire runs it, and the recurrence stays as it
    // was set, its time and its revision with it, so an occurrence moved
    // by hand (moveOne) keeps its date.
    if let Some(held) = plan.spec.clone().filter(|held| held.same_rule(&spec)) {
        let plan_at = settle_plan(cx, &mut plan)?;
        plan.write(cx, plan_at)?;
        return Ok(Value::Object(set_result(&plan, &held, &kept)));
    }
    spec.set_at = cx.now();
    spec.revision = plan.spec.as_ref().map_or(0, |held| held.revision) + 1;
    cx.set(RECURRENCE, spec.to_json())?;
    plan.spec = Some(spec.clone());
    let plan_at = settle_plan(cx, &mut plan)?;
    plan.write(cx, plan_at)?;
    Ok(Value::Object(set_result(&plan, &spec, &kept)))
}

// finish completes or skips an occurrence, then plans again: an afterDone
// recurrence dates its next from it.
fn finish(cx: &Cx<'_, RecurrenceConfig>, operation: &str, params: &Value) -> Hook<Value> {
    let now = cx.now();
    let mut plan = Plan::load(cx)?;
    plan.catch_up(now);
    let key = target(cx, &plan, operation, params)?.key.clone();
    let occ = plan
        .occs
        .get_mut(&key)
        .ok_or_else(|| cx.defect("the occurrence it settled is gone"))?;
    occ.state = if operation == COMPLETE { DONE } else { SKIPPED }.to_owned();
    occ.changed_at = Some(now);
    occ.completed_by_principal = Some(cx.caller().subject.clone());
    occ.note = params
        .get("note")
        .and_then(Value::as_str)
        .map(str::to_owned);
    if operation == COMPLETE {
        occ.completed_at = Some(now);
        if let Some(by) = params.get("completedBy") {
            occ.completed_by_table = by.get("table").and_then(Value::as_str).map(str::to_owned);
            occ.completed_by_key = by.get("key").and_then(Value::as_str).map(str::to_owned);
        }
        cx.set(LAST_DONE_AT, now)?;
        plan.last_done = Some(now);
    }
    let view = occ.view();
    let plan_at = settle_plan(cx, &mut plan)?;
    plan.write(cx, plan_at)?;
    Ok(Value::Object(view))
}

// moved_dates are an occurrence's dates at another wall date-time: the
// recurrence's lead times from it, with no adjustment.
fn moved_dates(
    view: &View<'_, RecurrenceConfig>,
    spec: &Spec,
    due: &str,
) -> Result<Occurrence, String> {
    let at = wall(due, "due")?;
    let config = view.config();
    let input = ExpandInput {
        anchor: "relative".into(),
        time_zone: Some(spec.time_zone.clone()),
        date: Some(json!(wall_text(at))),
        opens_before: config.opens_before.clone(),
        remind_before: config.remind_before.clone(),
        overdue_after: config.overdue_after.clone(),
        ..ExpandInput::default()
    };
    expand(&input)?
        .occurrences
        .into_iter()
        .next()
        .ok_or_else(|| format!("\"{due}\" dates no instant"))
}

fn move_one(cx: &Cx<'_, RecurrenceConfig>, params: &Value) -> Hook<Value> {
    let now = cx.now();
    let mut plan = Plan::load(cx)?;
    plan.catch_up(now);
    let key = target(cx, &plan, MOVE_ONE, params)?.key.clone();
    let spec = plan
        .spec
        .clone()
        .unwrap_or_else(|| Spec::from_params(cx.config(), &json!({})));
    let dates = moved_dates(cx, &spec, text_of(params, "due"))
        .map_err(|reason| bad_param(cx, MOVE_ONE, vec![SchemaIssue::new("/due", reason)]))?;
    let occ = plan
        .occs
        .get_mut(&key)
        .ok_or_else(|| cx.defect("the occurrence it settled is gone"))?;
    occ.due = dates.due;
    occ.due_at = dates.due_at;
    occ.remind_at = dates.remind_at;
    occ.overdue_at = dates.overdue_at;
    occ.opens_at = dates
        .opens_at
        .unwrap_or_else(|| occ.opens_at.min(dates.due_at));
    occ.shifted = None;
    occ.moved = true;
    occ.revision = spec.revision;
    if occ.reminded_at.is_some() && occ.remind_at.is_some_and(|at| at > now) {
        occ.reminded_at = None;
    }
    if occ.fell_due_at.is_some() && occ.due_at > now {
        occ.fell_due_at = None;
    }
    let view = occ.view();
    let plan_at = settle_plan(cx, &mut plan)?;
    plan.write(cx, plan_at)?;
    Ok(Value::Object(plan.occs.get(&key).map_or(view, Occ::view)))
}

fn list_occurrences(cx: &Cx<'_, RecurrenceConfig>, params: &Value) -> Hook<Value> {
    let mut plan = Plan::load(cx)?;
    plan.catch_up(cx.now());
    let from = params.get("from").and_then(whole).unwrap_or(i64::MIN);
    let to = params.get("to").and_then(whole).unwrap_or(i64::MAX);
    let states: Vec<&str> = params
        .get("states")
        .and_then(Value::as_array)
        .map(|all| all.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    let matching: Vec<&Occ> = plan
        .sorted()
        .into_iter()
        .filter(|occ| occ.due_at >= from && occ.due_at < to)
        .filter(|occ| states.is_empty() || states.contains(&occ.state.as_str()))
        .collect();
    let more = matching.len() > MAX_LISTED;
    let items: Vec<Value> = matching
        .into_iter()
        .take(MAX_LISTED)
        .map(|occ| Value::Object(occ.view()))
        .collect();
    let mut out = Map::new();
    out.insert("items".into(), Value::Array(items));
    if more {
        out.insert("more".into(), json!(true));
    }
    Ok(Value::Object(out))
}

// ---- small helpers ----

fn bad_param<C>(view: &View<'_, C>, operation: &str, issues: Vec<SchemaIssue>) -> Stop {
    Refusal::operation_params(view.behavior(), view.use_id(), operation, issues).into()
}

fn flagged(mut view: Map<String, Value>) -> Value {
    view.insert("unchanged".into(), Value::Bool(true));
    Value::Object(view)
}

fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn text_of<'p>(params: &'p Value, key: &str) -> &'p str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

fn instant(ms: i64) -> String {
    crate::values::date_time_text(ms).unwrap_or_else(|| ms.to_string())
}

// whole reads a whole number as an adapter gives it: a number, or the
// decimal string a BIGINT is read as.
fn whole(value: &Value) -> Option<i64> {
    match value {
        Value::Number(number) => number.as_i64().or_else(|| {
            number
                .as_f64()
                .filter(|f| f.fract() == 0.0)
                .map(|f| f as i64)
        }),
        Value::String(text) => text.parse().ok(),
        _ => None,
    }
}

// key_text is a relation field's key as text.
fn key_text(value: &Value) -> Option<String> {
    match value {
        Value::String(text) if !text.is_empty() => Some(text.clone()),
        Value::Number(number) => Some(number.to_string()),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_occurrences_values_are_read_as_an_adapter_gives_them() {
        let row = SubRow::new(
            "2026-10-31T09:00",
            json!({"due": "2026-11-02T09:00", "dueAt": "1793541600000", "opensAt": 1, "overdueAt": 2, "state": "open", "revision": "3", "moved": true})
                .as_object()
                .unwrap()
                .clone(),
        );
        let occ = Occ::of(&row);
        assert_eq!(occ.due_at, 1_793_541_600_000);
        assert_eq!(occ.revision, 3);
        assert!(occ.moved);
        assert_eq!(occ.view()["state"], json!("open"));
        assert!(occ.view().get("completedBy").is_none());
    }

    #[test]
    fn a_spec_round_trips_and_compares_its_rule_alone() {
        let spec = Spec::of(Some(json!({"anchor": "calendar", "rule": "FREQ=DAILY", "start": "2026-01-01", "timeZone": "UTC", "adjust": "none", "setAt": 5, "revision": 2}))).unwrap();
        assert_eq!(Spec::of(Some(spec.to_json())), Some(spec.clone()));
        let later = Spec {
            set_at: 9,
            revision: 3,
            ..spec.clone()
        };
        assert!(spec.same_rule(&later));
    }
}
