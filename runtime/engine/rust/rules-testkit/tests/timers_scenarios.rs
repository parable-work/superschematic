//! Timers' recorded scenarios (`core-timers.json`) replayed on the core
//! (D103): every step the replayer maps must answer as the engine did,
//! each refusal compared whole (class, code, status, message and details),
//! and the steps it maps must not drop below the floor, so a regression
//! cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-timers.json` the replayer maps and compares: 324 of
/// its 420. The 96 it skips: the 22 steps of the scenario that composes
/// StateRules and the 14 of the one that composes Reactions, which the
/// core does not run; 7 of the scenario whose access policy is the test's
/// own (its open is checked), since the API authorizes (D103); 23 of the
/// scenario that names namespaces, from its first define in one, since the
/// reference host keeps the corpus's one; 26 of the new-version scenario,
/// from the publish that adds Timers with a field deadline to a type with
/// rows, which the engine's afterConfigChange armed on every row and the
/// core arms at each row's next write; and the runner's status twice and
/// the tools' describe twice, the engine's own reads. The 21 `open` steps
/// that run count but compare nothing, and each `runner.runDue` counts as
/// checked once the deadlines it ran answered, the engine's counters being
/// its store's. The two scenarios that compose a test behavior run on the
/// fixtures this kit ports (test.Bounce, test.Hold).
const FLOOR: usize = 324;

#[test]
fn timers_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-timers"), &ported)
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
