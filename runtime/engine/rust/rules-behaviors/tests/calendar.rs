//! The calendar Schedules reads cron lines with (D104): a line's fields,
//! lists, ranges, steps and names, seven as Sunday, the macros, each
//! refusal in its words; the next run in a zone, a local time the spring
//! gap skips running at the gap's first instant and one the autumn fold
//! repeats running once, at its first occurrence, in America/New_York and in
//! Europe/London, and a gap's first minute there and in four zones more; a
//! leap day, a month without the day, Vixie cron's rule for two restricted
//! day fields, a line that never runs; the closest runs read around the
//! clock and across the dates the day fields name; a zone matched as the
//! database spells it, an unknown one refused; and that nothing here reads
//! a clock.

use superschematic_rules_behaviors::calendar::{Cron, Zone, HORIZON_DAYS};

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;
const DAY: i64 = 24 * HOUR;

fn cron(line: &str) -> Cron {
    Cron::parse(line).unwrap_or_else(|message| panic!("{line}: {message}"))
}

fn zone(name: &str) -> Zone {
    Zone::named(name).unwrap_or_else(|message| panic!("{message}"))
}

// runs is a line's next `count` runs in a zone after an instant, each from
// the one before it.
fn runs(line: &str, zone: &Zone, after: i64, count: usize) -> Vec<i64> {
    let cron = cron(line);
    let mut at = after;
    let mut found = Vec::new();
    for _ in 0..count {
        let Some(next) = cron.next_after(zone, at) else {
            break;
        };
        found.push(next);
        at = next;
    }
    found
}

fn refusal(line: &str) -> String {
    match Cron::parse(line) {
        Ok(cron) => panic!("{line} parsed as {cron:?}"),
        Err(message) => message,
    }
}

#[test]
fn a_line_reads_its_fields_lists_ranges_steps_names_and_seven_as_sunday() {
    let utc = Zone::utc();
    // Monday 2026-01-05, at 00:00 UTC: half past and on the hour of every
    // other hour from nine to five, on weekdays of January to March.
    let monday = 1_767_571_200_000;
    let line = "0,30 9-17/2 * JAN-MAR mon-fri";
    let day = runs(line, &utc, monday, 11);
    let hours: Vec<i64> = [9, 9, 11, 11, 13, 13, 15, 15, 17, 17]
        .iter()
        .zip([0, 30, 0, 30, 0, 30, 0, 30, 0, 30])
        .map(|(hour, minute)| monday + hour * HOUR + minute * MINUTE)
        .collect();
    assert_eq!(day[..10], hours[..]);
    // The next is Tuesday's first.
    assert_eq!(day[10], 1_767_690_000_000);
    // Friday's last is followed by Monday's first.
    assert_eq!(
        cron(line).next_after(&utc, 1_767_979_800_000),
        Some(1_768_208_400_000)
    );
    // After March's last weekday run, the next is January's first weekday,
    // a Friday.
    assert_eq!(
        cron(line).next_after(&utc, 1_774_978_200_000),
        Some(1_798_794_000_000)
    );
    // Seven is Sunday, as 0 is: Sunday 2026-03-01's midnight is not after
    // itself, and the next Sunday's is.
    for line in ["0 0 * * 7", "0 0 * * 0", "0 0 * * SUN"] {
        assert_eq!(
            cron(line).next_after(&utc, 1_772_323_200_000),
            Some(1_772_928_000_000),
            "{line}"
        );
    }
    // A line is normalized: trimmed, its fields one space apart, its names
    // and numbers as written.
    assert_eq!(cron("  0  9 * *   MON-FRI ").line(), "0 9 * * MON-FRI");
    assert_eq!(cron(line).line(), line);
    assert_eq!(cron("0  9 * *   MON-FRI"), cron("0 9 * * MON-FRI"));
    assert_ne!(cron("0 9 * * MON-FRI"), cron("0 9 * * 1-5"));
    // A name steps as its number does.
    assert_eq!(
        runs("0 0 1 JAN-DEC/3 *", &utc, 1_767_571_200_000, 2),
        [1_775_001_600_000, 1_782_864_000_000]
    );
}

