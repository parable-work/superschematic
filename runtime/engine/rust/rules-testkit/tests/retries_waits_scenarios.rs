//! The hand-written scenarios of D104's step 2 upgrades of Retries and
//! Lease (`core-retries-waits.json`) replayed on the core: a class's backoff
//! and the moves into retryInto and Lease's acquire that wait for it, then's
//! deadline (armed again at a status's entry, held by another principal's
//! lease and by the window), giveUpAfterMs, a window of attempts, keyField,
//! Lease's fencing
//! token presented as the command's precondition, its refusals, and a
//! lease's expiry as a deadline, with each one's define refusals, which the
//! engine recorded none of. Every step must answer as the file writes, and
//! the steps the replayer maps must not drop below the floor.
//!
//! A config's `status`, which names the Workflow of a type with several, a
//! raw write of keyField, and expireHolder's absence cannot be written in
//! the engine's format: `rules-behaviors`' tests hold them.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-retries-waits.json` the replayer maps and compares:
/// all 193 of its 12 scenarios', the 12 `open` steps among them, which count
/// and compare nothing, and the runDues, which count once the deadlines
/// they ran answered. None is left out.
const FLOOR: usize = 193;

#[test]
fn retries_waits_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-retries-waits"), &ported)
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
