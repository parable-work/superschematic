//! Budget's recorded scenarios (`workqueue-budget.json`) replayed on the
//! core (D103, D104 step 2): every step the replayer maps must answer as
//! the engine did, each refusal compared whole (class, code, status,
//! message and details), and the steps it maps must not drop below the
//! floor, so a regression cannot pass by skipping what it breaks. A
//! meter's scope, a link of the engine's Links, is a to-one relation on a
//! table, whose operations run on the scope's row through `call_on`; the
//! lease a reservation lives within is a table's Lease; and Comments,
//! composed beside Budget only as another behavior of the scope type in
//! the two config scenarios, is incidental (`INCIDENTAL`).

use superschematic_rules_testkit::{ported, replay_path, scenario_path};

/// The steps of `workqueue-budget.json` the replayer maps and compares:
/// 613 of its 796, in the 31 scenarios that compose no Queue. The 183 it
/// skips: the 132 steps of the five scenarios claimed through Queue, which
/// the core does not run; the 25 steps of the two limitField scenarios
/// from their setLimit of a meter whose limit an own field holds, which the
/// engine wrote through update() and a table sets by an update of the
/// field (D104); 12 of the config scenario, the defines that name Links
/// and from its link that holds a list of targets, which a to-one relation
/// cannot; three Links' own validations, which a relation's foreign key
/// holds; six reads of the engine's store; and three reads of the event
/// log, a Links operation and two holding a scope's writes, which the
/// engine logged as the scope's own operations and a table's change set
/// logs under the command that wrote them. The `open` steps count but
/// compare nothing.
const FLOOR: usize = 613;

#[test]
fn budget_scenarios_replay_on_the_core() {
    let report = replay_path(&scenario_path("workqueue-budget"), &ported)
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
