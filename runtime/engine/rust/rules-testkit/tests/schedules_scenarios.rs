//! The hand-written scenarios of D104's step 3 Schedules
//! (`core-schedules.json`) replayed on the core: setSchedule's three kinds,
//! a cron line in the config's time zone and in its own, a repeat answered
//! unchanged and a live name replaced, each parameter refused before the
//! permission and the permission before the row; a due schedule firing on
//! the runner once, its event run, an overdue every at the next point of its
//! grid and an after finishing, and a finished name set again counting its
//! runs on; the spring gap and the autumn fold of America/New_York and
//! Europe/London, the gap's first minute among them and a schedule set just
//! before the repeated hour; pause, resume past its time and cancel, and
//! their repeats, and a replace counting the name's runs on; reportRun's
//! outcome, a stale run, no_run, a retry as the run's next attempt, a retry
//! the next run overtakes, another outcome cancelling one, an after revived
//! for its retry, and a late report of an earlier attempt and of a replaced
//! schedule's run changing nothing; endOn through a
//! transition and through a raw write its change queue carries; max, the
//! finished schedules beyond it pruned, a new version's narrower config;
//! and the define refusals, which no engine recorded, since none implements
//! Schedules. Every step must answer as the file writes, the events each
//! step's commands wrote among it, and the steps the replayer maps must not
//! drop below the floor.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-schedules.json` the replayer maps and compares: 159 of
/// its 8 scenarios' 160, the 8 `open` steps among them, which count and
/// compare nothing, and the runDues, which count once the deadlines and the
/// changes they ran answered, with the events they wrote. The one left out
/// is the raw write of Problem p2's status, which the replayer applies to the
/// row past the adapter and does not compare, its result being SQLite's.
const FLOOR: usize = 159;

#[test]
fn core_schedules_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-schedules"), &ported)
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
