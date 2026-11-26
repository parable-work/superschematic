//! An RFC 5545 recurrence rule (3.3.10, `RRULE`) expanded over wall time
//! (D152), with no time zone: a rule's occurrences are civil date-times,
//! which [`super::expand()`] makes instants in the row's zone.
//!
//! It reads `FREQ` (`YEARLY`, `MONTHLY`, `WEEKLY`, `DAILY` or `HOURLY`),
//! `INTERVAL`, `COUNT`, `UNTIL` (a wall date or date-time: the caller makes
//! a UTC one wall time first), `BYMONTH`, `BYWEEKNO`, `BYYEARDAY`,
//! `BYMONTHDAY`, `BYDAY` (with an ordinal, `2MO`, `-1FR`), `BYHOUR`,
//! `BYMINUTE`, `BYSECOND`, `BYSETPOS` and `WKST`. `MINUTELY` and
//! `SECONDLY` are refused: a recurrence dates occurrences an hour apart at
//! the finest, and Schedules' cron line runs finer.
//!
//! The expansion is the one python-dateutil and the rrule crate make, the
//! reading every calendar client shares: each period of the frequency, from
//! the start's, `INTERVAL` apart, holds the days that pass every `BY` rule
//! the rule gives (with the start's month and day, or its weekday, when it
//! gives none of the day rules), at the times `BYHOUR`, `BYMINUTE` and
//! `BYSECOND` give (the start's when absent); `BYSETPOS` picks among a
//! period's set; the start itself counts only when the rule names it; and
//! `COUNT` and `UNTIL` end it. The rrule crate is this module's oracle: a
//! test expands a battery of rules with both and holds them equal. The
//! crate is not in the build, since the chrono it carries links JavaScript
//! imports into the wasm32 module, which imports nothing.

use jiff::civil::{Date, DateTime, Time, Weekday};
use jiff::{Span, ToSpan};

/// How many periods in a row that hold no occurrence an expansion visits
/// before it stops, by frequency: some 400 years of yearly, monthly,
/// weekly and daily periods (a 29th of February with a Friday the 13th
/// recurs in fewer), ten years of hourly ones. A rule that never matches
/// (the 30th of February) ends there.
pub fn max_empty_periods(freq: Freq) -> usize {
    match freq {
        Freq::Yearly => 400,
        Freq::Monthly => 4_800,
        Freq::Weekly => 20_871,
        Freq::Daily => 146_097,
        Freq::Hourly => 87_660,
    }
}

/// A rule's frequency.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Freq {
    Yearly,
    Monthly,
    Weekly,
    Daily,
    Hourly,
}

/// A rule, read.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Rule {
    pub freq: Freq,
    pub interval: i64,
    pub count: Option<u64>,
    pub until: Option<DateTime>,
    pub by_month: Vec<i8>,
    pub by_week_no: Vec<i8>,
    pub by_year_day: Vec<i16>,
    pub by_month_day: Vec<i8>,
    /// A weekday with its ordinal, 0 for every one.
    pub by_day: Vec<(i8, Weekday)>,
    pub by_hour: Vec<i8>,
    pub by_minute: Vec<i8>,
    pub by_second: Vec<i8>,
    pub by_set_pos: Vec<i32>,
    pub week_start: Weekday,
}

fn weekday(code: &str) -> Option<Weekday> {
    Some(match code {
        "MO" => Weekday::Monday,
        "TU" => Weekday::Tuesday,
        "WE" => Weekday::Wednesday,
        "TH" => Weekday::Thursday,
        "FR" => Weekday::Friday,
        "SA" => Weekday::Saturday,
        "SU" => Weekday::Sunday,
        _ => return None,
    })
}

// numbers reads a list of whole numbers within [low, high], zero among them
// only when `zero` says.
fn numbers<T>(name: &str, value: &str, low: T, high: T, zero: bool) -> Result<Vec<T>, String>
where
    T: std::str::FromStr + PartialOrd + Copy + Default,
{
    value
        .split(',')
        .map(|part| {
            let n: T = part
                .trim()
                .parse()
                .map_err(|_| format!("{name} \"{part}\" is no whole number"))?;
            if n < low || n > high || (!zero && n == T::default()) {
                return Err(format!("{name} \"{part}\" is out of range"));
            }
            Ok(n)
        })
        .collect()
}

