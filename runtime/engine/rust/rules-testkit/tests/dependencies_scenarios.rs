//! Dependencies' recorded scenarios (`core-dependencies.json`) replayed on
//! the core (D103, D104's step 4): every step the replayer maps must answer
//! as the engine did, each refusal compared whole, and the steps it maps
//! must not drop below the floor. A record's `Dependencies.blocked` is what
//! a read of the row computes through its reads of the blockers
//! (`superschematic_rules::Action::Read`).

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-dependencies.json` the replayer maps and compares:
/// 308 of its 395, in 18 scenarios: blocked and the gate across tables, a
/// blocker's outcome against satisfiedBy, gates on states a transition
/// leaves, addBlocker's refusals, a row past its last gate, removeBlocker,
/// a dependent's delete, the config and its change, and a create's
/// blockers held to addBlocker's checks. The 87 it skips: the 63 steps of
/// the four scenarios whose access policy is the test's own; the 19 of the
/// scenario that composes test behaviors written in TypeScript
/// (test.Finisher, test.Reader); three calls of listDependents, the
/// engine's alone, which a table leaves out; one define the engine's
/// requires check refused; and one read of the event log that holds the
/// removeBlocker the engine ran on a blocker's delete, which a table's core
/// does not hear.
const FLOOR: usize = 308;

#[test]
fn dependencies_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-dependencies"), &ported)
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
