//! The hand-written scenarios of Recurrence (`core-recurrence.json`, D152,
//! #601) replayed on the core: setRecurrence of a weekly calendar rule in
//! America/New_York planned to the horizon across the change of the clocks,
//! one due date moved off a holiday of the row's calendar, its parameters,
//! permission and repeat; the open, remind, due, overdue and plan deadlines
//! the runner fires, each moving the occurrences on by its time and
//! emitting opened, reminded, due and missed once, a late fire raising a
//! due and a miss together; complete with what and who completed it and
//! its repeat, skip, moveOne and listOccurrences, and the vetoes not_open,
//! no_occurrence and occurrence_finished; an afterDone recurrence dated from
//! a missed due and again from a late completion, and a rule change that
//! keeps the open occurrence; a relative recurrence read through its
//! relation, planned when the horizon reaches it, dated again by
//! setRecurrence with the rule it holds after the target's date moves, and
//! planning nothing once its relation is cleared; and the define refusals,
//! which no engine recorded, since none implements Recurrence. Every step
//! must answer as the file writes, the events each step's commands wrote
//! among it, and the steps the replayer maps must not drop below the floor.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-recurrence.json` the replayer maps and compares: all
/// 86 of its 4 scenarios', the 4 `open` steps among them, which count and
/// compare nothing, and the runDues, which count once the deadlines they
/// ran answered, with the events they wrote.
const FLOOR: usize = 86;

#[test]
fn core_recurrence_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-recurrence"), &ported)
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
