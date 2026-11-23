//! Cron lines and time zones (D104): what Schedules reads a cron line's runs
//! with, in a zone of the IANA database jiff bundles, and nothing else; it
//! reads no clock (a time is the command's) and no host zone file.
//!
//! A line is Vixie cron's five fields, minute (0-59), hour (0-23),
//! day-of-month (1-31), month (1-12, or JAN to DEC) and day-of-week (0-7, 0
//! and 7 Sunday, or SUN to SAT), names in any case. Each field is a list of
//! items, each `*`, a number, a range `a-b` that runs up, or a step `*/s` or
//! `a-b/s` from 1 to the field's count of values, a name standing for a
//! number. A day field whose text begins with `*` restricts nothing; when
//! both day fields restrict, a day runs when either matches, as Vixie cron
//! has it, else when both do. A line may instead be a macro, the whole line,
//! in any case: `@yearly` and `@annually` (`0 0 1 1 *`), `@monthly`
//! (`0 0 1 * *`), `@weekly` (`0 0 * * 0`), `@daily` and `@midnight`
//! (`0 0 * * *`), and `@hourly` (`0 * * * *`). A seconds field, `@reboot`,
//! and the extensions no two crons agree on (`?`, `L`, `W`, `#`, `H`) are
//! refused, each in words that name the field and what breaks.
//!
//! A line's next run after an instant is read in a zone: its local dates
//! from the instant's, at most [`HORIZON_DAYS`] of them, each date a line's
//! day names at each time of day it names, ascending, the first one whose
//! instant is after the given one. A local time a daylight-saving change
//! skips runs at the gap's first instant, the change itself, so a run is not
//! lost and several skipped times run once; one it repeats runs once, at its
//! first occurrence, and the repeated hour runs nothing. A civil time's
//! instant never decreases as the civil time grows, so the first matching
//! civil time after the instant is the least such instant.

use jiff::civil::{Date, DateTime};
use jiff::tz::{AmbiguousOffset, TimeZone, TimeZoneDatabase};
use jiff::Timestamp;

/// How far ahead a cron line's next run is looked for: ten years of local
/// dates, which every line with a run holds (a leap day's runs are at most
/// eight years apart).
pub const HORIZON_DAYS: i64 = 3653;

/// The macros a line may be, each with the line it reads as, in the order a
/// refusal lists them.
const MACROS: [(&str, &str); 7] = [
    ("@yearly", "0 0 1 1 *"),
    ("@annually", "0 0 1 1 *"),
    ("@monthly", "0 0 1 * *"),
    ("@weekly", "0 0 * * 0"),
    ("@daily", "0 0 * * *"),
    ("@midnight", "0 0 * * *"),
    ("@hourly", "0 * * * *"),
];

/// The months' and the days' names, in their numbers' order: JAN is 1, SUN
/// is 0.
const MONTHS: [&str; 12] = [
    "JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC",
];
const DAYS: [&str; 7] = ["SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"];

/// An IANA time zone of the bundled database.
#[derive(Clone, Debug)]
pub struct Zone {
    tz: TimeZone,
    name: String,
}

impl PartialEq for Zone {
    // Two zones are one when the database names them alike.
    fn eq(&self, other: &Zone) -> bool {
        self.name == other.name
    }
}

impl Eq for Zone {}

impl Zone {
    /// The zone of a name, matched as the database matches it, ignoring ASCII
    /// case; `Err` with `"<name>" is not a time zone of the IANA database the
    /// core bundles`. The name is read in the bundled database alone, never
    /// jiff's global one (`TimeZone::get`), which prefers the host's zone
    /// files (`TZDIR`, `/usr/share/zoneinfo`) whenever a crate of the build
    /// turns on jiff's `tzdb-zoneinfo`, as a project's Cargo graph may by
    /// feature unification.
    pub fn named(name: &str) -> Result<Zone, String> {
        let refused =
            || format!("\"{name}\" is not a time zone of the IANA database the core bundles");
        let tz = TimeZoneDatabase::bundled()
            .get(name)
            .map_err(|_| refused())?;
        let Some(canonical) = tz.iana_name().map(str::to_owned) else {
            return Err(refused());
        };
        Ok(Zone {
            tz,
            name: canonical,
        })
    }

    pub fn utc() -> Zone {
        Zone {
            tz: TimeZone::UTC,
            name: "UTC".to_owned(),
        }
    }

    /// Its name as the database spells it (`TimeZone::iana_name`).
    pub fn name(&self) -> &str {
        &self.name
    }

