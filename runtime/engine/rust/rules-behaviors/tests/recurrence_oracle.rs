//! Recurrence's RFC 5545 expansion (D152) held to the maintained rrule
//! crate, date for date: a battery of rules, each from several starts, the
//! first occurrences of each read by both and compared in wall time. The
//! crate expands in its UTC zone, which is wall time with no change in it,
//! as the core's own expansion is; the zone is applied after, by the core
//! alone, and the golden expansions (`recurrence_golden.rs`) hold that.

use chrono::{Datelike, TimeZone, Timelike};
use jiff::civil::DateTime;
use superschematic_rules_behaviors::recurrence::Rule;

// How many occurrences of each rule and start are compared.
const TAKE: usize = 60;

const RULES: &[&str] = &[
    "FREQ=YEARLY",
    "FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31",
    "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
    "FREQ=YEARLY;INTERVAL=2;BYMONTH=1;BYDAY=SU;BYHOUR=8,9;BYMINUTE=30",
    "FREQ=YEARLY;BYDAY=20MO",
    "FREQ=YEARLY;BYDAY=-1FR",
    "FREQ=YEARLY;BYMONTH=3;BYDAY=TH",
    "FREQ=YEARLY;BYMONTH=11;BYDAY=4TH",
    "FREQ=YEARLY;BYYEARDAY=1,100,200",
    "FREQ=YEARLY;BYYEARDAY=-1,-306",
    "FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO",
    "FREQ=YEARLY;BYWEEKNO=1;BYDAY=MO",
    "FREQ=YEARLY;BYWEEKNO=-1;BYDAY=SU",
    "FREQ=YEARLY;BYWEEKNO=53;BYDAY=TH",
    "FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO;WKST=SU",
    "FREQ=YEARLY;BYMONTH=1,4,7,10;BYMONTHDAY=15",
    "FREQ=YEARLY;BYMONTH=6;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1",
    "FREQ=MONTHLY",
    "FREQ=MONTHLY;BYMONTHDAY=31",
    "FREQ=MONTHLY;BYMONTHDAY=-1",
    "FREQ=MONTHLY;BYMONTHDAY=1,15",
    "FREQ=MONTHLY;BYMONTHDAY=-3",
    "FREQ=MONTHLY;BYDAY=1FR",
    "FREQ=MONTHLY;BYDAY=-2MO",
    "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1",
    "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1,-1",
    "FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13",
    "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=15;BYHOUR=17",
    "FREQ=MONTHLY;INTERVAL=18;BYMONTHDAY=10,11,12,13,14,15",
    "FREQ=MONTHLY;BYMONTH=1,6;BYDAY=2TU",
    "FREQ=WEEKLY",
    "FREQ=WEEKLY;BYDAY=TU,TH",
    "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR",
    "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,SU;WKST=SU",
    "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,SU;WKST=MO",
    "FREQ=WEEKLY;BYMONTH=1,2;BYDAY=SA",
    "FREQ=WEEKLY;BYDAY=MO;BYHOUR=9,17;BYMINUTE=0,30",
    "FREQ=DAILY",
    "FREQ=DAILY;INTERVAL=10",
    "FREQ=DAILY;BYMONTH=1;BYDAY=MO,WE",
    "FREQ=DAILY;BYMONTHDAY=1,-1",
    "FREQ=DAILY;BYHOUR=9,12,15",
    "FREQ=DAILY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=8;BYMINUTE=45",
    "FREQ=HOURLY;BYYEARDAY=60,61;BYHOUR=9",
    "FREQ=HOURLY",
    "FREQ=HOURLY;INTERVAL=5",
    "FREQ=HOURLY;BYHOUR=9,10,11;BYDAY=MO",
    "FREQ=HOURLY;INTERVAL=7;BYMINUTE=15,45",
    "FREQ=YEARLY;COUNT=3",
    "FREQ=MONTHLY;BYMONTHDAY=31;COUNT=5",
    "FREQ=WEEKLY;BYDAY=MO,FR;UNTIL=20270301T090000",
    "FREQ=DAILY;UNTIL=20261231",
];