impl Rule {
    /// Reads a rule's text, `FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31`, its
    /// `UNTIL` a wall date (`20271231`, the end of that day) or date-time
    /// (`20271231T235959`, a trailing `Z` read as wall time too).
    pub fn parse(text: &str) -> Result<Rule, String> {
        let mut freq = None;
        let mut rule = Rule {
            freq: Freq::Yearly,
            interval: 1,
            count: None,
            until: None,
            by_month: Vec::new(),
            by_week_no: Vec::new(),
            by_year_day: Vec::new(),
            by_month_day: Vec::new(),
            by_day: Vec::new(),
            by_hour: Vec::new(),
            by_minute: Vec::new(),
            by_second: Vec::new(),
            by_set_pos: Vec::new(),
            week_start: Weekday::Monday,
        };
        let mut seen = std::collections::BTreeSet::new();
        for part in text.split(';') {
            let Some((name, value)) = part.split_once('=') else {
                return Err(format!("\"{part}\" is no NAME=VALUE part"));
            };
            let name = name.trim().to_ascii_uppercase();
            let value = value.trim().to_ascii_uppercase();
            if !seen.insert(name.clone()) {
                return Err(format!("{name} is given twice"));
            }
            if value.is_empty() {
                return Err(format!("{name} has no value"));
            }
            match name.as_str() {
                "FREQ" => {
                    freq = Some(match value.as_str() {
                        "YEARLY" => Freq::Yearly,
                        "MONTHLY" => Freq::Monthly,
                        "WEEKLY" => Freq::Weekly,
                        "DAILY" => Freq::Daily,
                        "HOURLY" => Freq::Hourly,
                        "MINUTELY" | "SECONDLY" => {
                            return Err(format!(
                                "FREQ={value} is finer than a recurrence dates: HOURLY at the finest (Schedules' cron runs finer)"
                            ))
                        }
                        _ => return Err(format!("FREQ \"{value}\" is no frequency")),
                    })
                }
                "INTERVAL" => {
                    rule.interval = value
                        .parse::<i64>()
                        .ok()
                        .filter(|n| (1..=10_000).contains(n))
                        .ok_or_else(|| format!("INTERVAL \"{value}\" is not between 1 and 10000"))?
                }
                "COUNT" => {
                    rule.count = Some(
                        value
                            .parse::<u64>()
                            .ok()
                            .filter(|n| *n >= 1)
                            .ok_or_else(|| format!("COUNT \"{value}\" is no whole number from 1"))?,
                    )
                }
                "UNTIL" => rule.until = Some(until(&value)?),
                "BYMONTH" => rule.by_month = numbers("BYMONTH", &value, 1, 12, false)?,
                "BYWEEKNO" => rule.by_week_no = numbers("BYWEEKNO", &value, -53, 53, false)?,
                "BYYEARDAY" => rule.by_year_day = numbers("BYYEARDAY", &value, -366, 366, false)?,
                "BYMONTHDAY" => rule.by_month_day = numbers("BYMONTHDAY", &value, -31, 31, false)?,
                "BYHOUR" => rule.by_hour = numbers("BYHOUR", &value, 0, 23, true)?,
                "BYMINUTE" => rule.by_minute = numbers("BYMINUTE", &value, 0, 59, true)?,
                "BYSECOND" => rule.by_second = numbers("BYSECOND", &value, 0, 59, true)?,
                "BYSETPOS" => rule.by_set_pos = numbers("BYSETPOS", &value, -366, 366, false)?,
                "WKST" => {
                    rule.week_start =
                        weekday(&value).ok_or_else(|| format!("WKST \"{value}\" is no weekday"))?
                }
                "BYDAY" => {
                    for day in value.split(',') {
                        let day = day.trim();
                        let split = day.len().saturating_sub(2);
                        let (ordinal, code) = day.split_at(split);
                        let day_of = weekday(code).ok_or_else(|| format!("BYDAY \"{day}\" is no weekday"))?;
                        let ordinal = match ordinal {
                            "" => 0,
                            text => text
                                .trim_start_matches('+')
                                .parse::<i8>()
                                .ok()
                                .filter(|n| *n != 0 && (-53..=53).contains(n))
                                .ok_or_else(|| format!("BYDAY \"{day}\" has no ordinal between -53 and 53"))?,
                        };
                        rule.by_day.push((ordinal, day_of));
                    }
                }
                "RSCALE" | "SKIP" | "BYEASTER" => {
                    return Err(format!("{name} is not of RFC 5545, which a recurrence reads"))
                }
                _ => return Err(format!("{name} is no part of an RRULE")),
            }
        }
        rule.freq = freq.ok_or("the rule has no FREQ")?;
        if rule.count.is_some() && rule.until.is_some() {
            return Err("COUNT and UNTIL are both given; RFC 5545 takes one".into());
        }
        if !rule.by_week_no.is_empty() && rule.freq != Freq::Yearly {
            return Err("BYWEEKNO is for FREQ=YEARLY alone".into());
        }
        if !rule.by_year_day.is_empty()
            && matches!(rule.freq, Freq::Monthly | Freq::Weekly | Freq::Daily)
        {
            return Err("BYYEARDAY is not for FREQ=MONTHLY, WEEKLY or DAILY".into());
        }
        if !rule.by_month_day.is_empty() && rule.freq == Freq::Weekly {
            return Err("BYMONTHDAY is not for FREQ=WEEKLY".into());
        }
        if rule.by_day.iter().any(|(n, _)| *n != 0)
            && !(rule.freq == Freq::Monthly
                || (rule.freq == Freq::Yearly && rule.by_week_no.is_empty()))
        {
            return Err(
                "an ordinal BYDAY (2MO) is for FREQ=MONTHLY or YEARLY without BYWEEKNO".into(),
            );
        }
        Ok(rule)
    }

