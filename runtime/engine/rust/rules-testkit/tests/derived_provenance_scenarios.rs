//! The hand-written scenarios of D103's step 4 Derived and Provenance on a
//! table (`core-derived-provenance.json`, D73) replayed on the core: a
//! deriver's pull of a table's stale fields a page at a time, by row and
//! by field, its settle by hash and what a settle skips, a caller's
//! override until rederive, attempts counted into failedDerivations, a
//! field's states; Provenance's records and confidence, review, inReview
//! listed, an edit, a write acting for a subject, the gate, accept and
//! reject; with each one's define refusals, in the core's words where a
//! table's answers part from the engine's. Every step must answer as the
//! file writes, and the steps the replayer maps must not drop below the
//! floor.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-derived-provenance.json` the replayer maps and
/// compares: all 88 of its 7 scenarios', the 7 `open` steps among them,
/// which count and compare nothing. None is left out.
const FLOOR: usize = 88;

#[test]
fn derived_and_provenance_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-derived-provenance"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= FLOOR,
        "{} steps checked, under the floor of {FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}