const STARTS: &[&str] = &[
    "2026-01-01T09:00:00",
    "2026-10-31T09:00:00",
    "2027-02-28T23:30:00",
    "2028-02-29T00:00:00",
    "2026-12-29T07:15:00",
    "2025-03-09T02:30:00",
];

fn ours(rule: &str, start: DateTime) -> Vec<String> {
    let rule = Rule::parse(rule).unwrap_or_else(|reason| panic!("{rule}: {reason}"));
    rule.occurrences(start)
        .take(TAKE)
        .map(|at| at.strftime("%Y-%m-%dT%H:%M:%S").to_string())
        .collect()
}

fn oracle(rule: &str, start: DateTime) -> Vec<String> {
    let start = rrule::Tz::UTC
        .with_ymd_and_hms(
            i32::from(start.year()),
            start.month() as u32,
            start.day() as u32,
            start.hour() as u32,
            start.minute() as u32,
            start.second() as u32,
        )
        .single()
        .unwrap();
    // The crate takes UNTIL in UTC when the start is: wall time is its UTC.
    let text = match rule.split_once("UNTIL=") {
        Some((head, until)) if until.len() == 8 => format!("{head}UNTIL={until}T235959Z"),
        Some((head, until)) => format!("{head}UNTIL={until}Z"),
        None => rule.to_owned(),
    };
    let unvalidated: rrule::RRule<rrule::Unvalidated> = text
        .parse()
        .unwrap_or_else(|error| panic!("{text}: {error}"));
    // The crate refuses an UNTIL before the start, which has no
    // occurrence; the core answers none.
    let set = match unvalidated.build(start) {
        Ok(set) => set,
        Err(error) if error.to_string().contains("is later") => return Vec::new(),
        Err(error) => panic!("{text}: {error}"),
    };
    set.into_iter()
        .take(TAKE)
        .map(|at| {
            format!(
                "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
                at.year(),
                at.month(),
                at.day(),
                at.hour(),
                at.minute(),
                at.second()
            )
        })
        .collect()
}

#[test]
fn every_rule_expands_as_the_rrule_crate_does() {
    let mut failures = Vec::new();
    let mut compared = 0usize;
    for rule in RULES {
        for start in STARTS {
            let start: DateTime = start.parse().unwrap();
            let (a, b) = (ours(rule, start), oracle(rule, start));
            compared += b.len();
            if a != b {
                let first = a
                    .iter()
                    .zip(&b)
                    .position(|(x, y)| x != y)
                    .unwrap_or(a.len().min(b.len()));
                failures.push(format!(
                    "{rule} from {start}: at {first}, ours {:?}, the crate's {:?} ({} and {} occurrences)",
                    a.get(first),
                    b.get(first),
                    a.len(),
                    b.len()
                ));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} cases differ:\n{}",
        failures.len(),
        RULES.len() * STARTS.len(),
        failures.join("\n")
    );
    assert!(compared > 10_000, "only {compared} occurrences compared");
}

#[test]
fn what_rfc_5545_refuses_is_refused() {
    for (rule, part) in [
        ("BYMONTH=1", "FREQ"),
        ("FREQ=MINUTELY", "finer"),
        ("FREQ=SECONDLY", "finer"),
        ("FREQ=DAILY;COUNT=2;UNTIL=20270101", "COUNT and UNTIL"),
        ("FREQ=MONTHLY;BYWEEKNO=3", "BYWEEKNO"),
        ("FREQ=WEEKLY;BYMONTHDAY=3", "BYMONTHDAY"),
        ("FREQ=DAILY;BYYEARDAY=3", "BYYEARDAY"),
        ("FREQ=DAILY;BYDAY=2MO", "ordinal"),
        ("FREQ=YEARLY;BYMONTH=13", "BYMONTH"),
        ("FREQ=YEARLY;BYMONTHDAY=0", "BYMONTHDAY"),
        ("FREQ=YEARLY;INTERVAL=0", "INTERVAL"),
        ("FREQ=YEARLY;FREQ=DAILY", "twice"),
        ("FREQ=YEARLY;BYDAY=XX", "weekday"),
        ("FREQ=YEARLY;X-NAME=1", "no part"),
    ] {
        let reason = Rule::parse(rule).unwrap_err();
        assert!(reason.contains(part), "{rule}: {reason} names no {part}");
    }
}
