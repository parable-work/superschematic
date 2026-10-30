//! Workflow's recorded scenarios (`core-workflow.json`) replayed on the
//! core (D103): every step the replayer maps must answer as the engine
//! did, and the steps it maps must not drop below the floor, so a
//! regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-workflow.json` the replayer maps and compares: all of
/// its 158 but four, each of which tests the engine's store. The test's raw
/// SQL write of Workflow's `enteredAt` column is applied to the row and not
/// compared, since its result is SQLite's; the two writes of an own field
/// `status` the type does not have are the store's own-field validation,
/// which is the table's and never reaches the core; and test.Dispatch's
/// `forceStatus` is its SQL against the store's guard. The 19 `open`
/// steps count but compare nothing, and the recordings of
/// `isTerminalState` and `stateOutcome` hold only their define, create and
/// `schemas.behaviors` steps: those two functions are held by
/// superschematic-rules-behaviors' own tests, not by this replay.
const FLOOR: usize = 154;

#[test]
fn workflows_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-workflow"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the engine:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= FLOOR,
        "{} steps checked, under the floor of {FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}
