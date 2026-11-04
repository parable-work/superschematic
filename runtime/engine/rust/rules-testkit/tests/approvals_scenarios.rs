//! Approvals' recorded scenarios (`core-approvals.json`) replayed on the
//! core (D103, D104 step 2): every step the replayer maps must answer as
//! the engine did, each refusal compared whole (class, code, status,
//! message and details), and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-approvals.json` the replayer maps and compares: 151
/// of its 198. Each recorded type is given an own field createdBy, which
/// its creates set to their caller's subject and no comparison reads, as
/// the engine kept an instance's creator beside it. The 47 it skips: the 35
/// steps of the scenario that composes Comments and Revisions, which the
/// core does not run (a change of the own fields an approval's `update()`
/// makes); 10 of the new-version scenario, from the publish that removes a
/// gate on a type with rows, whose sign-offs the engine's afterConfigChange
/// consumed there and the core consumes at each row's next write; a read
/// of the event log that holds a move a Reactions rule made; and the define
/// the engine's requires check refused, a type with Approvals and no
/// Workflow, which the core refuses in its own words. The `open` steps that
/// run count but compare nothing, and the `runner.runDue` that runs the
/// Reactions rule's changes counts as checked once they answered.
const FLOOR: usize = 151;

#[test]
fn approvals_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-approvals"), &ported)
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
