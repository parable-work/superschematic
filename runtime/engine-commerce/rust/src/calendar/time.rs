//! Calendar's time (D70): weekly opening hours as wall times in an IANA
//! time zone, made instants through `jiff` and the time zone database it
//! bundles, so every server built from one lock file computes the same
//! slots, whatever zone files its host has.
//!
//! A day's range `[open, close)` is the set of instants whose wall time on
//! that date lies in it. The zone's offsets over a window are cut into
//! pieces of one offset each ([`Offsets`]); a wall time `c` is the instant
//! `c - offset` of each piece that holds it. So a wall time a daylight-saving
//! change skips is no instant, and one it repeats is two: a range over a
//! spring-forward gap is that much shorter, and one over a fall-back fold
//! that much longer, both of the repeated hour's passes open.
//!
//! The start grid is in wall time too: a hold starts a whole number of
//! slots after the opening time of the range its start lies in, so slots
//! keep the wall times a reader expects (`09:00`, `09:15`) on either side
//! of a change, and a grid time the change skips has no slot.
//!
//! Times are epoch milliseconds throughout. A wall time is held the same
//! way, as the milliseconds since 1970-01-01T00:00 of the civil date and
//! time read as if it were UTC, so a date is `wall.div_euclid(DAY_MS)`.

use jiff::tz::TimeZone;
use jiff::Timestamp;
use serde_json::Value;

use crate::supply::Interval;

pub const MINUTE_MS: i64 = 60_000;
pub const HOUR_MS: i64 = 60 * MINUTE_MS;
pub const DAY_MS: i64 = 24 * HOUR_MS;

/// The days of the week as the config names them, Monday first.
pub const DAYS: [&str; 7] = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];

/// The earliest instant Calendar opens, 1900-01-01T00:00Z, and the
/// instant after the latest, 9000-01-01T00:00Z, well inside what the time
/// zone database answers: the contract's bounds on an interval.
pub use crate::supply::{EARLIEST_MS, LATEST_MS};

/// How far past a window the zone's offsets are read, so that every wall
/// date the window reaches, at any offset, is covered.
const MARGIN_MS: i64 = 3 * DAY_MS;

/// One range of a day's opening hours, in milliseconds after midnight:
/// from `open` to `close`, which may be 24:00.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Range {
    pub open: i64,
    pub close: i64,
}

/// Opening hours by day of the week, Monday first, each day's ranges in
/// order and none overlapping.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WeekHours {
    pub days: [Vec<Range>; 7],
}

/// Why hours are refused: a JSON Pointer below the hours and a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HoursProblem {
    pub path: String,
    pub message: String,
}

/// A wall time, `HH:MM` from 00:00 to 24:00, in milliseconds after
/// midnight.
pub fn wall_time(text: &str) -> Option<i64> {
    let bytes = text.as_bytes();
    if bytes.len() != 5 || bytes[2] != b':' {
        return None;
    }
    let digits = |at: usize| -> Option<i64> {
        let pair = &bytes[at..at + 2];
        pair.iter()
            .all(u8::is_ascii_digit)
            .then(|| i64::from(pair[0] - b'0') * 10 + i64::from(pair[1] - b'0'))
    };
    let (hour, minute) = (digits(0)?, digits(3)?);
    if minute > 59 || hour > 24 || (hour == 24 && minute != 0) {
        return None;
    }
    Some(hour * HOUR_MS + minute * MINUTE_MS)
}

/// A time of day as `HH:MM`.
pub fn wall_text(ms: i64) -> String {
    format!("{:02}:{:02}", ms / HOUR_MS, ms % HOUR_MS / MINUTE_MS)
}

