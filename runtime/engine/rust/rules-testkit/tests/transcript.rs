//! The checked-in transcripts of the recorded scenarios the core runs
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, D103), Workflow's
//! and Timers': the core's commands and what each must answer, which every
//! language's adapter replays against tables of its own. The test fails
//! when a file is not what the writer writes from its recording, so a
//! change to the core, a behavior or the replayer that changes an answer
//! changes the file in the same commit. `UPDATE_TRANSCRIPTS=1 cargo test -p
//! superschematic-rules-testkit --test transcript` rewrites both; review
//! the diff before keeping them.

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
/// 420: 50 define and publish checks and 51 commands, 40 creates, updates,
/// invokes and a delete and the 11 deadlines the runner's sweeps ran, each
/// under the step number of the `runner.runDue` that ran it (so the 101
/// steps hold 98 of the recording's), in the 12 scenarios the core an
/// adapter embeds runs. Six hold none: two compose a test behavior
/// (test.Bounce, test.Hold), which the reference host runs as a fixture;
/// two compose StateRules or Reactions, which the core does not run; one's
/// access policy is the test's own; and one names namespaces, which the
/// reference host does not keep. The rest of the 322 left out are opens,
/// reads of rows, of the change log and of the runner's status, the sweeps
/// that ran nothing, the describes, and the steps from a publish whose
/// Timers change the engine rewrote rows for. An adapter's replay holds its
/// own count to this one.
const TIMERS_FLOOR: usize = 101;

#[test]
fn the_checked_in_transcripts_are_what_the_writer_writes() {
    for (name, floor) in [
        ("core-workflow", WORKFLOW_FLOOR),
        ("core-timers", TIMERS_FLOOR),
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
