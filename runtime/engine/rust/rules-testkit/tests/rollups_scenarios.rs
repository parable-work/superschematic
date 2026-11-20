//! Rollups' recorded scenarios (`core-rollups.json`) replayed on the core
//! (D103, D104's step 4): every step the replayer maps must answer as the
//! engine did, each refusal compared whole, and the steps it maps must not
//! drop below the floor. The engine's Links link is a to-one relation on a
//! table, so a rollup reads the rows a `HasMany` of the table lists, and a
//! record's `Rollups.values` is what a read of the row computes through its
//! reads (`superschematic_rules::Action::Read`), compared with the engine's
//! volatile field at each read.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-rollups.json` the replayer maps and compares: 220 of
/// its 286, in 13 scenarios: every function, a linked row's change shown at
/// the next read with no write of the row read, the gate whoever asks, an
/// any gate, outcomes, the config's rules on the type's own Workflow, a
/// rollup over its own table, a relation that points elsewhere, and the
/// config change. The 66 it skips: the 22 steps of the scenario whose
/// access policy is the test's own; the 28 of the two scenarios that stop
/// at a define the core refuses, a latest rollup, which waits for the core
/// to read when a row was created (D104's step 4); the 15 defines and
/// publishes the engine refused for what another schema lacks (its live
/// version, its link here, the field a function reads), which a type's
/// define check on the core cannot see and the build holds; and one refused
/// define of a type that lists Links, whose words name the engine's Links.
const FLOOR: usize = 220;

#[test]
fn rollups_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-rollups"), &ported)
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
