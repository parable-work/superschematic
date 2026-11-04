//! Presence's recorded scenarios (`workqueue-presence.json`) replayed on
//! the core (D103, D104 step 2): every step the replayer maps must answer
//! as the engine did, each refusal compared whole (class, code, status,
//! message and details), and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks. A miss is
//! a deadline on a table, which the runner's sweep ran on the engine, so a
//! recorded config's `sweepMs` is left out (`RUNNER_MEMBERS`) and each
//! `runner.runDue` runs the misses due; the Job of the releaseLeases
//! scenarios is a table that implements Lease, whose leases the miss
//! expires through Lease's own expire.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `workqueue-presence.json` the replayer maps and compares:
/// 207 of its 226. The 19 it skips: the scenario whose defines name
/// releaseLeases' schemas, which stops at its first define, since the
/// engine refused it for another schema's state (a define check the
/// core's cannot make, which the build holds), its 7 steps; the 8 steps
/// after the two `runner.runDue`s whose recording failed (a miss whose
/// move the runner may not make, and one without Lease's
/// overridePermission), the runner's status among them; three reads of the event log with a
/// limit, which the reference host does not take; and one read of it that
/// holds the leases a miss expired, which the engine logged as Lease's
/// expire and a table's change set logs under the miss's deadline. The
/// `open` steps count but compare nothing.
const FLOOR: usize = 207;

#[test]
fn presence_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("workqueue-presence"), &ported)
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
