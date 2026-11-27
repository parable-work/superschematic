//! The hand-written scenarios of the declarative vocabulary (D103's step 5,
//! `core-declarative.json`) replayed on the core: test.Votes, a behavior
//! whose declaration states its rules and which no Rust implements, its
//! votes upserted by the caller and held to the config's maxScore, its
//! derived count, sum, average, highest, lowest and filtered count, its
//! guards over the row, the Workflow and the caller, its counter, its move,
//! its two reactions heard from the change queue, and the owner and title
//! Constants' and StateRules' rules hold, with the define refusals. Every
//! step must answer as the file writes, and the steps the replayer maps
//! must not drop below the floor.

use std::path::{Path, PathBuf};

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-declarative.json` the replayer maps and compares: all
/// of its 4 scenarios', the 4 `open` steps among them, which count and
/// compare nothing. None is left out.
const FLOOR: usize = 52;

/// The steps of the acme example's `acme-rating.json` the replayer maps
/// and compares: all of its 4 scenarios', the 4 `open` steps among them.
pub const ACME_RATING_FLOOR: usize = 33;

/// The acme example's scenarios of acme.Rating as a declarative behavior
/// (`examples/acme-schematic/testdata/rules/acme-rating.json`), whose
/// declaration is the extension's own file.
pub fn acme_rating_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../examples/acme-schematic/testdata/rules/acme-rating.json")
}

/// acme.Rating with no crate (D103's step 5): a shopper's rating upserted
/// into the item's ratings, its stars held to 1 to maxStars, the count and
/// average derived from the rows, a caller with no subject refused, and a
/// new version that raises maxStars allowed and one that lowers it refused.
#[test]
fn acme_rating_replays_on_the_core_as_a_declaration() {
    let report =
        replay_path(&acme_rating_path(), &ported).unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= ACME_RATING_FLOOR,
        "{} steps checked, under the floor of {ACME_RATING_FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}

#[test]
fn declarative_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-declarative"), &ported)
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
