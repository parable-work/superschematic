//! Step 5's question (D104's "What the built steps left"): whether
//! Constants' constancy and `oneWay`, and StateRules' required and frozen
//! fields, become declarative rules. Each is written here in the vocabulary
//! (D103's step 5), a declaration with no Rust: the behavior's own
//! declaration file, its configSchema and its vetoes, with `rules` that
//! read the use's config (Constants' `constant`, `oneWay` and `permission`;
//! StateRules' `states`, `status` and `permission`, each `{ "config": ...
//! }`). On a core whose behavior is that declaration in place of the Rust
//! one, every recording and hand-written file that composes it replays as
//! it does on the core's own: no step answers otherwise, the same steps are
//! checked and skipped, and the transcript each writes holds the same steps
//! with the same answers. The Rust behaviors stay until the integration
//! decides (D104).

use std::path::{Path, PathBuf};

use serde_json::Value;
use superschematic_engine_testkit::{load, ScenarioFile};
use superschematic_rules::Core;
use superschematic_rules_behaviors::{constants, staterules, Declarative};
use superschematic_rules_testkit::{
    hand_written_path, hand_written_transcript, ported, replay_path, scenario_path, transcript,
    Transcript,
};

/// A behavior the vocabulary states, by its name and its Rust one's
/// declaration file, and the files that compose it, each a recording or a
/// hand-written file.
struct Case {
    name: &'static str,
    declaration: &'static str,
    recordings: &'static [&'static str],
    hand_written: &'static [&'static str],
}

const CASES: [Case; 2] = [
    Case {
        name: constants::NAME,
        declaration: constants::DECLARATION,
        recordings: &["core-constants", "core-behaviors", "workqueue-blueprint"],
        hand_written: &["core-freezes-and-sign-offs", "core-derived-provenance"],
    },
    Case {
        name: staterules::NAME,
        declaration: staterules::DECLARATION,
        recordings: &[
            "core-staterules",
            "core-timers",
            "core-behaviors",
            "workqueue-retries",
        ],
        hand_written: &[
            "core-freezes-and-sign-offs",
            "core-seats",
            "core-derived-provenance",
            "core-reconcile",
        ],
    },
];

/// The behavior's declaration with its rules
/// (`runtime/engine/testdata/rules/declarations/<name>.behavior.json`).
fn declared(name: &str) -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/rules/declarations")
        .join(format!("{name}.behavior.json"));
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the declaration file"))
        .expect("the declaration file reads")
}

/// The core with the behavior's declaration in the Rust one's place.
fn declarative(name: &str) -> Core {
    let declared =
        Declarative::from_document(&declared(name)).unwrap_or_else(|error| panic!("{error}"));
    let mut core = ported();
    core.push(declared);
    core
}

/// Each file that composes a case's behavior, with the writer of its
/// transcript.
type Writer = fn(&ScenarioFile, &dyn Fn() -> Core) -> Result<Transcript, String>;

fn files(case: &Case) -> Vec<(PathBuf, Writer)> {
    let recordings = case
        .recordings
        .iter()
        .map(|name| (scenario_path(name), transcript as Writer));
    let hand_written = case
        .hand_written
        .iter()
        .map(|name| (hand_written_path(name), hand_written_transcript as Writer));
    recordings.chain(hand_written).collect()
}

/// Each declarative file is its behavior's own declaration with rules
/// added, so a client reads one behavior whichever runs it, and the core
/// reads the declaration it reads from the Rust one.
#[test]
fn each_declarative_file_is_its_behaviors_declaration_with_rules() {
    for case in &CASES {
        let mut file = declared(case.name);
        let rules = file
            .as_object_mut()
            .and_then(|file| file.remove("rules"))
            .expect("it states rules");
        assert!(rules.is_object());
        let own: Value = serde_json::from_str(case.declaration).expect("the behavior's file");
        assert_eq!(file, own, "{}", case.name);
        assert_eq!(
            declarative(case.name).declaration(case.name),
            ported().declaration(case.name),
            "{}",
            case.name
        );
    }
}

/// Every file that composes the behavior answers alike on its declaration.
#[test]
fn each_replays_its_recordings_alike_as_a_declaration() {
    for case in &CASES {
        let make = || declarative(case.name);
        for (path, _) in files(case) {
            let rust = replay_path(&path, &ported).unwrap_or_else(|error| panic!("{error}"));
            let declared = replay_path(&path, &make).unwrap_or_else(|error| panic!("{error}"));
            println!("{} as a declaration: {}", case.name, declared.summary());
            assert!(
                declared.mismatches().is_empty(),
                "the declarative {} answered otherwise than {}:\n{}",
                case.name,
                path.display(),
                declared.details()
            );
            assert_eq!(
                (declared.checked(), declared.skipped()),
                (rust.checked(), rust.skipped()),
                "{}: the declarative {} checks and skips the steps the Rust one does",
                path.display(),
                case.name
            );
        }
    }
}

/// The transcripts the two cores write are one: the same steps, each with
/// the same command and the same answer.
#[test]
fn each_writes_its_transcripts_alike_as_a_declaration() {
    for case in &CASES {
        let make = || declarative(case.name);
        for (path, write) in files(case) {
            let file = load(&path).unwrap_or_else(|error| panic!("{error}"));
            let rust = write(&file, &ported).unwrap_or_else(|error| panic!("{error}"));
            let declared = write(&file, &make).unwrap_or_else(|error| panic!("{error}"));
            assert!(
                declared.scenarios == rust.scenarios,
                "{}: the declarative {} writes another transcript",
                path.display(),
                case.name
            );
            println!(
                "{} as a declaration: {} writes the same {} steps",
                case.name,
                path.display(),
                rust.steps()
            );
        }
    }
}
