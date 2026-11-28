//! Recurrence's expansion (D152): a recurrence's anchor, its adjustment and
//! its lead times in, its dated occurrences out, with no I/O and no clock.
//! Every language reaches it through the core (the `expand` operation of
//! `superschematic-rules-wasm`, `RulesCore.expand` in TypeScript and
//! `rules.Expand` in Go), so a date one host computes is the date every
//! host computes.
//!
//! Three anchors:
//!
//! - `calendar`: an RFC 5545 `RRULE` from a start, a wall date-time read in
//!   a time zone. The rule is expanded over wall time ([`super::rrule`]),
//!   and each wall time is then made an instant in the zone through the
//!   IANA database jiff bundles, the one Schedules and Calendar read. A
//!   wall time a daylight-saving change skips is read with the offset
//!   before the gap, and one it repeats is its first pass, as RFC 5545
//!   (3.3.5) reads a local time. An `UNTIL` in UTC is made the zone's wall
//!   time first; a floating one or a date is read as wall time.
//! - `afterDone`: an interval after the last completed occurrence: the
//!   first due is the local date of `lastDone` plus `every`, at the wall
//!   time of day of `start`, or `start` itself when nothing is done. Later
//!   occurrences are projected as if each were done on its due date
//!   (`projected: true`): a calendar shows them, and the behavior
//!   materialises only the first.
//! - `relative`: an offset from a date: `date` plus `offset`, and, with
//!   `every`, `date` plus each whole `every` plus `offset` (the date's
//!   anniversaries).
//!
//! The adjustment moves a due date that falls on a weekend day or a
//! holiday to the business day `before` or `after` it, keeping the wall
//! time; `none` leaves it. The key of an occurrence is its nominal wall
//! date-time (`2026-10-31T09:00`), before the adjustment, so an adjustment
//! that changes, a holiday added say, moves the same occurrence. A holiday
//! is a date (`2026-12-25`) or a weekday's name (`friday`), as a row of a
//! holiday calendar gives one (#729): a weekday closes that day every
//! week, and a list with one or more takes its weekdays as the weekend in
//! place of `weekendDays`, so each calendar has the weekend its rows say.
//!
//! Lead times are spans before or after the adjusted due, in the zone's
//! calendar: `opensBefore` (the window's opening), `remindBefore` and
//! `overdueAfter` (zero when absent). A span is ISO 8601 (`P90D`, `P1M`,
//! `-P60D`, `PT12H`), read by jiff in the zone, so a day is a calendar day
//! across a daylight-saving change.

use jiff::civil::{Date, DateTime, Time, Weekday};
use jiff::tz::TimeZone;
use jiff::{Span, Timestamp, Zoned};
use serde::Deserialize;
use serde_json::{json, Map, Value};

use super::rrule::Rule;
use crate::calendar::time::zone;

/// The most occurrences one expansion answers.
pub const MAX_LIMIT: usize = 5000;

/// The occurrences an expansion answers when its input names no limit.
pub const DEFAULT_LIMIT: usize = 1000;

/// How many days an adjustment may move a due date before it refuses: a
/// calendar whose every day is a weekend day or a holiday has no business
/// day to move to.
pub const MAX_SHIFT_DAYS: i64 = 31;

/// How many nominal occurrences one expansion reads at most, the ones
/// before its window among them: a rule of every minute from a start
/// decades back stops here.
pub const MAX_STEPS: usize = 100_000;

/// What an expansion takes, as the core's JSON has it.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ExpandInput {
    pub anchor: String,
    #[serde(default)]
    pub time_zone: Option<String>,
    #[serde(default)]
    pub rule: Option<String>,
    #[serde(default)]
    pub start: Option<String>,
    #[serde(default)]
    pub every: Option<String>,
    #[serde(default)]
    pub last_done: Option<i64>,
    #[serde(default)]
    pub date: Option<Value>,
    #[serde(default)]
    pub offset: Option<String>,
    #[serde(default)]
    pub adjust: Option<String>,
    #[serde(default)]
    pub weekend_days: Option<Vec<String>>,
    #[serde(default)]
    pub holidays: Vec<String>,
    #[serde(default)]
    pub opens_before: Option<String>,
    #[serde(default)]
    pub remind_before: Option<String>,
    #[serde(default)]
    pub overdue_after: Option<String>,
    #[serde(default)]
    pub from: Option<i64>,
    #[serde(default)]
    pub to: Option<i64>,
    #[serde(default)]
    pub limit: Option<usize>,
}