    // instant is a civil time's instant in the zone: its one instant; in a
    // gap a change skips, the gap's first instant, the change itself; in a
    // fold a change repeats, its first occurrence. None past the range the
    // database answers. A time in a gap, read at the offset after the
    // change, lies before the change and after the one before it, so the
    // change is the first transition following it; read at the offset
    // before, the gap's first time is the change itself, which jiff's
    // `preceding` (strictly before) would pass over for the change before.
    fn instant(&self, civil: DateTime) -> Option<Timestamp> {
        let ambiguous = self.tz.to_ambiguous_timestamp(civil);
        match ambiguous.offset() {
            AmbiguousOffset::Gap { after, .. } => {
                let early = after.to_timestamp(civil).ok()?;
                self.tz
                    .following(early)
                    .next()
                    .map(|transition| transition.timestamp())
            }
            AmbiguousOffset::Fold { .. } => ambiguous.earlier().ok(),
            _ => ambiguous.compatible().ok(),
        }
    }
}

/// A parsed cron line.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Cron {
    /// The minutes, hours, days of the month, months and days of the week
    /// it names, a bit each: minute m is bit m, day d bit d, month m bit m,
    /// Sunday bit 0.
    minutes: u64,
    hours: u32,
    days: u32,
    months: u16,
    weekdays: u8,
    /// Whether each day field restricts the days (its text does not begin
    /// with `*`).
    days_restricted: bool,
    weekdays_restricted: bool,
    line: String,
}

/// One of a line's five fields: its name, its values' bounds, and whether
/// it takes names.
#[derive(Clone, Copy)]
struct Field {
    name: &'static str,
    min: u32,
    max: u32,
    /// How many values it has, the greatest step it takes.
    count: u32,
    names: Option<&'static [&'static str]>,
    /// The number its first name stands for.
    first_named: u32,
}

const FIELDS: [Field; 5] = [
    Field {
        name: "minute",
        min: 0,
        max: 59,
        count: 60,
        names: None,
        first_named: 0,
    },
    Field {
        name: "hour",
        min: 0,
        max: 23,
        count: 24,
        names: None,
        first_named: 0,
    },
    Field {
        name: "day-of-month",
        min: 1,
        max: 31,
        count: 31,
        names: None,
        first_named: 0,
    },
    Field {
        name: "month",
        min: 1,
        max: 12,
        count: 12,
        names: Some(&MONTHS),
        first_named: 1,
    },
    Field {
        name: "day-of-week",
        min: 0,
        max: 7,
        count: 7,
        names: Some(&DAYS),
        first_named: 0,
    },
];