impl WeekHours {
    /// Reads hours as the config and setOpeningHours give them: by day,
    /// each a list of `[open, close]` wall times. A range must close after
    /// it opens, and a day's ranges must not overlap; ranges that touch
    /// are one.
    pub fn parse(value: &Value) -> Result<WeekHours, HoursProblem> {
        let problem = |path: String, message: &str| HoursProblem {
            path,
            message: message.to_owned(),
        };
        let Some(members) = value.as_object() else {
            return Err(problem(String::new(), "must be an object of days"));
        };
        let mut hours = WeekHours::default();
        for (day, ranges) in members {
            let Some(index) = DAYS.iter().position(|known| known == day) else {
                return Err(problem(format!("/{day}"), "is not a day: mon to sun"));
            };
            let Some(ranges) = ranges.as_array() else {
                return Err(problem(format!("/{day}"), "must be a list of ranges"));
            };
            let mut parsed = Vec::new();
            for (at, range) in ranges.iter().enumerate() {
                let path = format!("/{day}/{at}");
                let pair = range.as_array().filter(|pair| pair.len() == 2);
                let times = pair.and_then(|pair| {
                    Some((wall_time(pair[0].as_str()?)?, wall_time(pair[1].as_str()?)?))
                });
                let Some((open, close)) = times else {
                    return Err(problem(
                        path,
                        "must be [open, close], each HH:MM from 00:00 to 24:00",
                    ));
                };
                if close <= open {
                    return Err(problem(path, "must close after it opens"));
                }
                parsed.push((at, Range { open, close }));
            }
            parsed.sort_by_key(|(_, range)| range.open);
            for pair in parsed.windows(2) {
                let ((_, before), (at, after)) = (pair[0], pair[1]);
                if after.open < before.close {
                    return Err(problem(
                        format!("/{day}/{at}"),
                        &format!(
                            "overlaps the range {}-{} of the same day",
                            wall_text(before.open),
                            wall_text(before.close)
                        ),
                    ));
                }
            }
            hours.days[index] = parsed.into_iter().map(|(_, range)| range).collect();
        }
        Ok(hours)
    }

    /// Whether no day opens.
    pub fn is_closed(&self) -> bool {
        self.days.iter().all(Vec::is_empty)
    }
}

/// The time zone a name names in the bundled database; `None` for a name
/// it does not hold, and for `Etc/Unknown`, which names no zone.
pub fn zone(name: &str) -> Option<TimeZone> {
    if name.eq_ignore_ascii_case("Etc/Unknown") {
        return None;
    }
    TimeZone::get(name).ok()
}

/// Which day of the week a wall date is, Monday 0 (1970-01-01, day 0, was
/// a Thursday).
pub fn weekday(day: i64) -> usize {
    (day + 3).rem_euclid(7) as usize
}

fn timestamp(ms: i64) -> Timestamp {
    Timestamp::from_millisecond(ms.clamp(EARLIEST_MS - MARGIN_MS, LATEST_MS + MARGIN_MS))
        .unwrap_or(Timestamp::UNIX_EPOCH)
}

/// A stretch of instants over which a zone keeps one offset.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Piece {
    start: i64,
    end: i64,
    offset: i64,
}

/// A zone's offsets over a window of instants, cut where they change.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Offsets {
    pieces: Vec<Piece>,
}

impl Offsets {
    /// The zone's offsets from `start` to `end`, and a margin either side.
    pub fn over(tz: &TimeZone, start: i64, end: i64) -> Offsets {
        let start = start.saturating_sub(MARGIN_MS).max(EARLIEST_MS - MARGIN_MS);
        let end = end.saturating_add(MARGIN_MS).min(LATEST_MS + MARGIN_MS);
        let mut pieces = Vec::new();
        let mut at = start;
        let mut offset = i64::from(tz.to_offset(timestamp(start)).seconds()) * 1000;
        for transition in tz.following(timestamp(start)) {
            let moment = transition.timestamp().as_millisecond();
            if moment >= end {
                break;
            }
            if moment > at {
                pieces.push(Piece {
                    start: at,
                    end: moment,
                    offset,
                });
                at = moment;
            }
            offset = i64::from(transition.offset().seconds()) * 1000;
        }
        if end > at {
            pieces.push(Piece {
                start: at,
                end,
                offset,
            });
        }
        Offsets { pieces }
    }

    /// The wall time at an instant the window covers.
    pub fn wall(&self, instant: i64) -> i64 {
        let piece = self
            .pieces
            .iter()
            .find(|piece| piece.start <= instant && instant < piece.end)
            .or_else(|| self.pieces.last());
        instant + piece.map_or(0, |piece| piece.offset)
    }

