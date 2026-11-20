//! Numbering's recorded scenarios (`core-numbering.json`, and Lease beside
//! it in `workqueue-lease-numbering.json`) replayed on the core (D103, step
//! 4): every step the replayer maps must answer as the engine did, each
//! refusal compared whole, and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks. Each
//! recorded type that composes Numbering in any version is given an own
//! field createdAt, which its creates set to their time and no comparison
//! reads, as the engine kept an instance's creation time beside it, so
//! assignNumber numbers a row made before the type composed Numbering in
//! the year it was made.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-numbering.json` the replayer maps and compares: 127
/// of its 209, in its 12 scenarios. The 82 it skips: the two scenarios whose
/// test batches its creates in a function of its own (`storage.transaction`
/// with a JavaScript callback), which only the recording's host runs (39
/// steps); the 23 defines a type that lists Links refuses, whose paths and
/// words name the Links config where a table's link is a to-one relation,
/// whose refusals in the core's words `rules-behaviors`' tests hold; the
/// define of a scope link that holds many targets, which a relation cannot;
/// the two creates and the update the store's own-field validation or Links
/// refuses; a read of the event log that holds a Links operation; the two
/// describes; and the 13 steps of the scenario beside Revisions, which the
/// core does not run yet. The `open` steps that run count but compare
/// nothing.
const FLOOR: usize = 127;

/// The steps of `workqueue-lease-numbering.json` it maps and compares: all
/// 16 of its one scenario, a create numbered under another principal's
/// lease and assignNumber held by Lease's guard until its exempt names it.
const LEASE_FLOOR: usize = 16;

fn replays(file: &str, floor: usize) {
    let report =
        replay_path(&scenario_path(file), &ported).unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the engine:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= floor,
        "{} steps checked, under the floor of {floor}:\n{}",
        report.checked(),
        report.details()
    );
}

#[test]
fn numbering_scenarios_replay_on_the_core() {
    replays("core-numbering", FLOOR);
}

#[test]
fn lease_beside_numbering_replays_on_the_core() {
    replays("workqueue-lease-numbering", LEASE_FLOOR);
}