/// One dated occurrence.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Occurrence {
    /// Its nominal wall date-time, before the adjustment: its key.
    pub key: String,
    /// The wall date-time it falls due at, after the adjustment.
    pub due: String,
    /// When it falls due, in epoch milliseconds.
    pub due_at: i64,
    /// When its window opens, in epoch milliseconds; absent with no
    /// `opensBefore`.
    pub opens_at: Option<i64>,
    /// When it reminds, in epoch milliseconds; absent with no
    /// `remindBefore`.
    pub remind_at: Option<i64>,
    /// When it is overdue, in epoch milliseconds: `overdueAfter` past its
    /// due, its due when absent.
    pub overdue_at: i64,
    /// Why the adjustment moved it: `weekend` or `holiday`; absent when it
    /// did not.
    pub shifted: Option<&'static str>,
    /// An afterDone occurrence past the first, dated as if each before it
    /// were done on its due date.
    pub projected: bool,
}

impl Occurrence {
    /// The occurrence as the core's JSON has it, an absent member left out.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("key".into(), json!(self.key));
        out.insert("due".into(), json!(self.due));
        out.insert("dueAt".into(), json!(self.due_at));
        if let Some(at) = self.opens_at {
            out.insert("opensAt".into(), json!(at));
        }
        if let Some(at) = self.remind_at {
            out.insert("remindAt".into(), json!(at));
        }
        out.insert("overdueAt".into(), json!(self.overdue_at));
        if let Some(reason) = self.shifted {
            out.insert("shifted".into(), json!(reason));
        }
        if self.projected {
            out.insert("projected".into(), json!(true));
        }
        Value::Object(out)
    }
}

/// What an expansion answers: the occurrences in due order (ties by key),
/// and whether more lie in the window past the limit.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Expansion {
    pub occurrences: Vec<Occurrence>,
    pub more: bool,
}

impl Expansion {
    /// The expansion as the core's JSON has it: `{"occurrences": [...],
    /// "more": bool}`.
    pub fn to_json(&self) -> Value {
        json!({
            "occurrences": self.occurrences.iter().map(Occurrence::to_json).collect::<Vec<_>>(),
            "more": self.more,
        })
    }
}

/// Expands a recurrence's JSON input to its JSON output, or the reason the
/// input is refused.
pub fn expand_json(input: &Value) -> Result<Value, String> {
    let input: ExpandInput =
        serde_json::from_value(input.clone()).map_err(|error| error.to_string())?;
    expand(&input).map(|expansion| expansion.to_json())
}

/// The adjustment of a due date that falls on a weekend day or a holiday.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Adjust {
    None,
    Before,
    After,
}

impl Adjust {
    /// The adjustment a name gives: none, before or after.
    pub fn named(name: &str) -> Result<Adjust, String> {
        match name {
            "none" => Ok(Adjust::None),
            "before" => Ok(Adjust::Before),
            "after" => Ok(Adjust::After),
            other => Err(format!(
                "adjust \"{other}\" is none of none, before and after"
            )),
        }
    }
}

/// The days of the week a name list gives, by their English names or their
/// first three letters, any case.
pub fn weekdays(names: &[String]) -> Result<Vec<Weekday>, String> {
    names
        .iter()
        .map(|name| match name.to_ascii_lowercase().as_str() {
            "mon" | "monday" => Ok(Weekday::Monday),
            "tue" | "tuesday" => Ok(Weekday::Tuesday),
            "wed" | "wednesday" => Ok(Weekday::Wednesday),
            "thu" | "thursday" => Ok(Weekday::Thursday),
            "fri" | "friday" => Ok(Weekday::Friday),
            "sat" | "saturday" => Ok(Weekday::Saturday),
            "sun" | "sunday" => Ok(Weekday::Sunday),
            _ => Err(format!("\"{name}\" is no day of the week")),
        })
        .collect()
}

