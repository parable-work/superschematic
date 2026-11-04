//! The checked-in transcripts of the recorded scenarios the core runs
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, D103),
//! Workflow's, Timers', Reactions' two, Constants', StateRules' and
//! Approvals': the core's commands and what each must answer, which every language's adapter replays against tables of
//! its own. The test fails when a file is not what the writer writes from
//! its recording, so a change to the core, a behavior or the replayer that
//! changes an answer changes the file in the same commit.
//! `UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test
//! transcript` rewrites them; review the diff before keeping them. So are
//! the transcripts of the hand-written scenario files
//! (`runtime/engine/testdata/rules/scenarios`, D104), which hold each
//! command's row version beside what it answers.

use std::collections::BTreeMap;
use std::fs;

use superschematic_engine_testkit::{load, ScenarioFile};
use superschematic_rules::Core;
use superschematic_rules_testkit::{
    hand_written_path, hand_written_transcript, ported, scenario_path, transcript, transcript_path,
    Transcript, TranscriptStep,
};

/// The steps `core-workflow.transcript.json` holds, of `core-workflow.json`'s
/// 158: each one the reference host maps onto the core (61 define and
/// publish checks, 34 creates, updates, deletes and invokes, and the test's
/// raw write of Workflow's column) in the 14 scenarios the core an adapter
/// embeds runs. The two scenarios that need a fixture (test.Dispatch, and a
/// permission matcher of the test's own) hold none; the rest of the 62 left
/// out are opens, reads of the host's, the outbox's reads and the two
/// writes the store's own-field validation refuses. An adapter's replay
/// holds its own count to this one, so a regression cannot pass by
/// skipping.
const WORKFLOW_FLOOR: usize = 96;

/// The steps `core-timers.transcript.json` holds, of `core-timers.json`'s
/// 420: 54 define and publish checks and 85 commands, 44 creates, updates,
/// invokes and a delete, the 12 deadlines the runner's sweeps ran and the
/// 29 changes they ran, each a write of a row of a type Timers hears, each
/// under the step number of the `runner.runDue` that ran it, in the 13
/// scenarios the core an adapter embeds runs, the one that composes
/// StateRules among them (D104, step 2): a fire whose move StateRules
/// vetoes is recorded vetoed. A change runs Timers' reconcile on the row
/// as it is, which writes nothing for a write the core made, deletes
/// nothing but the deadlines of a row that is gone, and, in one scenario,
/// cancels a deadline whose until a version published after the write
/// added, before it falls due. Five scenarios hold none: two compose a test
/// behavior (test.Bounce, test.Hold), which the reference host runs as a
/// fixture; one's Reactions rule names a field Timers computes at a read,
/// which a table's rule waits for a later step to hear; one's access policy
/// is the test's own; and one names namespaces, which the reference host
/// does not keep. The rest of the 307 left out are opens, reads of rows, of
/// the change log and of the runner's status, the sweeps that ran nothing,
/// the describes, and the steps from a publish whose Timers change the
/// engine rewrote rows for. An adapter's replay holds its own count to this
/// one.
const TIMERS_FLOOR: usize = 139;

/// The steps `core-reactions.transcript.json` holds, of
/// `core-reactions.json`'s 601: 110 define and publish checks and 476
/// commands, 224 creates, updates, invokes and a delete, the engine's link
/// and unlink among them as updates of a relation's column, and the 252
/// changes
/// the runner's sweeps ran, each a write of a row of a type Reactions hears
/// or listens to, each under the step number of the `runner.runDue` that ran
/// it, with the rows a rule moved as its `others`. The rest of the 215 left
/// out are opens, reads of rows and of the event log, the sweeps that ran
/// nothing, and what the replay skips: a scenario that composes
/// Dependencies, the defines that name Links, the runner's status, and the
/// steps after a sweep whose recording failed. An adapter's replay holds its
/// own count to this one.
const REACTIONS_FLOOR: usize = 586;

