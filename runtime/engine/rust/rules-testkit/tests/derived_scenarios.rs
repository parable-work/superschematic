//! Derived's and Provenance's recorded scenarios (`core-derived.json` and
//! `core-provenance.json`, D73) replayed on the core (D103, step 4): every
//! step the replayer maps must answer as the engine did, each refusal
//! compared whole, and the steps it maps must not drop below the floor, so
//! a regression cannot pass by skipping what it breaks. A field a Derived
//! use derives carries the JSON Schema the engine's describe gave it, which
//! a table's layout gives, so the hashes a deriver pulls are the engine's.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-derived.json` the replayer maps and compares: 38 of
/// its 39, in its 4 scenarios: the pull and the settle by hash, a caller's
/// override until rederive with a settle of an old hash skipped, a refused
/// value and a failure counting attempts into failedDerivations, and the
/// permission each operation of the table needs. The one it skips is the
/// read of the event log after a settle, which the engine logged as
/// applyDerived's own event and a table logs under settleDerivations, the
/// operation of the table as a whole that wrote the row.
const DERIVED_FLOOR: usize = 38;

/// The steps of `core-provenance.json` the replayer maps and compares: all
/// 35, in its 4 scenarios: a caller's write and Derived's recorded with its
/// confidence, a write below the threshold proposed and listed by inReview,
/// a gate held until a reviewer accepts, a write acting for a subject
/// (D77), and reject's write, which Derived records as an override.
const PROVENANCE_FLOOR: usize = 35;

fn replays(file: &str, floor: usize) {
    let report =
        replay_path(&scenario_path(file), &ported).unwrap_or_else(|error| panic!("{error}"));
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
fn derived_scenarios_replay_on_the_core() {
    replays("core-derived", DERIVED_FLOOR);
}

#[test]
fn provenance_scenarios_replay_on_the_core() {
    replays("core-provenance", PROVENANCE_FLOOR);
}
