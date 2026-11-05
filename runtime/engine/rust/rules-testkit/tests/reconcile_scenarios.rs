//! The hand-written scenarios of D104's step 3 Reconcile
//! (`core-reconcile.json`) replayed on the core: an observation that moves
//! a Workflow field along the fewest transitions, each through Workflow's
//! transition as the observer, a repeat, an older observation and a bad
//! parameter changing nothing, drift on a step the observer may not make,
//! on another use's veto (StateRules, another principal's lease) and with
//! no path, the stale deadline moving a row to unreachable as the runner
//! and a newer observation moving it back, a stale move a guard vetoes,
//! the path's tie broken by the config's order, and the define refusals,
//! which no engine recorded, since none implements Reconcile. Its Workflow
//! and Reconcile are anchored on fields. Every step must answer as the
//! file writes, and the steps the replayer maps must not drop below the
//! floor.
//!
//! A raw write of `observedAt` re-arming the deadline, Approvals and
//! Retries holding a step, a Workflow anchored on the type and two Workflow
//! fields cannot be written in the engine's format, or need a raw write of
//! a field anchor's column: `rules-behaviors`' tests hold them.

use superschematic_rules_testkit::{hand_written_path, ported, replay_path};

/// The steps of `core-reconcile.json` the replayer maps and compares: all
/// 76 of its 7 scenarios', the 7 `open` steps among them, which count and
/// compare nothing, and the runDues, which count once the deadlines they
/// ran answered. None is left out.
const FLOOR: usize = 76;

#[test]
fn reconcile_scenarios_replay_on_the_core() {
    let report = replay_path(&hand_written_path("core-reconcile"), &ported)
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
