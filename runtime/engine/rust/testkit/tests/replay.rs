//! Replays milestone 1's scenario corpus against the Rust engine (D58): the
//! files of the engine's own test files ([`ENGINE_FILES`]) on SQLite, and on
//! Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one. Every
//! step of a scenario this engine can run (no `js`, and every behavior it
//! composes registered here) must give the recorded answer; the counts of
//! what ran and what was skipped, and why, print per file.
//!
//! The corpus is `runtime/engine/testdata/scenarios`, in the tree;
//! `ENGINE_CORPUS_DIR` names another directory. A directory that is not
//! there fails the test, so the replay cannot pass by running nothing.

use std::path::{Path, PathBuf};

use superschematic_engine_testkit::{load, replay_file, Database, FileReport, ENGINE_FILES};

const CORPUS_VARIABLE: &str = "ENGINE_CORPUS_DIR";
const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// Steps whose answers differ for a reason outside this engine, by file,
/// scenario and step, with the reason. The test reports them and passes; once one
/// gives the recorded answer, the test fails until it leaves this list.
const KNOWN: [(&str, &str, &str); 1] = [(
    "core-approvals",
    "Approvals (node) > from limits a gate to the moves from its states, and excludeCreator false lets the creator sign",
    "the recording's fixed clock read 7, which the recorder wrote as {{time:1}} (a fixed clock's times are written as recorded, README), so the replayer cannot set its clock to it; the recorder also wrote the seq 7 as {{time:1}} and a veto's count 0 as {{time:2}}, a defect of milestone 1's recorder, not of the engine",
)];

fn repository() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..")
}

fn corpus() -> PathBuf {
    match std::env::var(CORPUS_VARIABLE) {
        Ok(dir) if !dir.is_empty() => PathBuf::from(dir),
        _ => repository().join("runtime/engine/testdata/scenarios"),
    }
}

fn databases() -> Vec<Database> {
    let mut databases = Vec::new();
    if cfg!(feature = "sqlite") {
        databases.push(Database::Sqlite);
    }
    if cfg!(feature = "postgres") {
        match std::env::var(DATABASE_VARIABLE) {
            Ok(url) if !url.is_empty() => databases.push(Database::Postgres { url }),
            _ => eprintln!("replay: Postgres pass skipped; set {DATABASE_VARIABLE} to run it"),
        }
    }
    databases
}

#[test]
fn the_rust_engine_gives_the_recorded_answers() {
    let dir = corpus();
    assert!(
        dir.is_dir(),
        "no scenario corpus at {} (the tree holds runtime/engine/testdata/scenarios; {CORPUS_VARIABLE} names another directory)",
        dir.display()
    );
    let mut failures = Vec::new();
    for database in databases() {
        let mut reports: Vec<FileReport> = Vec::new();
        for name in ENGINE_FILES {
            let path = dir.join(format!("{name}.json"));
            let file = load(&path).unwrap_or_else(|error| panic!("{error}"));
            reports.push(replay_file(&file, &database, &repository()));
        }
        eprintln!("\nreplay on {:?}:", database.dialect());
        for report in &reports {
            eprintln!("  {}", report.summary());
            let mut reasons: Vec<&str> = report
                .skipped
                .iter()
                .map(|(_, reason)| reason.as_str())
                .collect();
            reasons.sort_unstable();
            reasons.dedup();
            for reason in reasons {
                let count = report
                    .skipped
                    .iter()
                    .filter(|(_, why)| why == reason)
                    .count();
                eprintln!("      skipped {count}: {reason}");
            }
            for (file, scenario, reason) in KNOWN {
                if file != report.file {
                    continue;
                }
                let failed = report
                    .failures
                    .iter()
                    .any(|failure| failure.starts_with(&format!("{scenario} ")));
                let replayed = !report
                    .skipped
                    .iter()
                    .any(|(name, _)| scenario.starts_with(&format!("{name} > step")));
                if failed {
                    eprintln!("      known mismatch: {scenario}: {reason}");
                } else if replayed {
                    failures.push(format!(
                        "[{:?}] {file}: {scenario} gives the recorded answer now; take it out of KNOWN",
                        database.dialect()
                    ));
                }
            }
            for failure in &report.failures {
                if KNOWN.iter().any(|(file, scenario, _)| {
                    *file == report.file && failure.starts_with(&format!("{scenario} "))
                }) {
                    continue;
                }
                failures.push(format!(
                    "[{:?}] {}: {failure}",
                    database.dialect(),
                    report.file
                ));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} steps gave other answers:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