#[test]
fn the_macros_read_as_their_lines_and_reboot_is_refused() {
    let utc = Zone::utc();
    let after = 1_767_571_200_000 + 90 * MINUTE;
    for (name, line) in [
        ("@yearly", "0 0 1 1 *"),
        ("@annually", "0 0 1 1 *"),
        ("@monthly", "0 0 1 * *"),
        ("@weekly", "0 0 * * 0"),
        ("@daily", "0 0 * * *"),
        ("@midnight", "0 0 * * *"),
        ("@hourly", "0 * * * *"),
    ] {
        let read = cron(name);
        assert_eq!(read.line(), name);
        assert_eq!(
            read.next_after(&utc, after),
            cron(line).next_after(&utc, after),
            "{name}"
        );
        assert_eq!(read.closest_ms(), cron(line).closest_ms(), "{name}");
    }
    // A macro is matched in any case and kept in lowercase.
    assert_eq!(cron(" @Daily ").line(), "@daily");
    assert_eq!(
        refusal("@reboot"),
        "@reboot runs when a host starts, which a schedule never does"
    );
    assert_eq!(
        refusal("@REBOOT"),
        "@reboot runs when a host starts, which a schedule never does"
    );
}

#[test]
fn each_refusal_names_its_field_and_what_breaks() {
    let cases = [
        // 1. An empty line.
        (
            "",
            "a cron line names five fields, minute hour day-of-month month day-of-week, and this one is empty",
        ),
        (
            "   ",
            "a cron line names five fields, minute hour day-of-month month day-of-week, and this one is empty",
        ),
        // 2. Another count of fields, a seconds field named as such.
        (
            "0 0 9 * * *",
            "a cron line names five fields, minute hour day-of-month month day-of-week, and \"0 0 9 * * *\" names 6: a seconds field is not taken",
        ),
        (
            "0 9 * *",
            "a cron line names five fields, minute hour day-of-month month day-of-week, and \"0 9 * *\" names 4",
        ),
        // 3. @reboot.
        (
            "@reboot",
            "@reboot runs when a host starts, which a schedule never does",
        ),
        // 4. Another macro.
        (
            "@often",
            "\"@often\" is no macro (the macros: @yearly, @annually, @monthly, @weekly, @daily, @midnight, @hourly)",
        ),
        // 5. An empty item.
        ("0 0 * * 1,,5", "the day-of-week field \"1,,5\" has an empty item"),
        // 6. A character no field takes: ?, # and the like.
        (
            "0 0 * * 5#3",
            "the day-of-week field \"5#3\" holds #, which cron's five fields do not take: a field takes *, numbers, names of months and days, ranges (a-b), steps (*/n, a-b/n) and lists (a,b)",
        ),
        (
            "0 0 ? * 1",
            "the day-of-month field \"?\" holds ?, which cron's five fields do not take: a field takes *, numbers, names of months and days, ranges (a-b), steps (*/n, a-b/n) and lists (a,b)",
        ),
        // 7. A name where no name is taken: L, W and H among them.
        (
            "0 0 L * *",
            "the day-of-month field \"L\" holds the name L: only the month and day-of-week fields take names",
        ),
        (
            "H 0 * * *",
            "the minute field \"H\" holds the name H: only the month and day-of-week fields take names",
        ),
        // 8. An unknown name.
        (
            "0 0 * FOO *",
            "the month field \"FOO\" holds FOO, which is no month's name (JAN to DEC)",
        ),
        (
            "0 0 * * MONDAY",
            "the day-of-week field \"MONDAY\" holds MONDAY, which is no day's name (SUN to SAT)",
        ),
        // 9. A value out of range.
        ("0 24 * * *", "the hour field \"24\" holds 24, outside 0-23"),
        ("0 0 0 * *", "the day-of-month field \"0\" holds 0, outside 1-31"),
        ("0 0 * * 8", "the day-of-week field \"8\" holds 8, outside 0-7"),
        ("60 0 * * *", "the minute field \"60\" holds 60, outside 0-59"),
        // 10. A range that runs down.
        (
            "0 0 * * 5-1",
            "the day-of-week field \"5-1\" holds the range 5-1, which runs down: a range runs up",
        ),
        // 11. A step after a single value.
        (
            "5/15 * * * *",
            "the minute field \"5/15\" steps from 5: a step follows * or a range (5-59/15)",
        ),
        // 12. A step out of bounds, or not a whole number.
        (
            "*/0 * * * *",
            "the minute field \"*/0\" steps by 0: a step is a whole number from 1 to 60",
        ),
        (
            "0 */25 * * *",
            "the hour field \"*/25\" steps by 25: a step is a whole number from 1 to 24",
        ),
        (
            "0 0 1-10/x * *",
            "the day-of-month field \"1-10/x\" steps by x: a step is a whole number from 1 to 31",
        ),
    ];
    for (line, message) in cases {
        assert_eq!(refusal(line), message, "{line}");
    }
}