impl Cron {
    /// Parses a line; `Err` with the refusal's message.
    pub fn parse(line: &str) -> Result<Cron, String> {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            return Err("a cron line names five fields, minute hour day-of-month month day-of-week, and this one is empty".to_owned());
        }
        if trimmed.starts_with('@') {
            let lowered = trimmed.to_ascii_lowercase();
            if lowered == "@reboot" {
                return Err(
                    "@reboot runs when a host starts, which a schedule never does".to_owned(),
                );
            }
            let Some((_, expanded)) = MACROS.iter().find(|(name, _)| *name == lowered) else {
                let listed: Vec<&str> = MACROS.iter().map(|(name, _)| *name).collect();
                return Err(format!(
                    "\"{trimmed}\" is no macro (the macros: {})",
                    listed.join(", ")
                ));
            };
            let mut cron = Cron::fields(expanded)?;
            cron.line = lowered;
            return Ok(cron);
        }
        let mut cron = Cron::fields(trimmed)?;
        cron.line = trimmed
            .split_ascii_whitespace()
            .collect::<Vec<_>>()
            .join(" ");
        Ok(cron)
    }

    // fields reads a line of five fields.
    fn fields(line: &str) -> Result<Cron, String> {
        let fields: Vec<&str> = line.split_ascii_whitespace().collect();
        if fields.len() != 5 {
            let mut message = format!(
                "a cron line names five fields, minute hour day-of-month month day-of-week, and \"{line}\" names {}",
                fields.len()
            );
            if fields.len() == 6 {
                message.push_str(": a seconds field is not taken");
            }
            return Err(message);
        }
        let mut bits = [0u64; 5];
        for (at, (text, field)) in fields.iter().zip(FIELDS).enumerate() {
            bits[at] = field.read(text)?;
        }
        // Sunday is 0 and 7.
        let weekdays = (bits[4] | (bits[4] >> 7)) & 0x7f;
        Ok(Cron {
            minutes: bits[0],
            hours: bits[1] as u32,
            days: bits[2] as u32,
            months: bits[3] as u16,
            weekdays: weekdays as u8,
            days_restricted: !fields[2].starts_with('*'),
            weekdays_restricted: !fields[4].starts_with('*'),
            line: line.to_owned(),
        })
    }

    /// The line normalized: trimmed, its fields one space apart, a macro in
    /// lowercase; names and numbers as written.
    pub fn line(&self) -> &str {
        &self.line
    }

    /// The first run strictly after `after_ms` in `zone`; `None` when none
    /// falls within HORIZON_DAYS local dates of `after_ms`'s.
    pub fn next_after(&self, zone: &Zone, after_ms: i64) -> Option<i64> {
        let after = Timestamp::from_millisecond(after_ms).ok()?;
        let local = zone.tz.to_datetime(after);
        let mut date: Date = local.date();
        let times = self.times();
        for day in 0..HORIZON_DAYS {
            if self.runs_on(date) {
                for &(hour, minute) in &times {
                    // A civil time before the instant's own is no later
                    // instant, so the first date's earlier times are passed
                    // over.
                    if day == 0 && (hour, minute) < (local.hour(), local.minute()) {
                        continue;
                    }
                    let civil = date.at(hour, minute, 0, 0);
                    let Some(instant) = zone.instant(civil) else {
                        continue;
                    };
                    if instant > after {
                        return Some(instant.as_millisecond());
                    }
                }
            }
            date = date.tomorrow().ok()?;
        }
        None
    }

    /// The least gap between two consecutive runs of the line on the local
    /// clock, in milliseconds: two of its times of day on a date it runs on,
    /// or a date's last time and the first of the next date it runs on, its
    /// days of the month, months and days of the week read over the
    /// Gregorian calendar's cycle of 400 years, after which every date falls
    /// on its weekday again (`@weekly` a week, `@monthly` 28 days, a line of
    /// one time a day a day); `i64::MAX` for a line that never runs.
    pub fn closest_ms(&self) -> i64 {
        let minutes: Vec<i64> = self
            .times()
            .iter()
            .map(|&(hour, minute)| i64::from(hour) * 60 + i64::from(minute))
            .collect();
        let (Some(first), Some(last)) = (minutes.first(), minutes.last()) else {
            return i64::MAX;
        };
        let Some(days) = self.closest_days() else {
            return i64::MAX;
        };
        let across = days * 1440 + first - last;
        let least = minutes
            .windows(2)
            .map(|pair| pair[1] - pair[0])
            .fold(across, i64::min);
        least * 60_000
    }

    // closest_days is the least count of days between two consecutive dates
    // the line runs on, over the 400 years from 2000-01-01 and around their
    // end; None when it runs on none. A month the line leaves out is passed
    // over whole, and the scan stops at a day apart, the least there is.
    fn closest_days(&self) -> Option<i64> {
        // The cycle's days: 400 years of 365 days, and 97 leap days.
        const CYCLE: i64 = 146_097;
        let (mut day, mut first, mut last, mut least) = (0i64, None, None::<i64>, None::<i64>);
        for year in 2000..2400 {
            for month in 1..=12 {
                let start = Date::new(year, month, 1).ok()?;
                let length = i64::from(start.days_in_month());
                if self.months & (1 << month) != 0 {
                    let weekday = i64::from(start.weekday().to_sunday_zero_offset());
                    for date in 1..=length {
                        let on = (weekday + date - 1) % 7;
                        if !self.runs_on_day(date as u32, on as u32) {
                            continue;
                        }
                        let at = day + date - 1;
                        if let Some(last) = last {
                            least = Some(least.map_or(at - last, |least| least.min(at - last)));
                        }
                        first.get_or_insert(at);
                        last = Some(at);
                        if least == Some(1) {
                            return least;
                        }
                    }
                }
                day += length;
            }
        }
        let around = first? + CYCLE - last?;
        Some(least.map_or(around, |least| least.min(around)))
    }

    // times is the line's times of day, ascending.
    fn times(&self) -> Vec<(i8, i8)> {
        let mut times = Vec::new();
        for hour in 0..24 {
            if self.hours & (1 << hour) == 0 {
                continue;
            }
            for minute in 0..60 {
                if self.minutes & (1 << minute) != 0 {
                    times.push((hour as i8, minute as i8));
                }
            }
        }
        times
    }

    // runs_on says whether a local date is one the line runs on: its month
    // matches, and its day by Vixie cron's rule.
    fn runs_on(&self, date: Date) -> bool {
        self.months & (1 << date.month()) != 0
            && self.runs_on_day(
                date.day() as u32,
                date.weekday().to_sunday_zero_offset() as u32,
            )
    }

    // runs_on_day says whether a day of the month, falling on a day of the
    // week (Sunday 0), is one the line runs on, by Vixie cron's rule: either
    // day field when both restrict, else both.
    fn runs_on_day(&self, day: u32, weekday: u32) -> bool {
        let by_day = self.days & (1 << day) != 0;
        let by_weekday = self.weekdays & (1 << weekday) != 0;
        if self.days_restricted && self.weekdays_restricted {
            by_day || by_weekday
        } else {
            by_day && by_weekday
        }
    }
}