    /// The occurrences from a start, in order, each a wall date-time, until
    /// the rule ends or the caller stops reading.
    pub fn occurrences(&self, start: DateTime) -> Occurrences<'_> {
        Occurrences {
            rule: self,
            start,
            period: 0,
            pending: Vec::new(),
            emitted: 0,
            empty: 0,
            done: false,
        }
    }
}

// until reads a rule's UNTIL as wall time.
fn until(value: &str) -> Result<DateTime, String> {
    let refused =
        || format!("UNTIL \"{value}\" is no date or date-time (20271231, 20271231T235959)");
    let text = value.strip_suffix('Z').unwrap_or(value);
    let digits = |s: &str| s.bytes().all(|b| b.is_ascii_digit());
    let (date, time) = match text.split_once('T') {
        None if text.len() == 8 && digits(text) => (text, None),
        Some((d, t)) if d.len() == 8 && t.len() == 6 && digits(d) && digits(t) => (d, Some(t)),
        _ => return Err(refused()),
    };
    let n = |s: &str| s.parse::<i16>().map_err(|_| refused());
    let date = Date::new(n(&date[..4])?, n(&date[4..6])? as i8, n(&date[6..])? as i8)
        .map_err(|_| refused())?;
    let time = match time {
        None => Time::new(23, 59, 59, 0),
        Some(t) => Time::new(n(&t[..2])? as i8, n(&t[2..4])? as i8, n(&t[4..])? as i8, 0),
    }
    .map_err(|_| refused())?;
    Ok(date.to_datetime(time))
}

/// A rule's occurrences from a start.
pub struct Occurrences<'r> {
    rule: &'r Rule,
    start: DateTime,
    period: i64,
    pending: Vec<DateTime>,
    emitted: u64,
    empty: usize,
    done: bool,
}

impl Iterator for Occurrences<'_> {
    type Item = DateTime;

    fn next(&mut self) -> Option<DateTime> {
        loop {
            if self.done {
                return None;
            }
            if let Some(at) = self.pending.pop() {
                if self.rule.until.is_some_and(|until| at > until) {
                    self.done = true;
                    return None;
                }
                if self.rule.count.is_some_and(|count| self.emitted >= count) {
                    self.done = true;
                    return None;
                }
                self.emitted += 1;
                return Some(at);
            }
            let Some(mut set) = self.rule.period_set(self.start, self.period) else {
                self.done = true;
                return None;
            };
            self.period += 1;
            set.retain(|at| *at >= self.start);
            if set.is_empty() {
                self.empty += 1;
                if self.empty >= max_empty_periods(self.rule.freq) {
                    self.done = true;
                }
                continue;
            }
            self.empty = 0;
            set.reverse();
            self.pending = set;
        }
    }
}

// The number of days in a year.
fn year_days(year: i16) -> i16 {
    if Date::new(year, 12, 31)
        .map(|d| d.day_of_year())
        .unwrap_or(365)
        == 366
    {
        366
    } else {
        365
    }
}

