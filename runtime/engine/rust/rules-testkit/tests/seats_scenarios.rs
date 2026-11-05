//! The hand-written scenarios of D104's step 3 Seats (`core-seats.json`)
//! replayed on the core: a caller taking a seat for itself and seating or
//! unseating another, a full seat refused and displaced, a repeat take and
//! a leave of a seat not held answered unchanged, a field's subject seated,
//! counted, never displaced and never leaving, an update that gives the
//! field a subject not seated held to the seat's max (refused, and
//! displacing), and `by` naming a seat on a Workflow transition and a
//! StateRules state, with `overridePermission` past it; with each one's define refusals, which no engine recorded,
//! since none implements Seats. Every step must answer as the file writes,
//! and the steps the replayer maps must not drop below the floor.
//!
//! A seat the config drops, a lowered max, a subject in either UUID form,
//! a caller with no subject who names one, and a take on a leased row
//! cannot be written in the engine's format, or need a new version or a
//! UUID field: `rules-behaviors`' tests hold them.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-seats.json` the replayer maps and compares: all 97 of
/// its 7 scenarios', the 7 `open` steps among them, which count and compare
/// nothing. None is left out.
const FLOOR: usize = 97;

#[test]
fn seats_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-seats"), &ported)
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
