//! The scenarios of `core-behaviors.json` that compose only behaviors the
//! core runs, replayed on the core (D103, D104 step 2): every step the
//! replayer maps must answer as the engine did, each refusal compared whole
//! (class, code, status, message and details), and the steps it maps must
//! not drop below the floor, so a regression cannot pass by skipping what
//! it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-behaviors.json` the replayer maps and compares: 118 of
/// its 223, in ten of its 13 scenarios. The expenses document's 20, a
/// claim two approvers sign and its claimant never does (Approvals beside a
/// Workflow); the invoices document's 12, a sent invoice that needs a
/// customer, an amount and its lines, which then stay (StateRules beside a
/// Workflow); the requests document's 17, escalated and reminded on the
/// runner (Timers); 36 of the project tree's 37 (Reactions), all but a read
/// of the event log that holds a move a rule made, which a change command
/// writes as an update of the row's owned fields; and the `open` of the
/// scenario that registers the core's behaviors, whose two reads of the
/// engine's registry and `behaviors.register`, where it stops, are outside
/// the reference host's surface; and the `open` of the scenario whose
/// engine ran without the work queue's package and refused a define of
/// Lease for its implementation, which the core runs (D104), where it skips
/// that define. The document the core binary builds, Workflow, Comments and
/// Revisions on one type, runs since the core runs all three (D103's step
/// 4), but for two reads of the event log that hold a post beside the row;
/// and so does the start of the tasks, projects and invoices documents,
/// up to a link that holds a list of targets. The 3 other scenarios compose
/// behaviors the core does not run yet (Branches, Search, Variants), and
/// are skipped whole.
/// The `open` steps that run count but compare nothing, and each
/// `runner.runDue` counts as checked once the changes and deadlines it ran
/// answered.
const FLOOR: usize = 118;

#[test]
fn core_behaviors_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-behaviors"), &ported)
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
