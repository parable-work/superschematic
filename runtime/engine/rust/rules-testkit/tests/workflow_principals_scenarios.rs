//! Workflow's hand-written scenarios (`core-workflow-principals.json`,
//! D104) replayed on the core: who makes a transition (`by`, and the
//! `overridePermission` past it), a move to the state a row is in answered
//! as no change (`repeat`), the state a caller read (`from`), and a state a
//! transition leaves that counts as finished (`finished`, D113), which the
//! engine recorded none of. Every step must answer as the file writes, and
//! the steps the replayer maps must not drop below the floor.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-workflow-principals.json` the replayer maps and
/// compares: all 133 of its 11 scenarios', the 11 `open` steps among them,
/// which count and compare nothing, and test.Gate's scenario's, which the
/// reference host runs as a fixture. None is left out.
const FLOOR: usize = 133;

#[test]
fn workflow_principals_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-workflow-principals"), &ported)
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
