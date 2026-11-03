//! Reactions' recorded scenarios (`core-reactions.json` and
//! `core-reactions-changes.json`) replayed on the core (D103): every step the
//! replayer maps must answer as the engine did, each refusal compared whole
//! (class, code, status, message and details), and the steps it maps must
//! not drop below each file's floor, so a regression cannot pass by skipping
//! what it breaks. The engine's Links is a to-one relation on a table, and
//! the writes the runner's sweep heard are the change commands a table's
//! change queue runs; `core-reactions-holds-revised.json` waits for Rollups,
//! Revisions and Branches on the core (D103's order of the work, step 4)
//! and is not replayed.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-reactions.json` the replayer maps and compares: 443
/// of its 601. The 158 it skips: 107 reads of the engine's event log that
/// hold a Links operation or a move a rule made, which a table writes as an
/// update of the relation's column and of the row's owned fields, and one
/// read past a cursor, which the reference host does not take; the 28 steps
/// of the scenario that composes Dependencies, which the core does not run;
/// six refused defines of a type that lists Links, whose paths and words
/// name the engine's Links where a table has relations; one define the
/// engine's requires check refused; the runner's status three times; and
/// the twelve steps after a runDue whose recording failed, from which the
/// engine's subscription heard nothing. Each runDue counts as checked once
/// the changes and deadlines it ran answered, the engine's counters being
/// its store's.
const REACTIONS_FLOOR: usize = 443;

/// The steps of `core-reactions-changes.json` the replayer maps and
/// compares: 65 of its 151. The 86 it skips: 32 of the scenarios that
/// compose a test behavior written in TypeScript (Workflow.Tally,
/// test.Score) and 22 of the ones that compose Revisions, which the core
/// does not run; the 14 of the scenario whose engine keeps large values in a
/// value store and the 14 of the one whose runner stops a chain at its depth
/// limit, which a table has no counterpart of; and four reads of the
/// engine's event log that hold a move a rule made.
const CHANGES_FLOOR: usize = 65;

// replays holds a recording's replay to the engine's answers and its floor.
fn replays(name: &str, floor: usize) {
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
}

#[test]
fn reactions_scenarios_replay_on_the_core() {
    replays("core-reactions", REACTIONS_FLOOR);
}

#[test]
fn reactions_changes_scenarios_replay_on_the_core() {
    replays("core-reactions-changes", CHANGES_FLOOR);
}
