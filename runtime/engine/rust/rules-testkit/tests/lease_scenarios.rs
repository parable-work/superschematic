//! Lease's recorded scenarios replayed on the core (D103, D104 step 2): a
//! table's lease, its fencing token presented as the command's
//! precondition, its expiry a deadline the runner's runDue runs as the API's
//! worker does. Every step the replayer maps must answer as the engine did,
//! each refusal compared whole (class, code, status, message and details),
//! and the steps it maps must not drop below each file's floor, so a
//! regression cannot pass by skipping what it breaks.
//!
//! The recordings' configs leave out sweepMs, the engine's runner's, which
//! a table's Lease refuses and whose sweep its deadlines replace
//! ([`superschematic_rules_testkit::replay::RUNNER_MEMBERS`]); Comments in
//! `workqueue-lease.json` is incidental
//! ([`superschematic_rules_testkit::replay::INCIDENTAL`]); test.Gate and
//! test.Signal run as the kit's fixtures.

use superschematic_rules_testkit::{ported, replay_path, scenario_path, Report};

/// The steps of `workqueue-lease.json` the replayer maps and compares: 637
/// of its 691, in 47 of its 48 scenarios, expireHolder's calls among them,
/// each run as a command without a row on Lease's use (D104). The 54 it
/// skips: the 26 steps of the scenario that composes Dependencies, which
/// the core does not run; the 12 steps of the engines opened with a value
/// store, which a table has no counterpart of; the 10 steps that call
/// Comments' operations or whose refusal names it (incidental); and the 6
/// reads of the engine's store (storage.get, runner.status) and list and
/// update options the reference host does not take (limit, expectedSeq).
/// The `open` steps that run count but compare nothing, and each
/// `runner.runDue` counts as checked once the deadlines it ran answered.
const FLOOR: usize = 637;

/// The steps of `workqueue-lease-timers.json` the replayer maps and
/// compares: all 24, a Timers fire the holder's lease holds and one it
/// exempts.
const TIMERS_FLOOR: usize = 24;

/// The steps of `workqueue-lease-approvals.json` the replayer maps and
/// compares: all 25, sign-offs another principal's lease holds and one the
/// config exempts.
const APPROVALS_FLOOR: usize = 25;

// replayed replays a recording on the core and holds it to no mismatch
// and its floor.
fn replayed(name: &str, floor: usize) -> Report {
    let report =
        replay_path(&scenario_path(name), &ported).unwrap_or_else(|error| panic!("{error}"));
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
    report
}

#[test]
fn lease_scenarios_replay_on_the_core() {
    replayed("workqueue-lease", FLOOR);
}

#[test]
fn lease_beside_timers_replays_on_the_core() {
    replayed("workqueue-lease-timers", TIMERS_FLOOR);
}

#[test]
fn lease_beside_approvals_replays_on_the_core() {
    replayed("workqueue-lease-approvals", APPROVALS_FLOOR);
}

/// `workqueue-lease-direct-on.json` composes Branches and Revisions, whose
/// moves directOn sends its directives on, which wait for step 4 of D103:
/// every step is skipped with that reason, and none mismatches.
#[test]
fn lease_direct_on_is_skipped_with_its_reason() {
    let report = replayed("workqueue-lease-direct-on", 0);
    assert_eq!(report.checked(), 0);
    for scenario in &report.scenarios {
        assert_eq!(scenario.skipped.len(), scenario.steps, "{}", scenario.name);
        for skipped in &scenario.skipped {
            assert_eq!(
                skipped.reason,
                "it composes behaviors the core does not run: Branches, Revisions"
            );
        }
    }
}
