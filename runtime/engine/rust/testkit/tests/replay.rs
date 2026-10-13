//! Replays milestone 1's scenario corpus against the Rust engine (D58): the
//! files of the engine's own test files ([`ENGINE_FILES`]) on SQLite, and on
//! Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one. Every
//! step of a scenario this engine can run (no behavior, no `js`) must give
//! the recorded answer; the counts of what ran and what was skipped, and
//! why, print per file.
//!
//! The corpus is `runtime/engine/testdata/scenarios`, which milestone 1's
//! pull request adds; `ENGINE_CORPUS_DIR` names another directory. Until the
//! corpus is on this branch the test finds no directory and skips, saying
//! so loudly, which is what the `engine-rust` CI job does until then.

use std::path::{Path, PathBuf};

use superschematic_engine_testkit::{load, replay_file, Database, FileReport, ENGINE_FILES};

const CORPUS_VARIABLE: &str = "ENGINE_CORPUS_DIR";
const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// Steps whose answers differ for a reason outside this engine, by file,
/// scenario and step, with the reason. The test reports them and passes; once one
/// gives the recorded answer, the test fails until it leaves this list.
const KNOWN: [(&str, &str, &str); 2] = [
    (
        "registry",
        "schema documents (node) > a document the loader refuses is refused with its issues, and nothing is written > step 3",
        "JSON.parse's wording: the corpus was recorded on Node.js 24, whose V8 says \"Expected double-quoted property name in JSON at position 19 (line 1 column 20)\"; the schema reader (superschematic-schema-ir's JSON reader) writes an earlier V8's \"Unexpected end of JSON input\"",
    ),
    (
        "registry",
        "schema documents (node) > with the core meta-schema, the loader itself refuses a behavior the core does not declare > step 2",
        "the core registry's meta-schema on this branch predates D56's behaviors (Approvals, Numbering, StateRules, Timers), which the corpus's branch declares; it matches once the branches meet",
    ),
];

fn repository() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..")
}

fn corpus() -> Option<PathBuf> {
    let dir = match std::env::var(CORPUS_VARIABLE) {
        Ok(dir) if !dir.is_empty() => PathBuf::from(dir),
        _ => repository().join("runtime/engine/testdata/scenarios"),
    };
    dir.is_dir().then_some(dir)
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
    let Some(dir) = corpus() else {
        eprintln!(
            "\n*** replay SKIPPED: no scenario corpus at {} (set {CORPUS_VARIABLE} to milestone 1's runtime/engine/testdata/scenarios) ***\n",
            repository().join("runtime/engine/testdata/scenarios").display()
        );
        return;
    };
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
