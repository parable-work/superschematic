//! The hand-written scenarios of D104's step 2 upgrades of Budget and
//! Presence (`core-periods-and-presence.json`) replayed on the core:
//! Budget's meters that start again at each period (hourly, daily, weekly,
//! monthly and everyMs, in UTC, a read writing nothing and the next write
//! starting the period, reservations kept), a rolling window across the
//! boundary a fixed reset starts again at and checkReserve's until, observe
//! and its lapse at resetsAt and at the meter's next period, observe with no
//! observePermission, setLimit of a meter whose limit an own field holds
//! and the update that sets it; Presence's ttlByState on a beat and on a
//! transition, the miss falling due at the deadline the state gives and a
//! missed row arming nothing until its next beat; and each one's define
//! refusals, which the engine recorded none of. Every step must answer as
//! the file writes, and the steps the replayer maps must not drop below the
//! floor.
//!
//! A config's `status` among several Workflows, which the engine's format
//! cannot write, a principalField that holds a UUID beaten in either form,
//! and the scopes and leases a meter and a miss reach are held by
//! `rules-behaviors`' tests.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-periods-and-presence.json` the replayer maps and
/// compares: all 119 of its 8 scenarios', the 8 `open` steps among them,
/// which count and compare nothing, and the 5 `runner.runDue` steps, whose
/// counters are the engine's store's. None is left out.
const FLOOR: usize = 119;

#[test]
fn periods_and_presence_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-periods-and-presence"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= FLOOR,
        "{} steps checked, under the floor of {FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}