#[test]
fn the_spring_gap_in_new_york_runs_at_its_first_instant() {
    let new_york = zone("America/New_York");
    // 02:30 is skipped on 2026-03-08: it runs at 03:00 EDT, 07:00Z, the
    // change itself; the next day at 02:30 EDT.
    assert_eq!(
        runs("30 2 * * *", &new_york, 1_772_884_800_000, 2),
        [1_772_953_200_000, 1_773_037_800_000]
    );
    // Several skipped times run once, at the gap's first instant.
    assert_eq!(
        runs("0,30 2 * * *", &new_york, 1_772_884_800_000, 2),
        [1_772_953_200_000, 1_773_036_000_000]
    );
    // The gap's first minute, 02:00, the change itself read at EST, runs
    // at the change too, not a day late: a nightly 2 AM line keeps its run.
    assert_eq!(
        runs("0 2 * * *", &new_york, 1_772_884_800_000, 2),
        [1_772_953_200_000, 1_773_036_000_000]
    );
    // A line of that one date, set a week before, runs at the change, not
    // a year on.
    assert_eq!(
        cron("0 2 8 3 *").next_after(&new_york, 1_772_323_200_000),
        Some(1_772_953_200_000)
    );
}

#[test]
fn a_gaps_first_minute_runs_at_the_change_in_every_zone() {
    for (name, line, after, change) in [
        // Paris springs forward at 02:00 CET, 01:00Z, on 2026-03-29.
        (
            "Europe/Paris",
            "0 2 * * *",
            1_774_699_200_000,
            1_774_746_000_000,
        ),
        // Havana at midnight on 2026-03-08, 05:00Z: @daily's run that day.
        (
            "America/Havana",
            "0 0 * * *",
            1_772_884_800_000,
            1_772_946_000_000,
        ),
        (
            "America/Havana",
            "@daily",
            1_772_884_800_000,
            1_772_946_000_000,
        ),
        // Santiago at midnight on 2026-09-06, 04:00Z.
        (
            "America/Santiago",
            "0 0 * * *",
            1_788_609_600_000,
            1_788_667_200_000,
        ),
        // Lord Howe by half an hour at 02:00 on 2026-10-04, 15:30Z the day
        // before.
        (
            "Australia/Lord_Howe",
            "0 2 * * *",
            1_790_985_600_000,
            1_791_041_400_000,
        ),
    ] {
        assert_eq!(
            cron(line).next_after(&zone(name), after),
            Some(change),
            "{line} in {name}"
        );
    }
}

