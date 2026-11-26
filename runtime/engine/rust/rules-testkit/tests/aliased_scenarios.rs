//! The hand-written scenarios of D147's Aliased (`core-aliased.json`)
//! replayed on the core: a create that gives the name, and a name another
//! row holds, as its name or an alias, in its normalized form, refused
//! with that row's key in a create, a rename and an addAlias; rename
//! keeping the old name as an alias, a rename to an alias, one of only the
//! case and one answered unchanged; addAlias and removeAlias under
//! normalize slug, a removed alias taken by another row and the name
//! refused; resolve on the table; a delete that frees the row's names; and
//! the define refusals, which no engine recorded, since none implements
//! Aliased. Every step must answer as the file writes, and the steps the
//! replayer maps must not drop below the floor.
//!
//! A name whose holder is gone, two creates of one name at once and the
//! parameters' checks need a host of the test's own: `rules-behaviors`'
//! tests hold them.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-aliased.json` the replayer maps and compares: all 54
/// of its 5 scenarios', the 5 `open` steps among them, which count and
/// compare nothing. None is left out.
const FLOOR: usize = 54;

#[test]
fn aliased_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-aliased"), &ported)
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
