//! Replays milestone 1's HTTP corpus through the engine's transport-neutral
//! routes (D58): every exchange, event-stream transcript and MCP exchange
//! of `runtime/engine/testdata/http` ([`HTTP_FILES`]), on SQLite, and on
//! Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one. A case
//! is skipped only when only the TypeScript engine can run it (`js`: a
//! function the host gave the app), or when it composes a core behavior
//! not ported yet (milestones 10 and 11); the counts of what ran and what
//! was skipped, and why, print per file. With the `axum` feature the same
//! corpus, with the same skips and the same [`KNOWN`] steps, replays
//! through the engine's axum router too (`AxumRouter`), as a Rust server
//! mounts it.
//!
//! The corpus is `runtime/engine/testdata/http`, in the tree;
//! `ENGINE_HTTP_CORPUS_DIR` names another directory, and `ENGINE_HTTP_ONLY`
//! one file of it to replay alone. A directory that is
//! not there fails the test, so the replay cannot pass by running nothing.

use std::path::{Path, PathBuf};

use superschematic_engine_testkit::{
    load, replay_file_through, Database, Direct, FileReport, Transport, HTTP_FILES,
};

const CORPUS_VARIABLE: &str = "ENGINE_HTTP_CORPUS_DIR";
const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// Steps whose answers differ for a reason outside the routes, by file,
/// case and step, with the reason. The test reports them and passes; once
/// one gives the recorded answer, the test fails until it leaves this list.
const KNOWN: [(&str, &str, &str); 2] = [
    (
        "http",
        "the behavior catalog > lists every behavior the engine runs, to any caller, and describes one by name",
        "the catalog lists all 15 of the core's behaviors beside the test's; the Rust engine registers those ported so far, whose entries match, until milestones 10 and 11 port the rest",
    ),
    (
        "mcp",
        "tools/call > list_behaviors and describe_behavior read the behaviors the engine runs, for any caller",
        "list_behaviors lists all 15 of the core's behaviors beside the test's; the Rust engine registers those ported so far, whose entries match, until milestones 10 and 11 port the rest",
    ),
];

fn repository() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..")
}

fn corpus() -> PathBuf {
    match std::env::var(CORPUS_VARIABLE) {
        Ok(dir) if !dir.is_empty() => PathBuf::from(dir),
        _ => repository().join("runtime/engine/testdata/http"),
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
            _ => eprintln!("http replay: Postgres pass skipped; set {DATABASE_VARIABLE} to run it"),
        }
    }
    databases
}

#[test]
fn the_engines_routes_give_the_recorded_answers() {
    replay_through(&Direct, "the engine's routes");
}

#[cfg(feature = "axum")]
#[test]
fn the_engines_axum_router_gives_the_recorded_answers() {
    replay_through(
        &superschematic_engine_testkit::AxumRouter::new(),
        "the axum router",
    );
}

// replay_through replays every file on every database through `transport`,
// and fails on a step that differs, but for those KNOWN lists.
fn replay_through(transport: &dyn Transport, label: &str) {
    let dir = corpus();
    assert!(
        dir.is_dir(),
        "no HTTP corpus at {} (the tree holds runtime/engine/testdata/http; {CORPUS_VARIABLE} names another directory)",
        dir.display()
    );
    let mut failures = Vec::new();
    for database in databases() {
        let mut reports: Vec<FileReport> = Vec::new();
        let only = std::env::var("ENGINE_HTTP_ONLY").unwrap_or_default();
        for name in HTTP_FILES {
            if !only.is_empty() && only != name {
                continue;
            }
            let path = dir.join(format!("{name}.json"));
            let file = load(&path).unwrap_or_else(|error| panic!("{error}"));
            assert!(
                file.scenarios.len() > 5,
                "{} holds {} cases",
                path.display(),
                file.scenarios.len()
            );
            reports.push(replay_file_through(
                &file,
                &database,
                &repository(),
                transport,
            ));
        }
        eprintln!("\nhttp replay through {label} on {:?}:", database.dialect());
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
            for (file, case, reason) in KNOWN {
                if file != report.file {
                    continue;
                }
                let failed = report
                    .failures
                    .iter()
                    .any(|failure| failure.starts_with(&format!("{case} ")));
                if failed {
                    eprintln!("      known mismatch: {case}: {reason}");
                } else {
                    failures.push(format!(
                        "[{:?}] {file}: {case} gives the recorded answer now; take it out of KNOWN",
                        database.dialect()
                    ));
                }
            }
            for failure in &report.failures {
                if KNOWN.iter().any(|(file, case, _)| {
                    *file == report.file && failure.starts_with(&format!("{case} "))
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
        "{} steps gave other answers through {label}:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