/// A span of ISO 8601 (`P90D`, `-P60D`, `P1M`, `PT12H`).
pub fn span(text: &str, what: &str) -> Result<Span, String> {
    text.parse::<Span>()
        .map_err(|_| format!("{what} \"{text}\" is no ISO 8601 duration (P90D, -P60D, P1M)"))
}

/// A wall date-time: `YYYY-MM-DD` (its midnight) or `YYYY-MM-DDTHH:MM[:SS]`,
/// with no offset or zone.
pub fn wall(text: &str, what: &str) -> Result<DateTime, String> {
    let refused =
        || format!("{what} \"{text}\" is no wall date or date-time (2026-10-31, 2026-10-31T09:00)");
    if text.len() == 10 {
        return text
            .parse::<Date>()
            .map(|date| date.to_datetime(Time::midnight()))
            .map_err(|_| refused());
    }
    if text.ends_with('Z')
        || text.contains('[')
        || text
            .get(10..)
            .is_some_and(|t| t.contains('+') || t.contains('-'))
    {
        return Err(refused());
    }
    text.parse::<DateTime>().map_err(|_| refused())
}

/// A wall date-time's text as a key has it: `2026-10-31T09:00`, the
/// seconds when not zero.
pub fn wall_text(at: DateTime) -> String {
    let base = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}",
        at.year(),
        at.month(),
        at.day(),
        at.hour(),
        at.minute()
    );
    if at.second() == 0 {
        base
    } else {
        format!("{base}:{:02}", at.second())
    }
}

/// The time zone of a name in the bundled database, UTC when absent.
pub fn zone_of(name: Option<&str>) -> Result<TimeZone, String> {
    match name {
        None => Ok(TimeZone::UTC),
        Some(name) => zone(name).ok_or_else(|| {
            format!("\"{name}\" is not a time zone of the IANA database the core bundles")
        }),
    }
}

// instant is the instant of a wall time in a zone: a time a gap skips is
// read with the offset before it (later), one a fold repeats is its first
// pass, as RFC 5545 reads a local time and jiff's compatible disambiguation
// does.
fn instant(tz: &TimeZone, at: DateTime) -> Result<Zoned, String> {
    tz.to_ambiguous_zoned(at)
        .compatible()
        .map_err(|error| format!("{at} has no instant in the zone: {error}"))
}

fn ms(zoned: &Zoned) -> i64 {
    zoned.timestamp().as_millisecond()
}

// plus adds a span to a zoned time in its zone's calendar.
fn plus(zoned: &Zoned, by: Span, what: &str) -> Result<Zoned, String> {
    zoned
        .checked_add(by)
        .map_err(|error| format!("{what} is out of range: {error}"))
}

fn wall_plus(at: DateTime, by: Span, what: &str) -> Result<DateTime, String> {
    at.checked_add(by)
        .map_err(|error| format!("{what} is out of range: {error}"))
}

/// The settled input of an expansion.
struct Plan {
    tz: TimeZone,
    adjust: Adjust,
    weekend: Vec<Weekday>,
    holidays: std::collections::BTreeSet<Date>,
    opens_before: Option<Span>,
    remind_before: Option<Span>,
    overdue_after: Option<Span>,
    from: Option<i64>,
    to: Option<i64>,
    limit: usize,
}