#[test]
fn the_autumn_fold_in_new_york_runs_once_at_its_first_occurrence() {
    let new_york = zone("America/New_York");
    // 01:30 comes twice on 2026-11-01: it runs at the first, 01:30 EDT,
    // 05:30Z, and the next day at 01:30 EST.
    assert_eq!(
        runs("30 1 * * *", &new_york, 1_793_448_000_000, 2),
        [1_793_511_000_000, 1_793_601_000_000]
    );
    // The repeated hour runs nothing: after 01:45 EDT the next half hour is
    // 02:00 EST, 07:00Z.
    assert_eq!(
        cron("*/30 * * * *").next_after(&new_york, 1_793_511_900_000),
        Some(1_793_516_400_000)
    );
}

#[test]
fn the_spring_gap_in_london_runs_at_its_first_instant() {
    let london = zone("Europe/London");
    // 01:30 is skipped on 2026-03-29: it runs at 02:00 BST, 01:00Z; the next
    // day at 01:30 BST, 00:30Z.
    assert_eq!(
        runs("30 1 * * *", &london, 1_774_699_200_000, 2),
        [1_774_746_000_000, 1_774_830_600_000]
    );
    // The gap's first minute, 01:00, runs at the change too; the next day
    // at 01:00 BST, 00:00Z.
    assert_eq!(
        runs("0 1 * * *", &london, 1_774_699_200_000, 2),
        [1_774_746_000_000, 1_774_828_800_000]
    );
}

#[test]
fn the_autumn_fold_in_london_runs_once_at_its_first_occurrence() {
    let london = zone("Europe/London");
    // 01:30 comes twice on 2026-10-25: it runs at the first, 01:30 BST,
    // 00:30Z, and the next day at 01:30 GMT.
    assert_eq!(
        runs("30 1 * * *", &london, 1_792_843_200_000, 2),
        [1_792_888_200_000, 1_792_978_200_000]
    );
}

#[test]
fn a_leap_day_runs_in_its_year() {
    let line = "0 12 29 2 *";
    assert_eq!(
        cron(line).next_after(&Zone::utc(), 1_772_323_200_000),
        Some(1_835_438_400_000)
    );
    assert_eq!(
        cron(line).next_after(&zone("America/New_York"), 1_772_323_200_000),
        Some(1_835_456_400_000)
    );
    // And the leap day after it, four years on.
    assert_eq!(
        cron(line).next_after(&Zone::utc(), 1_835_438_400_000),
        Some(1_835_438_400_000 + 1461 * DAY)
    );
}

#[test]
fn a_month_without_the_day_is_passed_over() {
    // April has no 31st: the next is May's.
    assert_eq!(
        cron("0 0 31 * *").next_after(&Zone::utc(), 1_775_001_600_000),
        Some(1_780_185_600_000)
    );
}

#[test]
fn both_day_fields_restricted_run_on_either_and_a_star_step_restricts_nothing() {
    // The 13th or a Friday: Fridays 2026-02-06, 02-13 (the 13th one) and
    // 02-20.
    assert_eq!(
        runs("0 0 13 * 5", &Zone::utc(), 1_769_904_000_000, 3),
        [1_770_336_000_000, 1_770_940_800_000, 1_771_545_600_000]
    );
    // After Friday 2026-04-10, the 13th, a Monday, then Friday 04-17.
    assert_eq!(
        runs("0 0 13 * 5", &Zone::utc(), 1_775_779_200_000, 2),
        [1_776_038_400_000, 1_776_384_000_000]
    );
    // A day field that begins with * restricts nothing, so the line runs
    // on odd days that are Mondays: 2026-02-09, 02-23, 03-09, 03-23.
    let odd_mondays: Vec<i64> = [8, 22, 36, 50]
        .iter()
        .map(|days| 1_769_904_000_000 + days * DAY)
        .collect();
    assert_eq!(
        runs("0 0 */2 * 1", &Zone::utc(), 1_769_904_000_000, 4),
        odd_mondays
    );
}

