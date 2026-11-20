//! Revisions' recorded scenarios (`core-revisions.json`) replayed on the
//! core (D103, step 4): every step the replayer maps must answer as the
//! engine did, each refusal compared whole, and the steps it maps must not
//! drop below the floor, so a regression cannot pass by skipping what it
//! breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-revisions.json` the replayer maps and compares: 146
/// of its 210, in its 16 scenarios, test.Hold's among them, whose port
/// vetoes every update once held, so an approval's update meets it. The 64
/// it skips: the two scenarios whose access policy is not allowAll (51
/// steps: listProposals asks the policy for read, and propose checks the
/// proposer may read each type it cites), which the reference host does
/// not apply, since a table's API authorizes; the scenario beside Comments,
/// which the core does not run (D103, step 4), 9 steps; the three proposes
/// the store's own-field validation refuses (a title too long, a required
/// title cleared, a field the type does not have), which a table's request
/// types and database hold; and a read of the event log after an update
/// that changed nothing, which the engine answered with no write and the
/// core writes as any update.
const FLOOR: usize = 146;

#[test]
fn revisions_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-revisions"), &ported)
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
