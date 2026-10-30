//! The checked-in transcript of Workflow's recorded scenarios
//! (`runtime/engine/testdata/rules/core-workflow.transcript.json`, D103):
//! the core's commands and what each must answer, which every language's
//! adapter replays against tables of its own. The test fails when the file
//! is not what the writer writes from `core-workflow.json`, so a change to
//! the core, a behavior or the replayer that changes an answer changes the
//! file in the same commit. `UPDATE_TRANSCRIPTS=1 cargo test -p
//! superschematic-rules-testkit --test transcript` rewrites it; review the
//! diff before keeping it.

use std::fs;

use superschematic_engine_testkit::load;
use superschematic_rules_testkit::{
    ported, scenario_path, transcript, transcript_path, TranscriptStep,
};

/// The steps the transcript holds, of `core-workflow.json`'s 158: each one
/// the reference host maps onto the core (61 define and publish checks, 34
/// creates, updates, deletes and invokes, and the test's raw write of
/// Workflow's column) in the 14 scenarios the core an adapter embeds runs.
/// The two scenarios that need a fixture (test.Dispatch, and a permission
/// matcher of the test's own) hold none; the rest of the 62 left out are
/// opens, reads of the host's, the outbox's reads and the two writes the
/// store's own-field validation refuses. An adapter's replay holds its own
/// count to this one, so a regression cannot pass by skipping.
const FLOOR: usize = 96;

#[test]
fn the_checked_in_transcript_is_what_the_writer_writes() {
    let file = load(&scenario_path("core-workflow")).unwrap_or_else(|error| panic!("{error}"));
    let written = transcript(&file, &ported).unwrap_or_else(|error| panic!("{error}"));
    let text = written.render();
    let path = transcript_path("core-workflow");
    if std::env::var("UPDATE_TRANSCRIPTS").is_ok_and(|value| value == "1") {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).expect("create the transcripts' directory");
        }
        fs::write(&path, &text).expect("write the transcript");
    }
    let checked_in = fs::read_to_string(&path).unwrap_or_default();
    assert!(
        checked_in == text,
        "{} is not what the writer writes from core-workflow.json; run UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript and review the diff",
        path.display()
    );
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
        written.steps() >= FLOOR,
        "the transcript holds {} steps, under the floor of {FLOOR}",
        written.steps()
    );
}
