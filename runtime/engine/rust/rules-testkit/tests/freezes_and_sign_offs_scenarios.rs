//! The hand-written scenarios of D104's step 2 upgrades
//! (`core-freezes-and-sign-offs.json`) replayed on the core: Constants'
//! fields that move one way (`oneWay`: up and down, a boolean, a number
//! and a date-time, the permission past it, no value below every value, a
//! create), StateRules' subjects who may change a frozen field (`by`, on a
//! field and on createdBy, required held for them all the same),
//! Approvals' gates whose sign-offs a change of chosen fields consumes
//! (`resetOn`, `[]`, resetOnChange) and that pin the version their signer
//! read (`pinned`, and a version on a gate that is not), and each one's
//! define refusals, which the engine recorded none of. Every step must
//! answer as the file writes, and the steps the replayer maps must not drop
//! below the floor.
//!
//! A config's `status`, which names the Workflow of a type with several,
//! cannot be written in the engine's format, whose type has one Workflow:
//! `rules-behaviors`' tests hold it.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-freezes-and-sign-offs.json` the replayer maps and
/// compares: all 138 of its 12 scenarios', the 12 `open` steps among them,
/// which count and compare nothing. None is left out.
const FLOOR: usize = 138;

#[test]
fn freezes_and_sign_offs_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-freezes-and-sign-offs"), &ported)
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