#[test]
fn a_line_that_never_runs_has_no_next_run() {
    assert_eq!(
        cron("0 0 30 2 *").next_after(&Zone::utc(), 1_772_323_200_000),
        None
    );
    assert_eq!(
        cron("0 0 31 4,6,9,11 *").next_after(&zone("Europe/London"), 1_772_323_200_000),
        None
    );
}

#[test]
fn a_next_run_is_looked_for_within_the_horizon_and_never_at_the_instant() {
    let utc = Zone::utc();
    // A run at the instant itself is not after it.
    assert_eq!(
        cron("0 0 * * *").next_after(&utc, 1_772_323_200_000),
        Some(1_772_323_200_000 + DAY)
    );
    // A minute's run after an instant within that minute is the next.
    assert_eq!(
        cron("* * * * *").next_after(&utc, 1_772_323_200_001),
        Some(1_772_323_200_000 + MINUTE)
    );
    // The horizon is ten years of local dates, which a leap day's runs
    // eight years apart lie within: after 2096-03-01, 2100 has none, and
    // the next is 2104-02-29, 2920 days on.
    assert_eq!(HORIZON_DAYS, 3653);
    assert_eq!(
        cron("0 0 29 2 *").next_after(&utc, 3_981_398_400_000),
        Some(4_233_686_400_000)
    );
    assert_eq!(4_233_686_400_000 - 3_981_398_400_000, 2920 * DAY);
}

#[test]
fn the_closest_runs_are_read_around_the_clock() {
    assert_eq!(cron("*/5 * * * *").closest_ms(), 300_000);
    assert_eq!(cron("0 23,1 * * *").closest_ms(), 7_200_000);
    assert_eq!(cron("0 9 * * *").closest_ms(), 86_400_000);
    assert_eq!(cron("* * * * *").closest_ms(), 60_000);
    assert_eq!(cron("@hourly").closest_ms(), 3_600_000);
}

#[test]
fn the_closest_runs_read_the_day_fields() {
    // A day's two times, 22 hours apart, are closer than a week's last and
    // the next week's first, six days and two hours apart.
    assert_eq!(cron("0 1,23 * * MON").closest_ms(), 22 * HOUR);
    assert_eq!(cron("@weekly").closest_ms(), 7 * DAY);
    // February's first to March's.
    assert_eq!(cron("@monthly").closest_ms(), 28 * DAY);
    assert_eq!(cron("@yearly").closest_ms(), 365 * DAY);
    // The 15th to the next month's first, in February.
    assert_eq!(cron("0 12 1,15 * *").closest_ms(), 14 * DAY);
    // A leap day's runs are four years apart at their closest.
    assert_eq!(cron("0 0 29 2 *").closest_ms(), 1461 * DAY);
    // The 13th or a Friday: a Thursday the 13th and the Friday after.
    assert_eq!(cron("0 0 13 * 5").closest_ms(), DAY);
    // A line that never runs has no two runs.
    assert_eq!(cron("0 0 30 2 *").closest_ms(), i64::MAX);
}

#[test]
fn an_unknown_zone_is_refused_and_a_name_is_matched_as_the_database_spells_it() {
    assert_eq!(
        Zone::named("Mars/Olympus").map(|zone| zone.name().to_owned()),
        Err("\"Mars/Olympus\" is not a time zone of the IANA database the core bundles".to_owned())
    );
    assert_eq!(zone("america/new_york").name(), "America/New_York");
    assert_eq!(zone("UTC").name(), "UTC");
    assert_eq!(Zone::utc().name(), "UTC");
    assert_eq!(zone("utc"), Zone::utc());
    assert_eq!(zone("europe/LONDON"), zone("Europe/London"));
    // An identifier that names no IANA zone is refused too.
    assert!(Zone::named("Etc/Unknown").is_err());
    assert!(Zone::named("").is_err());
}

#[test]
fn calendar_reads_no_clock() {
    let source = include_str!("../src/calendar.rs");
    for call in ["Timestamp::now", "Zoned::now", "TimeZone::system"] {
        assert!(!source.contains(call), "calendar.rs names {call}");
    }
}