/// The steps `core-reactions-changes.transcript.json` holds, of
/// `core-reactions-changes.json`'s 151: 12 define and publish checks and 48
/// commands, 24 creates, updates, invokes and a delete and the 24 changes
/// the runner's sweeps ran. The rest of the 104 left out are what the replay
/// skips (Revisions, a test behavior, the value store, a runner's depth
/// limit), opens, reads of rows and of the event log. An adapter's replay
/// holds its own count to this one.
const REACTIONS_CHANGES_FLOOR: usize = 60;

/// The steps `core-workflow-principals.transcript.json` holds, of the
/// hand-written `core-workflow-principals.json`'s 122 (D104): 34 define
/// and publish checks, twelve of them refused, and 58 commands, creates and
/// transitions, five of them repeats answered unchanged, each holding the
/// version of its row's command before it, in the 9 scenarios the core an
/// adapter embeds runs. test.Gate's scenario holds none, since the
/// reference host runs test.Gate as a fixture; the rest of the 30 left out
/// are the opens, the reads of rows and the read of the event log. An
/// adapter's replay holds its own count to this one.
const WORKFLOW_PRINCIPALS_FLOOR: usize = 92;

/// The steps `core-constants.transcript.json` holds, of
/// `core-constants.json`'s 65: 19 define and publish checks and 20 creates
/// and updates, in the four scenarios that compose Constants alone or with
/// Workflow. The two that compose Comments and Revisions, or Variants,
/// which the core does not run, hold none; the rest of the 26 left out are
/// opens and reads of rows and of the types the host keeps. An adapter's
/// replay holds its own count to this one.
const CONSTANTS_FLOOR: usize = 39;

/// The steps `core-staterules.transcript.json` holds, of
/// `core-staterules.json`'s 126: 33 define and publish checks and 43
/// creates, updates and transitions, in the seven scenarios that compose
/// StateRules and Workflow alone, the type named apart from its schema
/// (schema `invoices`, type `Invoice`) checked under the type's name for a
/// define's refusal. The two that compose test.Recorder, which no fixture
/// ports, and the two that compose Revisions, which the core does not run,
/// hold none; the rest of the 50 left out are opens, reads of rows and of
/// the event log, and the define the engine's requires check refused. An
/// adapter's replay holds its own count to this one.
const STATERULES_FLOOR: usize = 76;

/// The steps `core-approvals.transcript.json` holds, of
/// `core-approvals.json`'s 198: 29 define and publish checks and 93
/// commands, 88 creates, an update, a delete, transitions and the three
/// operations, and the 5 changes the runner's sweep ran for the Reactions
/// rule that moves a row into a gated state, in nine scenarios, each
/// recorded type given the own field createdBy the engine kept beside an
/// instance. The scenario that composes Comments and Revisions holds none,
/// and the new-version one stops at the publish that removes a gate on a
/// type with rows, whose sign-offs the engine consumed there and the core
/// consumes at each row's next write; the rest of the 80 left out are
/// opens, reads of rows and of the event log, and the define the engine's
/// requires check refused. An adapter's replay holds its own count to this
/// one.
const APPROVALS_FLOOR: usize = 122;

/// The steps `core-freezes-and-sign-offs.transcript.json` holds, of the
/// hand-written `core-freezes-and-sign-offs.json`'s 138 (D104): 40 define
/// and publish checks, fourteen of them refused, and 75 creates, updates,
/// transitions and sign-offs, each holding its row's version after the
/// step, in its 12 scenarios; none is a repeat. The 23 left out are the
/// opens and the reads of rows. An adapter's replay holds its own count to
/// this one.
const FREEZES_AND_SIGN_OFFS_FLOOR: usize = 115;

