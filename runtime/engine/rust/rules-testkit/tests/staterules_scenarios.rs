//! StateRules' recorded scenarios (`core-staterules.json`) replayed on the
//! core (D103, D104 step 2): every step the replayer maps must answer as
//! the engine did, each refusal compared whole (class, code, status,
//! message and details), and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks.

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `core-staterules.json` the replayer maps and compares: 123
/// of its 126, in its 11 scenarios, their opens among them. Their type is
/// named apart from its schema (schema `invoices`, type `Invoice`), as the
/// engine's define refusals name the type and its other messages the
/// schema, so a refused define is checked again under the type's name. The
/// two scenarios that compose test.Recorder run its port, whose operation
/// writes the row's own fields through `update()` (D103's step 4, beside
/// Revisions), and the two that compose Revisions run it. The 3 it skips:
/// the define the engine's requires check refused, a type with StateRules
/// and no Workflow, which the core refuses in its own words, and the two
/// tools.describe reads, which change nothing.
const FLOOR: usize = 123;

#[test]
fn staterules_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("core-staterules"), &ported)
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