impl Field {
    // read reads a field's text as the bits of the values it names.
    fn read(&self, text: &str) -> Result<u64, String> {
        if let Some(c) = text
            .chars()
            .find(|c| !(c.is_ascii_alphanumeric() || matches!(c, '*' | ',' | '-' | '/')))
        {
            return Err(format!(
                "the {} field \"{text}\" holds {c}, which cron's five fields do not take: a field takes *, numbers, names of months and days, ranges (a-b), steps (*/n, a-b/n) and lists (a,b)",
                self.name
            ));
        }
        let mut bits = 0u64;
        for item in text.split(',') {
            if item.is_empty() {
                return Err(format!(
                    "the {} field \"{text}\" has an empty item",
                    self.name
                ));
            }
            bits |= self.item(text, item)?;
        }
        Ok(bits)
    }

    // item reads one item of a field's list: `*`, a value, a range, or
    // either of the first and the last stepped.
    fn item(&self, text: &str, item: &str) -> Result<u64, String> {
        let (base, step) = match item.split_once('/') {
            Some((base, step)) => (base, Some(step)),
            None => (item, None),
        };
        let (low, high, single) = if base == "*" {
            (self.min, self.top(), false)
        } else {
            match base.split_once('-') {
                Some((a, b)) => {
                    // A range with no end is a number outside the bounds
                    // (`-5`), as it reads.
                    if a.is_empty() || b.is_empty() {
                        return Err(self.outside(text, base));
                    }
                    let (low, high) = (self.value(text, a)?, self.value(text, b)?);
                    if low > high {
                        return Err(format!(
                            "the {} field \"{text}\" holds the range {a}-{b}, which runs down: a range runs up",
                            self.name
                        ));
                    }
                    (low, high, false)
                }
                None => {
                    let value = self.value(text, base)?;
                    (value, value, true)
                }
            }
        };
        let by = match step {
            None => 1,
            Some(step) => {
                if single {
                    return Err(format!(
                        "the {} field \"{text}\" steps from {base}: a step follows * or a range ({base}-{}/{step})",
                        self.name, self.max
                    ));
                }
                match step.parse::<u32>() {
                    Ok(by) if (1..=self.count).contains(&by) && step.bytes().all(|b| b.is_ascii_digit()) => by,
                    _ => {
                        return Err(format!(
                            "the {} field \"{text}\" steps by {step}: a step is a whole number from 1 to {}",
                            self.name, self.count
                        ))
                    }
                }
            }
        };
        let mut bits = 0u64;
        let mut value = low;
        while value <= high {
            bits |= 1 << value;
            value += by;
        }
        Ok(bits)
    }

    // top is the greatest value `*` stands for: Saturday for the days of the
    // week, whose 7 is Sunday again.
    fn top(&self) -> u32 {
        if self.name == "day-of-week" {
            6
        } else {
            self.max
        }
    }

    // value reads a number or a name of the field, within its bounds.
    fn value(&self, text: &str, value: &str) -> Result<u32, String> {
        if value.bytes().any(|b| b.is_ascii_alphabetic()) {
            let Some(names) = self.names else {
                return Err(format!(
                    "the {} field \"{text}\" holds the name {value}: only the month and day-of-week fields take names",
                    self.name
                ));
            };
            let upper = value.to_ascii_uppercase();
            return match names.iter().position(|name| *name == upper) {
                Some(at) => Ok(self.first_named + at as u32),
                None if self.name == "month" => Err(format!(
                    "the month field \"{text}\" holds {value}, which is no month's name (JAN to DEC)"
                )),
                None => Err(format!(
                    "the day-of-week field \"{text}\" holds {value}, which is no day's name (SUN to SAT)"
                )),
            };
        }
        let number: u32 = value
            .parse()
            .ok()
            .filter(|_| value.bytes().all(|b| b.is_ascii_digit()))
            .ok_or_else(|| self.outside(text, value))?;
        if !(self.min..=self.max).contains(&number) {
            return Err(self.outside(text, value));
        }
        Ok(number)
    }

    // outside is the refusal of a value outside the field's bounds.
    fn outside(&self, text: &str, value: &str) -> String {
        format!(
            "the {} field \"{text}\" holds {value}, outside {}-{}",
            self.name, self.min, self.max
        )
    }
}