#[test]
fn the_checked_in_transcripts_are_what_the_writer_writes() {
    for (name, floor) in [
        ("core-workflow", WORKFLOW_FLOOR),
        ("core-timers", TIMERS_FLOOR),
        ("core-reactions", REACTIONS_FLOOR),
        ("core-reactions-changes", REACTIONS_CHANGES_FLOOR),
        ("core-constants", CONSTANTS_FLOOR),
        ("core-staterules", STATERULES_FLOOR),
        ("core-approvals", APPROVALS_FLOOR),
    ] {
        let written = written(name, &scenario_path(name), transcript);
        let kinds = |kind: fn(&TranscriptStep) -> bool| {
            written
                .scenarios
                .iter()
                .flat_map(|scenario| &scenario.steps)
                .filter(|step| kind(step))
                .count()
        };
        println!(
            "{}: {} scenarios, {} steps ({} commands, {} checks, {} raw writes), {} left out",
            written.file,
            written.scenarios.len(),
            written.steps(),
            kinds(|step| matches!(step, TranscriptStep::Command { .. })),
            kinds(|step| matches!(step, TranscriptStep::Check { .. })),
            kinds(|step| matches!(step, TranscriptStep::Raw { .. })),
            written.skipped()
        );
        assert!(
            written.steps() >= floor,
            "{name}'s transcript holds {} steps, under the floor of {floor}",
            written.steps()
        );
    }
}

/// A hand-written file's transcript holds each command's row version after
/// the step, and a command a repeat answered unchanged holds the version of
/// its row's command before it: it wrote nothing. A file holds repeats, or
/// holds none, as its entry says.
#[test]
fn the_hand_written_transcripts_are_what_the_writer_writes() {
    for (name, floor, holds_repeats) in [
        ("core-workflow-principals", WORKFLOW_PRINCIPALS_FLOOR, true),
        (
            "core-freezes-and-sign-offs",
            FREEZES_AND_SIGN_OFFS_FLOOR,
            false,
        ),
    ] {
        let written = written(name, &hand_written_path(name), hand_written_transcript);
        let mut repeats = 0;
        for scenario in &written.scenarios {
            // The version each row's last command left it at.
            let mut last: BTreeMap<(String, String), Option<i64>> = BTreeMap::new();
            for step in &scenario.steps {
                let TranscriptStep::Command {
                    type_name,
                    command,
                    expect,
                    ..
                } = step
                else {
                    continue;
                };
                let at = (
                    type_name.clone(),
                    command.action.key().unwrap_or_default().to_owned(),
                );
                assert!(
                    expect.version.is_some() || expect.row.is_none(),
                    "{}: a command on a row that is there holds its version",
                    scenario.name
                );
                if expect.result.get("unchanged") == Some(&serde_json::Value::Bool(true)) {
                    repeats += 1;
                    assert_eq!(
                        Some(&expect.version),
                        last.get(&at),
                        "{}: a repeat of {} {} moved its version",
                        scenario.name,
                        at.0,
                        at.1
                    );
                }
                last.insert(at, expect.version);
            }
        }
        let checks = written
            .scenarios
            .iter()
            .flat_map(|scenario| &scenario.steps)
            .filter(|step| matches!(step, TranscriptStep::Check { .. }))
            .count();
        println!(
            "{}: {} scenarios, {} steps ({} commands, {} checks, {repeats} repeats), {} left out",
            written.file,
            written.scenarios.len(),
            written.steps(),
            written.steps() - checks,
            checks,
            written.skipped()
        );
        assert_eq!(
            repeats > 0,
            holds_repeats,
            "{name}'s transcript holds {repeats} repeats"
        );
        assert!(
            written.steps() >= floor,
            "{name}'s transcript holds {} steps, under the floor of {floor}",
            written.steps()
        );
    }
}

// The writer of a scenario file's transcript.
type Writer = fn(&ScenarioFile, &dyn Fn() -> Core) -> Result<Transcript, String>;

// written writes a scenario file's transcript, rewrites the checked-in file
// when UPDATE_TRANSCRIPTS=1, and holds the checked-in file to it.
fn written(name: &str, source: &std::path::Path, write: Writer) -> Transcript {
    let file = load(source).unwrap_or_else(|error| panic!("{error}"));
    let written = write(&file, &ported).unwrap_or_else(|error| panic!("{error}"));
    let text = written.render();
    let path = transcript_path(name);
    if std::env::var("UPDATE_TRANSCRIPTS").is_ok_and(|value| value == "1") {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).expect("create the transcripts' directory");
        }
        fs::write(&path, &text).expect("write the transcript");
    }
    let checked_in = fs::read_to_string(&path).unwrap_or_default();
    assert!(
        checked_in == text,
        "{} is not what the writer writes from {name}.json; run UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript and review the diff",
        path.display()
    );
    written
}