/// Expands a recurrence: its occurrences whose due lies in `[from, to)`,
/// in due order, ties by key, at most `limit` of them.
pub fn expand(input: &ExpandInput) -> Result<Expansion, String> {
    let tz = zone_of(input.time_zone.as_deref())?;
    let adjust = Adjust::named(input.adjust.as_deref().unwrap_or("none"))?;
    let mut holidays = std::collections::BTreeSet::new();
    let mut closed: Vec<Weekday> = Vec::new();
    for text in &input.holidays {
        if let Ok(date) = text.parse::<Date>() {
            holidays.insert(date);
            continue;
        }
        let day = weekdays(std::slice::from_ref(text))
            .map_err(|_| format!("holiday \"{text}\" is no date (2026-12-25) and no weekday (friday)"))?;
        for day in day {
            if !closed.contains(&day) {
                closed.push(day);
            }
        }
    }
    // A calendar's weekday rows are its weekend, in place of the config's.
    let weekend = match &input.weekend_days {
        _ if !closed.is_empty() => closed,
        Some(names) => weekdays(names)?,
        None => vec![Weekday::Saturday, Weekday::Sunday],
    };
    let lead =
        |text: &Option<String>, what: &str| text.as_deref().map(|t| span(t, what)).transpose();
    let limit = input.limit.unwrap_or(DEFAULT_LIMIT);
    if limit == 0 || limit > MAX_LIMIT {
        return Err(format!("limit {limit} is not between 1 and {MAX_LIMIT}"));
    }
    // A rule with neither COUNT nor UNTIL, and no window's end, has no end:
    // the limit bounds it.
    let plan = Plan {
        tz,
        adjust,
        weekend,
        holidays,
        opens_before: lead(&input.opens_before, "opensBefore")?,
        remind_before: lead(&input.remind_before, "remindBefore")?,
        overdue_after: lead(&input.overdue_after, "overdueAfter")?,
        from: input.from,
        to: input.to,
        limit,
    };
    match input.anchor.as_str() {
        "calendar" => calendar(input, &plan),
        "afterDone" => after_done(input, &plan),
        "relative" => relative(input, &plan),
        other => Err(format!(
            "anchor \"{other}\" is none of calendar, afterDone and relative"
        )),
    }
}

/// What a nominal wall time becomes, or why it is past the window.
enum Step {
    /// An occurrence, kept.
    Kept(Occurrence),
    /// Before the window: read on.
    Before,
    /// At or after the window's end: stop.
    Past,
}

impl Plan {
    // shift moves a wall date-time off weekend days and holidays as the
    // adjustment says, and says why it moved.
    fn shift(&self, at: DateTime) -> Result<(DateTime, Option<&'static str>), String> {
        let closed = |date: Date| {
            if self.holidays.contains(&date) {
                Some("holiday")
            } else if self.weekend.contains(&date.weekday()) {
                Some("weekend")
            } else {
                None
            }
        };
        let Some(reason) = closed(at.date()) else {
            return Ok((at, None));
        };
        let step = match self.adjust {
            Adjust::None => return Ok((at, None)),
            Adjust::Before => -1,
            Adjust::After => 1,
        };
        let mut moved = at;
        for _ in 0..MAX_SHIFT_DAYS {
            moved = wall_plus(moved, Span::new().days(step), "an adjusted due date")?;
            if closed(moved.date()).is_none() {
                return Ok((moved, Some(reason)));
            }
        }
        Err(format!(
            "{} has no business day within {MAX_SHIFT_DAYS} days {}",
            wall_text(at),
            if step < 0 { "before it" } else { "after it" }
        ))
    }

    // occurrence dates a nominal wall time: its adjustment, its instant and
    // its lead times, and where it lies against the window.
    fn occurrence(&self, nominal: DateTime, projected: bool) -> Result<Step, String> {
        let (due, shifted) = self.shift(nominal)?;
        let zoned = instant(&self.tz, due)?;
        let due_at = ms(&zoned);
        if self.to.is_some_and(|to| due_at >= to) {
            // An adjustment back across the window's end may still bring a
            // later nominal inside; the caller reads past by a margin.
            return Ok(Step::Past);
        }
        if self.from.is_some_and(|from| due_at < from) {
            return Ok(Step::Before);
        }
        let before = |by: Option<Span>, what: &str| -> Result<Option<i64>, String> {
            by.map(|span| plus(&zoned, span.negate(), what).map(|z| ms(&z)))
                .transpose()
        };
        let overdue_at = match self.overdue_after {
            Some(span) => ms(&plus(&zoned, span, "overdueAfter")?),
            None => due_at,
        };
        Ok(Step::Kept(Occurrence {
            key: wall_text(nominal),
            due: wall_text(due),
            due_at,
            opens_at: before(self.opens_before, "opensBefore")?,
            remind_at: before(self.remind_before, "remindBefore")?,
            overdue_at,
            shifted,
            projected,
        }))
    }