impl Rule {
    // period_set is the sorted, deduplicated set of a period's occurrences
    // (BYSETPOS applied), the period the n-th from the start's; None once
    // a period lies past the calendar's end.
    fn period_set(&self, start: DateTime, n: i64) -> Option<Vec<DateTime>> {
        let step = n.checked_mul(self.interval)?;
        let (days, hour): (Vec<Date>, Option<i8>) = match self.freq {
            Freq::Yearly => {
                let year = i64::from(start.year()).checked_add(step)?;
                let year = i16::try_from(year)
                    .ok()
                    .filter(|y| (1..=9998).contains(y))?;
                let first = Date::new(year, 1, 1).ok()?;
                (
                    (0..year_days(year))
                        .filter_map(|d| first.checked_add(i64::from(d).days()).ok())
                        .collect(),
                    None,
                )
            }
            Freq::Monthly => {
                let first = start
                    .date()
                    .first_of_month()
                    .checked_add(step.months())
                    .ok()?;
                if first.year() > 9998 {
                    return None;
                }
                (
                    (0..first.days_in_month())
                        .filter_map(|d| first.checked_add(i64::from(d).days()).ok())
                        .collect(),
                    None,
                )
            }
            Freq::Weekly => {
                let back = (start.date().weekday().to_monday_zero_offset()
                    - self.week_start.to_monday_zero_offset())
                .rem_euclid(7);
                let first = start
                    .date()
                    .checked_sub(i64::from(back).days())
                    .ok()?
                    .checked_add(step.checked_mul(7)?.days())
                    .ok()?;
                if first.year() > 9998 {
                    return None;
                }
                (
                    (0..7)
                        .filter_map(|d| first.checked_add(i64::from(d).days()).ok())
                        .collect(),
                    None,
                )
            }
            Freq::Daily => {
                let day = start.date().checked_add(step.days()).ok()?;
                if day.year() > 9998 {
                    return None;
                }
                (vec![day], None)
            }
            Freq::Hourly => {
                let at = start.checked_add(step.hours()).ok()?;
                if at.year() > 9998 {
                    return None;
                }
                (vec![at.date()], Some(at.hour()))
            }
        };
        let mut set: Vec<DateTime> = Vec::new();
        for day in days.into_iter().filter(|day| self.day_matches(*day, start)) {
            for time in self.times(start, hour) {
                set.push(day.to_datetime(time));
            }
        }
        set.sort();
        set.dedup();
        if !self.by_set_pos.is_empty() {
            let len = set.len() as i32;
            let mut picked: Vec<DateTime> = self
                .by_set_pos
                .iter()
                .filter_map(|pos| {
                    let index = if *pos > 0 { pos - 1 } else { len + pos };
                    (0..len).contains(&index).then(|| set[index as usize])
                })
                .collect();
            picked.sort();
            picked.dedup();
            set = picked;
        }
        Some(set)
    }

    // times are a day's times of the period: an HOURLY period's one hour,
    // held to BYHOUR, else BYHOUR's hours; BYMINUTE's and BYSECOND's, the
    // start's when absent.
    fn times(&self, start: DateTime, hour: Option<i8>) -> Vec<Time> {
        let hours: Vec<i8> = match hour {
            Some(h) => {
                if self.by_hour.is_empty() || self.by_hour.contains(&h) {
                    vec![h]
                } else {
                    vec![]
                }
            }
            None if self.by_hour.is_empty() => vec![start.hour()],
            None => self.by_hour.clone(),
        };
        let minutes = if self.by_minute.is_empty() {
            vec![start.minute()]
        } else {
            self.by_minute.clone()
        };
        let seconds = if self.by_second.is_empty() {
            vec![start.second()]
        } else {
            self.by_second.clone()
        };
        let mut times = Vec::new();
        for h in &hours {
            for m in &minutes {
                for s in &seconds {
                    if let Ok(time) = Time::new(*h, *m, *s, 0) {
                        times.push(time);
                    }
                }
            }
        }
        times
    }