    /// The instants whose wall time lies from `start` to `end`, in order:
    /// none over a gap, two stretches over a fold.
    pub fn instants(&self, start: i64, end: i64) -> Vec<Interval> {
        let mut out = Vec::new();
        for piece in &self.pieces {
            let from = piece.start.max(start - piece.offset);
            let to = piece.end.min(end - piece.offset);
            if from < to {
                out.push(Interval {
                    start_ms: from,
                    end_ms: to,
                });
            }
        }
        merge(out)
    }

    /// The instants a wall time is: none in a gap, two in a fold.
    pub fn instants_at(&self, wall: i64) -> Vec<i64> {
        self.pieces
            .iter()
            .map(|piece| wall - piece.offset)
            .zip(&self.pieces)
            .filter(|(instant, piece)| piece.start <= *instant && *instant < piece.end)
            .map(|(instant, _)| instant)
            .collect()
    }

    /// The wall dates the window reaches, first and last.
    fn days(&self, start: i64, end: i64) -> (i64, i64) {
        (
            self.wall(start).div_euclid(DAY_MS) - 1,
            self.wall(end).div_euclid(DAY_MS) + 1,
        )
    }
}

/// Intervals sorted, and those that overlap or touch joined.
pub fn merge(mut intervals: Vec<Interval>) -> Vec<Interval> {
    intervals.sort_by_key(|interval| (interval.start_ms, interval.end_ms));
    let mut out: Vec<Interval> = Vec::with_capacity(intervals.len());
    for interval in intervals {
        match out.last_mut() {
            Some(last) if interval.start_ms <= last.end_ms => {
                last.end_ms = last.end_ms.max(interval.end_ms);
            }
            _ => out.push(interval),
        }
    }
    out
}

/// The instants open from `start` to `end`, and around them, by the hours:
/// sorted and joined, so a hold is inside the hours when one of them holds
/// it.
pub fn opening(hours: &WeekHours, offsets: &Offsets, start: i64, end: i64) -> Vec<Interval> {
    let (first, last) = offsets.days(start, end);
    let mut out = Vec::new();
    for day in first..=last {
        for range in &hours.days[weekday(day)] {
            let base = day * DAY_MS;
            out.extend(offsets.instants(base + range.open, base + range.close));
        }
    }
    let bounded = Interval {
        start_ms: EARLIEST_MS,
        end_ms: LATEST_MS,
    };
    merge(out)
        .into_iter()
        .filter_map(|interval| {
            let from = interval.start_ms.max(bounded.start_ms);
            let to = interval.end_ms.min(bounded.end_ms);
            (from < to).then_some(Interval {
                start_ms: from,
                end_ms: to,
            })
        })
        .collect()
}

/// Whether one of the open stretches holds the interval.
pub fn inside(open: &[Interval], asked: &Interval) -> bool {
    let at = open.partition_point(|interval| interval.end_ms < asked.end_ms);
    open.get(at).is_some_and(|interval| {
        interval.start_ms <= asked.start_ms && asked.end_ms <= interval.end_ms
    })
}

/// Whether an instant starts on the grid: a whole number of `slot_ms`
/// after the opening time of the day's range its wall time lies in.
pub fn on_grid(hours: &WeekHours, offsets: &Offsets, start: i64, slot_ms: i64) -> bool {
    let wall = offsets.wall(start);
    let day = wall.div_euclid(DAY_MS);
    let time = wall - day * DAY_MS;
    hours.days[weekday(day)]
        .iter()
        .find(|range| range.open <= time && time < range.close)
        .is_some_and(|range| (time - range.open) % slot_ms == 0)
}

/// Every start on the grid from `start` to `end`, soonest first: each
/// grid wall time of each range, as the instants it is.
pub fn grid(hours: &WeekHours, offsets: &Offsets, start: i64, end: i64, slot_ms: i64) -> Vec<i64> {
    let (first, last) = offsets.days(start, end);
    let mut out = Vec::new();
    for day in first..=last {
        for range in &hours.days[weekday(day)] {
            let base = day * DAY_MS;
            let mut time = range.open;
            while time < range.close {
                out.extend(
                    offsets
                        .instants_at(base + time)
                        .into_iter()
                        .filter(|instant| (start..end).contains(instant)),
                );
                time += slot_ms;
            }
        }
    }
    out.sort_unstable();
    out.dedup();
    out
}