    // collect reads nominal wall times in order until the window's end (and
    // a margin past it, which an adjustment before may bring back) or the
    // limit, and sorts what it kept by due, ties by key.
    fn collect(
        &self,
        nominals: impl Iterator<Item = Result<(DateTime, bool), String>>,
    ) -> Result<Expansion, String> {
        let mut kept: Vec<Occurrence> = Vec::new();
        let mut past = 0usize;
        let margin = if self.adjust == Adjust::Before {
            MAX_SHIFT_DAYS as usize + 1
        } else {
            1
        };
        let mut more = false;
        for (steps, nominal) in nominals.enumerate() {
            if steps >= MAX_STEPS {
                return Err(format!("the rule reads more than {MAX_STEPS} occurrences before its window ends; narrow the window or the rule"));
            }
            let (nominal, projected) = nominal?;
            match self.occurrence(nominal, projected)? {
                Step::Kept(occurrence) => {
                    if kept.len() == self.limit {
                        more = true;
                        break;
                    }
                    kept.push(occurrence);
                }
                Step::Before => {}
                Step::Past => {
                    past += 1;
                    if past >= margin {
                        break;
                    }
                }
            }
        }
        kept.sort_by(|a, b| a.due_at.cmp(&b.due_at).then_with(|| a.key.cmp(&b.key)));
        Ok(Expansion {
            occurrences: kept,
            more,
        })
    }
}

// The calendar anchor: an RRULE from a start, over wall time.
fn calendar(input: &ExpandInput, plan: &Plan) -> Result<Expansion, String> {
    let rule = input
        .rule
        .as_deref()
        .ok_or("the calendar anchor needs rule, an RFC 5545 RRULE")?;
    let start = wall(
        input
            .start
            .as_deref()
            .ok_or("the calendar anchor needs start, a wall date-time")?,
        "start",
    )?;
    let rule = read_rule(rule, &plan.tz)?;
    let nominals = rule.occurrences(start).map(|at| Ok((at, false)));
    plan.collect(nominals)
}

/// Checks an RFC 5545 RRULE from a start in a zone, as `calendar` reads
/// it: the reason it is refused, or nothing.
pub fn check_rule(rule: &str, start: &str, time_zone: Option<&str>) -> Result<(), String> {
    let tz = zone_of(time_zone)?;
    wall(start, "start")?;
    read_rule(rule, &tz).map(|_| ())
}

/// Reads a rule's text, with or without its `RRULE:` name, its `UNTIL` in
/// UTC made the zone's wall time.
pub fn read_rule(rule: &str, tz: &TimeZone) -> Result<Rule, String> {
    let text = rule.trim();
    let text = text.strip_prefix("RRULE:").unwrap_or(text);
    if text.is_empty() || text.contains(['\n', '\r', ':']) {
        return Err(format!(
            "rule \"{rule}\" is no RRULE: one line of NAME=VALUE parts (FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31)"
        ));
    }
    let mut parts = Vec::new();
    for part in text.split(';') {
        match part.split_once('=') {
            Some((name, value)) if name.trim().eq_ignore_ascii_case("UNTIL") => {
                parts.push(format!("UNTIL={}", wall_until(value.trim(), tz)?));
            }
            _ => parts.push(part.to_owned()),
        }
    }
    Rule::parse(&parts.join(";")).map_err(|reason| format!("rule \"{rule}\": {reason}"))
}

// wall_until makes an UNTIL in UTC (a trailing Z) the zone's wall time; a
// date or a floating date-time is wall time already.
fn wall_until(value: &str, tz: &TimeZone) -> Result<String, String> {
    let Some(utc) = value.strip_suffix('Z').or_else(|| value.strip_suffix('z')) else {
        return Ok(value.to_owned());
    };
    let refused = || format!("UNTIL \"{value}\" is no UTC date-time (20271231T235959Z)");
    let (date, time) = utc.split_once(['T', 't']).ok_or_else(refused)?;
    if date.len() != 8
        || time.len() != 6
        || !date.bytes().chain(time.bytes()).all(|b| b.is_ascii_digit())
    {
        return Err(refused());
    }
    let n = |s: &str| s.parse::<i16>().map_err(|_| refused());
    let day = Date::new(n(&date[..4])?, n(&date[4..6])? as i8, n(&date[6..])? as i8)
        .map_err(|_| refused())?;
    let of_day = Time::new(
        n(&time[..2])? as i8,
        n(&time[2..4])? as i8,
        n(&time[4..])? as i8,
        0,
    )
    .map_err(|_| refused())?;
    let at = day.to_datetime(of_day);
    let local = at
        .to_zoned(TimeZone::UTC)
        .map_err(|_| refused())?
        .with_time_zone(tz.clone())
        .datetime();
    Ok(format!(
        "{:04}{:02}{:02}T{:02}{:02}{:02}",
        local.year(),
        local.month(),
        local.day(),
        local.hour(),
        local.minute(),
        local.second()
    ))
}

