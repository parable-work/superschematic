//! The hand-written scenarios of D103's step 5's bundles
//! (`core-bundles.json`) replayed on the core: Reservation, Barrier,
//! Mailbox and Assignment, each a table whose uses are the parts the loader
//! expands the bundle into, which `internal/loader`'s
//! TestBundleScenariosAreTheLoadersParts holds the file's defines to. A
//! bundle defines no rules, so the core runs its parts as it runs any
//! table's uses: a booking's hold of its room's time, its payment, its
//! commit and its expiry; a rollout's gate over its agents, its deadline's
//! vetoed release and a person's go-ahead; a message's sender's moves, its
//! taker's lease, its freeze while taken and the lease's expiry back to
//! the queue; and one seat of one subject. Every step must answer as the
//! file writes, and the steps the replayer maps must not drop below the
//! floor.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-bundles.json` the replayer maps and compares: all 81
/// of its 4 scenarios', the 4 `open` steps among them, which count and
/// compare nothing. None is left out.
const FLOOR: usize = 81;

#[test]
fn bundles_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-bundles"), &ported)
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
