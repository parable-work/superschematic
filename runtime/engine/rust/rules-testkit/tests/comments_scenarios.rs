//! Comments' recorded scenarios (`core-comments.json`) replayed on the core
//! (D103, step 4), and its hand-written ones (`core-comments-beside.json`),
//! which hold what the engine recorded none of: a write beside the row that
//! moves no version of it beside a Lease another principal holds, code
//! blocks, and the config members a table's Comments waits for. Every step
//! the replayer maps must answer as the engine did, or as the file writes,
//! each refusal compared whole, and the steps it maps must not drop below
//! the floor, so a regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path, scenario_path};

/// The steps of `core-comments.json` the replayer maps and compares: 276 of
/// its 502, in batch 1's scenarios whole (posts, retries, blocks, edits,
/// deletes and purges, reactions, pins, resolves, listMessages' anchors,
/// the beside writes, a delete of the row that keeps its conversation's
/// numbers, Comments joining a table with rows) and the start of the
/// others. The 226 it skips: what a table's Comments leaves out
/// (`superschematic_rules_behaviors::comments::LEFT_OUT_OPERATIONS`): the
/// scenarios of streamed replies, read state, deliveries and the inbox, and
/// code anchors with attachments, each from its first begin or its define
/// of a config member a table's Comments waits for; the two scenarios whose
/// access policy is not allowAll, which the reference host does not apply,
/// since a table's API authorizes (search across a namespace among them);
/// the reads of the event log that hold a write beside the row, which the
/// engine logged with no sequence and no log of a table's rows holds; and
/// the reads outside the reference host's surface (events.head).
const FLOOR: usize = 276;

/// The steps of `core-comments-beside.json` the replayer maps and compares:
/// all of its two scenarios', the `open` steps among them, which count and
/// compare nothing.
const BESIDE_FLOOR: usize = 29;

#[test]
fn comments_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-comments"), &ported)
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

#[test]
fn comments_hand_written_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-comments-beside"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= BESIDE_FLOOR,
        "{} steps checked, under the floor of {BESIDE_FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}