// The afterDone anchor: the first due from the last done, the rest
// projected.
fn after_done(input: &ExpandInput, plan: &Plan) -> Result<Expansion, String> {
    let every = span(
        input
            .every
            .as_deref()
            .ok_or("the afterDone anchor needs every, an ISO 8601 duration (P90D)")?,
        "every",
    )?;
    if every.is_negative() || every.is_zero() {
        return Err(format!(
            "every \"{}\" is not after its base",
            input.every.as_deref().unwrap_or("")
        ));
    }
    let start = wall(
        input
            .start
            .as_deref()
            .ok_or("the afterDone anchor needs start, the first due when nothing is done")?,
        "start",
    )?;
    let first = match input.last_done {
        None => start,
        Some(done) => {
            let done = Timestamp::from_millisecond(done)
                .map_err(|_| format!("lastDone {done} is out of range"))?
                .to_zoned(plan.tz.clone());
            wall_plus(
                done.date().to_datetime(start.time()),
                every,
                "the due after lastDone",
            )?
        }
    };
    let mut nominal = Some(first);
    let mut index = 0usize;
    let nominals = std::iter::from_fn(move || {
        let at = nominal?;
        let projected = index > 0;
        index += 1;
        // The next is done on this one's adjusted due date.
        nominal = plan
            .shift(at)
            .ok()
            .and_then(|(due, _)| wall_plus(due, every, "a projected due").ok());
        Some(Ok((at, projected)))
    });
    plan.collect(nominals)
}

