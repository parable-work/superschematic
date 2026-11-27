//! Blueprint's recorded scenarios (`workqueue-blueprint.json`) replayed on
//! the core (D149): every step the replayer maps must answer as the engine
//! did, each refusal compared whole, and the steps it maps must not drop
//! below the floor. The engine's Links link is a to-one relation on a
//! table, so a child's parent link is the child table's relation to the
//! parent's, which a stamp sets in each child's create, its blockers
//! Dependencies' create parameters; and a record's `Blueprint.children` is
//! what a read of the row computes through its reads
//! (`superschematic_rules::Action::Read`): the rows that point at it.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `workqueue-blueprint.json` the replayer maps and compares:
/// 206 of its 405, the inline steps' scenarios whole and the config
/// scenarios as far as they run on the core: every child with its key,
/// copied fields and data, linked to its parent and blocked after its steps
/// in one create, a when that leaves steps out and passes their edges on, a
/// chain of left-out steps, copied relations, a cycle refused, an update
/// that stamps nothing again, and a failing child that leaves nothing. The
/// 199 it skips: the 131 steps from a define whose links pin a revision or
/// a release (the five scenarios of steps kept in a definition, a pinned
/// parentLink and the rest of the child-schema scenario), which a to-one
/// relation cannot hold, so a table's Blueprint takes inline steps alone;
/// the 25 of the scenario that composes Branches for a copied release pin;
/// the 17 of the scenario whose children compose Queue, which folds into a
/// claim (D104); the 9 of the scenario whose access policy is the test's
/// own; 10 defines the engine refused for what the child schema lacks,
/// which a type's define check on the core cannot see and the build holds;
/// 4 refusals that name Links, a required link's target delete among them;
/// the removal of Blueprint from a type with rows, which the core allows,
/// since nothing stays behind; a create whose child the store refuses for
/// its own fields; and a read of the event log with a limit. The `open`
/// steps count but compare nothing.
const FLOOR: usize = 206;

#[test]
fn blueprint_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("workqueue-blueprint"), &ported)
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
