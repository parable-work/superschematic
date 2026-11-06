//! The calendar reads a zone in the IANA database jiff bundles and never in
//! the host's zone files (D104): a host whose `TZDIR` holds another
//! America/New_York computes the bundled one's runs. In this workspace
//! jiff's `tzdb-zoneinfo` is off, so jiff's own lookup would not read
//! `TZDIR` either; a project's Cargo graph may turn it on by feature
//! unification, which `cargo test -p superschematic-rules-behaviors
//! --features jiff/tzdb-zoneinfo --test calendar_zones` stands in for. The
//! test is the only one of its process, since jiff reads `TZDIR` once, at
//! its first lookup.

use std::fs;

use superschematic_rules_behaviors::calendar::{Cron, Zone};

// utc_tzif is a TZif file (version 2) of a zone that is UTC at every
// instant: one local time type, no transitions, and the footer UTC0.
fn utc_tzif() -> Vec<u8> {
    let header = |bytes: &mut Vec<u8>| {
        bytes.extend_from_slice(b"TZif2");
        bytes.extend_from_slice(&[0; 15]);
        // isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt.
        for count in [0u32, 0, 0, 0, 1, 4] {
            bytes.extend_from_slice(&count.to_be_bytes());
        }
    };
    let data = |bytes: &mut Vec<u8>| {
        // One type: offset 0, not daylight saving time, designation 0.
        bytes.extend_from_slice(&[0, 0, 0, 0, 0, 0]);
        bytes.extend_from_slice(b"UTC\0");
    };
    let mut bytes = Vec::new();
    header(&mut bytes);
    data(&mut bytes);
    header(&mut bytes);
    data(&mut bytes);
    bytes.extend_from_slice(b"\nUTC0\n");
    bytes
}

#[test]
fn a_zone_is_read_in_the_bundled_database_and_never_in_tzdir() {
    let dir = std::env::temp_dir().join(format!(
        "superschematic-calendar-tzdir-{}",
        std::process::id()
    ));
    let america = dir.join("America");
    fs::create_dir_all(&america).unwrap_or_else(|error| panic!("{error}"));
    fs::write(america.join("New_York"), utc_tzif()).unwrap_or_else(|error| panic!("{error}"));
    std::env::set_var("TZDIR", &dir);
    let new_york = Zone::named("America/New_York").unwrap_or_else(|message| panic!("{message}"));
    let midnight = Cron::parse("0 0 * * *")
        .unwrap_or_else(|message| panic!("{message}"))
        .next_after(&new_york, 1_767_571_200_000);
    let _ = fs::remove_dir_all(&dir);
    // Midnight in New York on 2026-01-05 is 05:00Z, EST; the zone TZDIR
    // holds would make it 00:00Z the next day.
    assert_eq!(midnight, Some(1_767_589_200_000));
}