// The relative anchor: a date plus an offset, and its anniversaries by
// every.
fn relative(input: &ExpandInput, plan: &Plan) -> Result<Expansion, String> {
    let date = match &input.date {
        Some(Value::String(text)) => wall(text, "date")?,
        Some(Value::Number(number)) => {
            let at = number
                .as_i64()
                .ok_or("date is no whole number of epoch milliseconds")?;
            Timestamp::from_millisecond(at)
                .map_err(|_| format!("date {at} is out of range"))?
                .to_zoned(plan.tz.clone())
                .datetime()
        }
        Some(_) => return Err("date is neither a wall date-time nor epoch milliseconds".into()),
        None => return Err("the relative anchor needs date, the date its offset is from".into()),
    };
    let offset = match input.offset.as_deref() {
        Some(text) => span(text, "offset")?,
        None => Span::new(),
    };
    let every = input
        .every
        .as_deref()
        .map(|text| span(text, "every"))
        .transpose()?;
    if let Some(every) = every {
        if every.is_negative() || every.is_zero() {
            return Err(format!(
                "every \"{}\" is not after its base",
                input.every.as_deref().unwrap_or("")
            ));
        }
    }
    let mut k: i64 = 0;
    let mut done = false;
    let nominals = std::iter::from_fn(move || {
        if done {
            return None;
        }
        let base = match every {
            None => {
                done = true;
                Ok(date)
            }
            Some(every) => every
                .checked_mul(k)
                .map_err(|error| format!("anniversary {k} is out of range: {error}"))
                .and_then(|by| wall_plus(date, by, "an anniversary")),
        };
        k += 1;
        Some(
            base.and_then(|base| wall_plus(base, offset, "date plus offset"))
                .map(|at| (at, false)),
        )
    });
    plan.collect(nominals)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn run(input: Value) -> Value {
        expand_json(&input).unwrap_or_else(|error| panic!("{error}"))
    }

    #[test]
    fn a_yearly_rule_in_a_zone_keeps_its_wall_time_across_a_change() {
        let out = run(json!({
            "anchor": "calendar", "timeZone": "America/New_York",
            "rule": "FREQ=MONTHLY;BYMONTHDAY=1", "start": "2026-10-01T09:00",
            "to": 1_799_000_000_000_i64, "limit": 3
        }));
        let due: Vec<_> = out["occurrences"]
            .as_array()
            .unwrap()
            .iter()
            .map(|o| o["due"].clone())
            .collect();
        assert_eq!(
            due,
            vec![
                json!("2026-10-01T09:00"),
                json!("2026-11-01T09:00"),
                json!("2026-12-01T09:00")
            ]
        );
        // 09:00 EDT is 13:00Z; 09:00 EST is 14:00Z.
        assert_eq!(out["occurrences"][0]["dueAt"], json!(1_790_859_600_000_i64));
        assert_eq!(out["occurrences"][1]["dueAt"], json!(1_793_541_600_000_i64));
    }

    #[test]
    fn a_weekend_moves_after_and_before() {
        // 2026-10-31 is a Saturday.
        let base = json!({"anchor": "relative", "date": "2026-10-31", "timeZone": "UTC"});
        let mut after = base.clone();
        after["adjust"] = json!("after");
        assert_eq!(
            run(after)["occurrences"][0]["due"],
            json!("2026-11-02T00:00")
        );
        let mut before = base.clone();
        before["adjust"] = json!("before");
        assert_eq!(
            run(before.clone())["occurrences"][0]["due"],
            json!("2026-10-30T00:00")
        );
        before["holidays"] = json!(["2026-10-30"]);
        let out = run(before);
        assert_eq!(out["occurrences"][0]["due"], json!("2026-10-29T00:00"));
        assert_eq!(out["occurrences"][0]["shifted"], json!("weekend"));
        assert_eq!(out["occurrences"][0]["key"], json!("2026-10-31T00:00"));
    }

    #[test]
    fn a_calendars_weekday_holidays_are_its_weekend() {
        // 2026-10-31 is a Saturday and 2026-11-01 a Sunday; the calendar
        // closes on Friday and Saturday, in place of weekendDays.
        let calendar = json!({"anchor": "relative", "date": "2026-10-31", "timeZone": "UTC", "adjust": "before", "weekendDays": ["sunday"], "holidays": ["friday", "Saturday", "2026-10-29"]});
        let out = run(calendar.clone());
        assert_eq!(out["occurrences"][0]["due"], json!("2026-10-28T00:00"));
        assert_eq!(out["occurrences"][0]["shifted"], json!("weekend"));
        let mut sunday = calendar;
        sunday["date"] = json!("2026-11-01");
        assert!(run(sunday)["occurrences"][0].get("shifted").is_none());
        let error = expand_json(&json!({"anchor": "relative", "date": "2026-10-31", "holidays": ["someday"]})).unwrap_err();
        assert!(error.contains("no weekday"), "{error}");
    }

    #[test]
    fn until_in_utc_is_read_in_the_zone() {
        let out = run(json!({
            "anchor": "calendar", "timeZone": "Asia/Tokyo",
            "rule": "FREQ=DAILY;UNTIL=20270102T000000Z", "start": "2027-01-01T08:00"
        }));
        // 2027-01-02T00:00Z is 09:00 in Tokyo, so the 2nd's 08:00 is in.
        assert_eq!(out["occurrences"].as_array().unwrap().len(), 2);
    }

    #[test]
    fn refusals_name_what_is_wrong() {
        for (input, part) in [
            (json!({"anchor": "weekly"}), "anchor"),
            (
                json!({"anchor": "calendar", "rule": "FREQ=YEARLY"}),
                "start",
            ),
            (
                json!({"anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": "2026-01-01"}),
                "rule",
            ),
            (
                json!({"anchor": "calendar", "rule": "FREQ=DAILY", "start": "2026-01-01", "timeZone": "Mars/Base"}),
                "time zone",
            ),
            (
                json!({"anchor": "afterDone", "every": "-P1D", "start": "2026-01-01"}),
                "every",
            ),
            (
                json!({"anchor": "relative", "date": "2026-01-03", "adjust": "after", "weekendDays": ["mon","tue","wed","thu","fri","sat","sun"]}),
                "business day",
            ),
        ] {
            let error = expand_json(&input).unwrap_err();
            assert!(error.contains(part), "{error} names no {part}");
        }
    }
}
