//! The engine's commerce recordings replayed on the core (D70, D103's step
//! 4): Stock's counts, movements and holds (`commerce-stock.json`), Holds'
//! takes, commits, releases and returns over Stock (`commerce-holds.json`),
//! and a hold's expiry through Timers (`commerce-holds-timers.json`). The
//! engine's Links link is a to-one relation on a table, so a `link` hold
//! reads the relation's own field. Every step the replayer maps must answer
//! as the engine did, each refusal compared whole, and the steps it maps
//! must not drop below each file's floor. The hand-written
//! `core-commerce.json` holds what the engine recorded none of: a link hold
//! over a relation, a direct call of the contract that changes nothing,
//! two claims for the last unit, and each one's define refusals.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path, scenario_path};

/// The steps of the hand-written `core-commerce.json` the replayer maps and
/// compares: all of its scenarios' steps, the `open` steps among them.
const CORE_COMMERCE_FLOOR: usize = 83;

#[test]
fn core_commerce_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-commerce"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= CORE_COMMERCE_FLOOR,
        "{} steps checked, under the floor of {CORE_COMMERCE_FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}

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

/// The steps of `commerce-stock.json` the replayer maps and compares.
const STOCK_FLOOR: usize = 60;

#[test]
fn stock_scenarios_replay_on_the_core() {
    replays("commerce-stock", STOCK_FLOOR);
}

/// The steps of `commerce-holds.json` the replayer maps and compares.
const HOLDS_FLOOR: usize = 92;

#[test]
fn holds_scenarios_replay_on_the_core() {
    replays("commerce-holds", HOLDS_FLOOR);
}

/// The steps of `commerce-holds-timers.json` the replayer maps and
/// compares.
const HOLDS_TIMERS_FLOOR: usize = 20;

#[test]
fn holds_timers_scenarios_replay_on_the_core() {
    replays("commerce-holds-timers", HOLDS_TIMERS_FLOOR);
}

/// The steps of `commerce-calendar.json` the replayer maps and compares:
/// Calendar's time held through a booking's Holds, its rules in their
/// order, checkHold, findAvailability over the table, the hours and
/// closures and the delete that waits for committed time.
const CALENDAR_FLOOR: usize = 95;

#[test]
fn calendar_scenarios_replay_on_the_core() {
    replays("commerce-calendar", CALENDAR_FLOOR);
}

/// The steps of the hand-written `core-calendar.json` the replayer maps and
/// compares: all of its scenarios' steps, the `open` steps among them.
const CORE_CALENDAR_FLOOR: usize = 54;

#[test]
fn core_calendar_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-calendar"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= CORE_CALENDAR_FLOOR,
        "{} steps checked, under the floor of {CORE_CALENDAR_FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}

/// The steps of `commerce-payments.json` the replayer maps and compares.
const PAYMENTS_FLOOR: usize = 67;

#[test]
fn payments_scenarios_replay_on_the_core() {
    replays("commerce-payments", PAYMENTS_FLOOR);
}

/// The steps of `commerce-payments-refunds.json` the replayer maps and
/// compares.
const PAYMENTS_REFUNDS_FLOOR: usize = 56;

#[test]
fn payments_refunds_scenarios_replay_on_the_core() {
    replays("commerce-payments-refunds", PAYMENTS_REFUNDS_FLOOR);
}

/// The steps of `commerce-payments-holds.json` the replayer maps and
/// compares.
const PAYMENTS_HOLDS_FLOOR: usize = 76;

#[test]
fn payments_holds_scenarios_replay_on_the_core() {
    replays("commerce-payments-holds", PAYMENTS_HOLDS_FLOOR);
}

/// The steps of the hand-written `core-payments.json` the replayer maps and
/// compares: all of its scenarios' steps, the `open` steps among them.
const CORE_PAYMENTS_FLOOR: usize = 84;

#[test]
fn core_payments_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-payments"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= CORE_PAYMENTS_FLOOR,
        "{} steps checked, under the floor of {CORE_PAYMENTS_FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}
