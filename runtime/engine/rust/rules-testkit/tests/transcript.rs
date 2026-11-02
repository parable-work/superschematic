//! The checked-in transcripts of the recorded scenarios the core runs
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, D103),
//! Workflow's, Timers' and Reactions' two: the core's commands and what each
//! must answer, which every language's adapter replays against tables of
//! its own. The test fails when a file is not what the writer writes from
//! its recording, so a change to the core, a behavior or the replayer that
//! changes an answer changes the file in the same commit.
//! `UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test
//! transcript` rewrites them; review the diff before keeping them.

use std::fs;

use superschematic_engine_testkit::load;
use superschematic_rules_testkit::{
    ported, scenario_path, transcript, transcript_path, Transcript, TranscriptStep,
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
/// 420: 50 define and publish checks and 75 commands, 40 creates, updates,
/// invokes and a delete, the 10 deadlines the runner's sweeps ran and the 25
/// changes they ran, each a write of a row of a type Timers hears, each
/// under the step number of the `runner.runDue` that ran it, in the 12
/// scenarios the core an adapter embeds runs. A change runs Timers'
/// reconcile on the row as it is, which writes nothing for a write the
/// core made, deletes nothing but the deadlines of a row that is gone, and,
/// in one scenario, cancels a deadline whose until a version published
/// after the write added, before it falls due. Six scenarios hold none: two
/// compose a test behavior (test.Bounce, test.Hold), which the reference
/// host runs as a fixture; one composes StateRules, which the core does not
/// run; one's Reactions rule names a field Timers computes at a read, which
/// a table's rule waits for a later step to hear; one's access policy is
/// the test's own; and one names namespaces, which the reference host does
/// not keep. The rest of the 317 left out are opens, reads of rows, of the
/// change log and of the runner's status, the sweeps that ran nothing, the
/// describes, and the steps from a publish whose Timers change the engine
/// rewrote rows for. An adapter's replay holds its own count to this one.
const TIMERS_FLOOR: usize = 125;

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

#[test]
fn the_checked_in_transcripts_are_what_the_writer_writes() {
    for (name, floor) in [
        ("core-workflow", WORKFLOW_FLOOR),
        ("core-timers", TIMERS_FLOOR),
        ("core-reactions", REACTIONS_FLOOR),
        ("core-reactions-changes", REACTIONS_CHANGES_FLOOR),
    ] {
        let written = written(name);
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

// written writes a recording's transcript, rewrites the checked-in file when
// UPDATE_TRANSCRIPTS=1, and holds the checked-in file to it.
fn written(name: &str) -> Transcript {
    let file = load(&scenario_path(name)).unwrap_or_else(|error| panic!("{error}"));
    let written = transcript(&file, &ported).unwrap_or_else(|error| panic!("{error}"));
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