/// The most of `taken` that overlap at any instant of `window`, and, when
/// any do, the first instant they do.
pub fn peak_at(taken: &[Interval], window: &Interval) -> (i64, Option<i64>) {
    let mut events: Vec<(i64, i64)> = Vec::new();
    for interval in taken.iter().filter(|interval| interval.overlaps(window)) {
        events.push((interval.start_ms.max(window.start_ms), 1));
        events.push((interval.end_ms.min(window.end_ms), -1));
    }
    // An end and a start at one instant do not overlap.
    events.sort_unstable();
    let (mut count, mut most, mut at) = (0, 0, None);
    for (instant, step) in events {
        count += step;
        if count > most {
            most = count;
            at = Some(instant);
        }
    }
    (most, at)
}

/// The most of `taken` that overlap at any instant of `window`, and, when
/// any do, the index of the first by start among those at the peak.
pub fn peak(taken: &[Interval], window: &Interval) -> (i64, Option<usize>) {
    let (most, at) = peak_at(taken, window);
    let first = at.and_then(|instant| {
        taken
            .iter()
            .enumerate()
            .filter(|(_, interval)| interval.start_ms <= instant && instant < interval.end_ms)
            .min_by_key(|(_, interval)| (interval.start_ms, interval.end_ms))
            .map(|(index, _)| index)
    });
    (most, first)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn at(text: &str) -> i64 {
        text.parse::<Timestamp>()
            .expect("a timestamp")
            .as_millisecond()
    }

    fn span(start: &str, end: &str) -> Interval {
        Interval {
            start_ms: at(start),
            end_ms: at(end),
        }
    }

    #[test]
    fn hours_read_as_wall_time_ranges() {
        assert_eq!(wall_time("00:00"), Some(0));
        assert_eq!(wall_time("24:00"), Some(DAY_MS));
        assert_eq!(wall_time("09:30"), Some(9 * HOUR_MS + 30 * MINUTE_MS));
        for bad in ["24:01", "9:30", "09:60", "25:00", "ab:cd", "09-30"] {
            assert_eq!(wall_time(bad), None, "{bad}");
        }
        let hours =
            WeekHours::parse(&json!({"mon": [["13:30", "17:30"], ["09:00", "12:30"]], "sun": []}))
                .expect("hours");
        assert_eq!(
            hours.days[0],
            [
                Range {
                    open: 9 * HOUR_MS,
                    close: 12 * HOUR_MS + 30 * MINUTE_MS
                },
                Range {
                    open: 13 * HOUR_MS + 30 * MINUTE_MS,
                    close: 17 * HOUR_MS + 30 * MINUTE_MS
                }
            ]
        );
        assert!(hours.days[6].is_empty());
        let refused = |value: Value| WeekHours::parse(&value).expect_err("refused");
        assert_eq!(refused(json!({"tue": [["10:00", "10:00"]]})).path, "/tue/0");
        let overlap = refused(json!({"wed": [["09:00", "12:00"], ["11:00", "13:00"]]}));
        assert_eq!(overlap.path, "/wed/1");
        assert!(overlap.message.contains("09:00-12:00"), "{overlap:?}");
        assert_eq!(refused(json!({"someday": []})).path, "/someday");
        assert!(
            WeekHours::parse(&json!({"thu": [["09:00", "12:00"], ["12:00", "24:00"]]})).is_ok()
        );
    }

    #[test]
    fn a_wall_time_a_change_skips_is_no_instant_and_one_it_repeats_is_two() {
        let london = zone("Europe/London").expect("London");
        let spring = Offsets::over(&london, at("2026-03-28T00:00Z"), at("2026-03-30T00:00Z"));
        // 01:30 on 29 March 2026 is skipped: clocks go from 01:00 GMT to
        // 02:00 BST.
        let wall = at("2026-03-29T01:30Z");
        assert!(spring.instants_at(wall).is_empty());
        assert_eq!(
            spring.instants_at(at("2026-03-29T02:30Z")),
            [at("2026-03-29T01:30Z")]
        );
        let autumn = Offsets::over(&london, at("2026-10-24T00:00Z"), at("2026-10-26T00:00Z"));
        // 01:30 on 25 October 2026 happens twice, BST then GMT.
        assert_eq!(
            autumn.instants_at(at("2026-10-25T01:30Z")),
            [at("2026-10-25T00:30Z"), at("2026-10-25T01:30Z")]
        );
        // 00:00 to 04:00 that day is five hours long, and on 29 March three.
        assert_eq!(
            autumn.instants(at("2026-10-25T00:00Z"), at("2026-10-25T04:00Z")),
            [span("2026-10-24T23:00Z", "2026-10-25T04:00Z")]
        );
        assert_eq!(
            spring.instants(at("2026-03-29T00:00Z"), at("2026-03-29T04:00Z")),
            [span("2026-03-29T00:00Z", "2026-03-29T03:00Z")]
        );
        // A range that opens in the gap opens when the gap ends.
        assert_eq!(
            spring.instants(at("2026-03-29T01:30Z"), at("2026-03-29T05:00Z")),
            [span("2026-03-29T01:00Z", "2026-03-29T04:00Z")]
        );
    }

    #[test]
    fn a_half_hour_change_moves_the_grid_by_half_an_hour() {
        // Lord Howe Island goes from +10:30 to +11:00 at 02:00 on the first
        // Sunday of October: 4 October 2026, at 15:30 UTC on the 3rd.
        let lord_howe = zone("Australia/Lord_Howe").expect("Lord Howe");
        let offsets = Offsets::over(&lord_howe, at("2026-10-02T00:00Z"), at("2026-10-06T00:00Z"));
        let hours = WeekHours::parse(&json!({"sun": [["00:00", "06:00"]]})).expect("hours");
        let day = Interval {
            start_ms: at("2026-10-03T12:00Z"),
            end_ms: at("2026-10-04T00:00Z"),
        };
        let open = opening(&hours, &offsets, day.start_ms, day.end_ms);
        // 00:00 +10:30 is 13:30 UTC; 06:00 +11:00 is 19:00 UTC.
        assert!(
            open.contains(&span("2026-10-03T13:30Z", "2026-10-03T19:00Z")),
            "{open:?}"
        );
        let slots = grid(&hours, &offsets, day.start_ms, day.end_ms, HOUR_MS);
        // 00:00 and 01:00 at +10:30; 02:00 is skipped; 03:00 to 05:00 at
        // +11:00.
        assert_eq!(
            slots,
            [
                at("2026-10-03T13:30Z"),
                at("2026-10-03T14:30Z"),
                at("2026-10-03T16:00Z"),
                at("2026-10-03T17:00Z"),
                at("2026-10-03T18:00Z"),
            ]
        );
        assert!(on_grid(&hours, &offsets, at("2026-10-03T16:00Z"), HOUR_MS));
        // 02:30 +11:00, an hour after 01:00 +10:30, is not on the grid.
        assert!(!on_grid(&hours, &offsets, at("2026-10-03T15:30Z"), HOUR_MS));
    }

    #[test]
    fn the_peak_counts_what_overlaps_at_one_instant() {
        let hour = |h: i64| h * HOUR_MS;
        let i = |start, end| Interval {
            start_ms: hour(start),
            end_ms: hour(end),
        };
        // A long hold overlaps two short ones at different times: the peak
        // is two, not three.
        let taken = [i(9, 17), i(9, 10), i(12, 13)];
        assert_eq!(peak(&taken, &i(9, 17)), (2, Some(1)));
        assert_eq!(peak(&taken, &i(10, 12)), (1, Some(0)));
        // One that ends as another starts does not overlap it.
        assert_eq!(peak(&[i(9, 10), i(10, 11)], &i(9, 11)), (1, Some(0)));
        assert_eq!(peak(&taken, &i(18, 19)), (0, None));
        assert!(inside(&[i(9, 12), i(13, 17)], &i(13, 17)));
        assert!(!inside(&[i(9, 12), i(13, 17)], &i(11, 14)));
        assert!(!inside(&[], &i(11, 14)));
    }

    #[test]
    fn days_of_the_week_count_from_a_thursday() {
        assert_eq!(DAYS[weekday(0)], "thu");
        assert_eq!(
            DAYS[weekday(at("2026-10-19T12:00Z").div_euclid(DAY_MS))],
            "mon"
        );
        assert_eq!(DAYS[weekday(-1)], "wed");
        assert!(zone("Etc/Unknown").is_none());
        assert!(zone("Mars/Olympus_Mons").is_none());
        assert!(zone("America/New_York").is_some());
    }
}