    // day_matches holds a day to every BY rule of a day, with the start's
    // month and day (YEARLY), day (MONTHLY) or weekday (WEEKLY) when the
    // rule gives none of BYWEEKNO, BYYEARDAY, BYMONTHDAY and BYDAY.
    fn day_matches(&self, day: Date, start: DateTime) -> bool {
        let no_day_rule = self.by_week_no.is_empty()
            && self.by_year_day.is_empty()
            && self.by_month_day.is_empty()
            && self.by_day.is_empty();
        let by_month: Vec<i8> =
            if self.by_month.is_empty() && no_day_rule && self.freq == Freq::Yearly {
                vec![start.month()]
            } else {
                self.by_month.clone()
            };
        if !by_month.is_empty() && !by_month.contains(&day.month()) {
            return false;
        }
        let by_month_day: Vec<i8> =
            if no_day_rule && matches!(self.freq, Freq::Yearly | Freq::Monthly) {
                vec![start.day()]
            } else {
                self.by_month_day.clone()
            };
        if !by_month_day.is_empty() {
            let last = day.days_in_month();
            if !by_month_day.iter().any(|d| {
                if *d > 0 {
                    *d == day.day()
                } else {
                    last + 1 + *d == day.day()
                }
            }) {
                return false;
            }
        }
        if !self.by_year_day.is_empty() {
            let total = year_days(day.year());
            let of_year = day.day_of_year();
            if !self.by_year_day.iter().any(|d| {
                if *d > 0 {
                    *d == of_year
                } else {
                    total + 1 + *d == of_year
                }
            }) {
                return false;
            }
        }
        if !self.by_week_no.is_empty() && !self.week_no_matches(day) {
            return false;
        }
        let by_day: Vec<(i8, Weekday)> = if no_day_rule && self.freq == Freq::Weekly {
            vec![(0, start.date().weekday())]
        } else {
            self.by_day.clone()
        };
        if !by_day.is_empty()
            && !by_day
                .iter()
                .any(|(n, wd)| self.weekday_matches(day, *n, *wd))
        {
            return false;
        }
        true
    }

    // weekday_matches holds a day to a BYDAY entry: its weekday, and an
    // ordinal counted within the month (MONTHLY, or YEARLY with BYMONTH)
    // or the year.
    fn weekday_matches(&self, day: Date, ordinal: i8, wd: Weekday) -> bool {
        if day.weekday() != wd {
            return false;
        }
        if ordinal == 0 {
            return true;
        }
        let in_month = self.freq == Freq::Monthly || !self.by_month.is_empty();
        let (index, total) = if in_month {
            let index = (day.day() - 1) / 7 + 1;
            let total = index + (day.days_in_month() - day.day()) / 7;
            (i16::from(index), i16::from(total))
        } else {
            let of_year = day.day_of_year();
            let index = (of_year - 1) / 7 + 1;
            let total = index + (year_days(day.year()) - of_year) / 7;
            (index, total)
        };
        let ordinal = i16::from(ordinal);
        if ordinal > 0 {
            index == ordinal
        } else {
            total + 1 + ordinal == index
        }
    }

    // week_no_matches holds a day to BYWEEKNO: its week of the year, weeks
    // starting on WKST and week 1 the first with at least four days of the
    // year; a day in the last days of December may be week 1 of the next
    // year and one in the first days of January the last week of the year
    // before, and each counts for the year its period is.
    fn week_no_matches(&self, day: Date) -> bool {
        let ws = self.week_start.to_monday_zero_offset();
        let week_one_start = |year: i16| -> Option<Date> {
            let jan1 = Date::new(year, 1, 1).ok()?;
            let back = (jan1.weekday().to_monday_zero_offset() - ws).rem_euclid(7);
            let start = jan1.checked_sub(i64::from(back).days()).ok()?;
            // Week 1 holds at least four days of the year.
            if 7 - back >= 4 {
                Some(start)
            } else {
                start.checked_add(7.days()).ok()
            }
        };
        let year = day.year();
        let Some(this) = week_one_start(year) else {
            return false;
        };
        let Some(next) = week_one_start(year + 1) else {
            return false;
        };
        let weeks_between = |from: Date, to: Date| {
            i16::try_from(from.until(to).map(|s: Span| s.get_days()).unwrap_or(364) / 7)
                .unwrap_or(52)
        };
        let weeks = weeks_between(this, next);
        let number = if day < this {
            // The last week of the year before: it is week -1, and week 52
            // or 53 by the year before's count, as dateutil and the rrule
            // crate read it.
            let Some(last) = week_one_start(year - 1) else {
                return false;
            };
            let before = weeks_between(last, this);
            return self
                .by_week_no
                .iter()
                .any(|w| *w == -1 || i16::from(*w) == before);
        } else if day >= next {
            // Week 1 of the next year, within this year's days.
            1
        } else {
            i16::try_from(this.until(day).map(|s: Span| s.get_days()).unwrap_or(0) / 7).unwrap_or(0)
                + 1
        };
        self.by_week_no.iter().any(|w| {
            if *w > 0 {
                i16::from(*w) == number
            } else {
                weeks + 1 + i16::from(*w) == number
            }
        })
    }
}
