//! Constants' recorded scenarios (`core-constants.json`) replayed on the
//! core (D103, D104 step 2): every step the replayer maps must answer as
//! the engine did, each refusal compared whole (class, code, status,
//! message and details), and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-constants.json` the replayer maps and compares: 45 of
/// its 65, every step of the four scenarios that compose Constants alone or
/// with Workflow, their opens among them, which count and compare nothing.
/// The 20 it skips are the two scenarios that compose what the core does
/// not run: an operation's `update()` held to the constants through
/// Revisions' proposal and approval (it composes Comments and Revisions,
/// and the core has no write of own fields from an operation, D103), and
/// the document the engine's binary builds for Variants.
const FLOOR: usize = 45;

#[test]
fn constants_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-constants"), &ported)
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
