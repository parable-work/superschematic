//! Retries' recorded scenarios (`workqueue-retries.json`) replayed on the
//! core (D103, D104 step 2): every step the replayer maps must answer as the
//! engine did, each refusal compared whole (class, code, status, message
//! and details), and the steps it maps must not drop below the floor, so a
//! regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `workqueue-retries.json` the replayer maps and compares:
/// 290 of its 379, in 16 of its 21 scenarios. The 89 it skips: the 36 steps
/// of the scenarios that compose Queue, which folds into @queue (D103,
/// D104); the 52 of the five scenarios whose config gives resultField,
/// which a table's Retries refuses (a kept result written through an update
/// an operation makes waits for Revisions, D103 step 4), each stopping at
/// its define with the core's reason; and the define the engine's requires
/// check refused, a type with Retries and no Workflow, which the core
/// refuses in its own words. The `open` steps that run count but compare
/// nothing.
const FLOOR: usize = 290;

#[test]
fn retries_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("workqueue-retries"), &ported)
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
