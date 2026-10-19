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
//!
//! The files replay on threads of their own, every file on every database
//! one job, since each scenario opens engines on a file or a schema of its
//! own: as many threads as the machine has cores, at most eight
//! (`ENGINE_REPLAY_THREADS` sets another number, 1 to replay one file at a
//! time). The reports print and are checked in the files' order.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Mutex, PoisonError};

use superschematic_engine::storage::Dialect;
use superschematic_engine_testkit::{load, replay_file, Database, FileReport, ENGINE_FILES};

const CORPUS_VARIABLE: &str = "ENGINE_CORPUS_DIR";
const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// Steps whose answers differ for a reason outside this engine, by file,
/// scenario and step, the database they differ on (`None` for both), with
/// the reason. The test reports them and passes; once one gives the
/// recorded answer, the test fails until it leaves this list.
const KNOWN: [(&str, &str, Option<Dialect>, &str); 1] = [(
    "core-links-many",
    "Links, many-valued (node) > a file Links v2 left: the table is built again keyed by the target, its rows, their order and their references kept > step 17",
    // A storage step: Postgres skips it.
    Some(Dialect::Sqlite),
    "between its two opens the test rebuilds the table as Links' second migration left it and drops migration 3 from the ledger, through a Storage of its own the recorder does not see; the replay's file applied all three migrations at the first open, so migration 3's applied_at is not a later instant; the recorder is to mark such a scenario js",
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

const THREADS_VARIABLE: &str = "ENGINE_REPLAY_THREADS";

// threads is how many files replay at once.
fn threads() -> usize {
    std::env::var(THREADS_VARIABLE)
        .ok()
        .and_then(|threads| threads.parse::<usize>().ok())
        .filter(|threads| *threads > 0)
        .unwrap_or_else(|| {
            std::thread::available_parallelism()
                .map_or(4, std::num::NonZeroUsize::get)
                .min(8)
        })
}

// replay_all replays each (database, file) job on the threads, and returns
// the reports in the jobs' order.
fn replay_all(dir: &Path, jobs: &[(&Database, &str)]) -> Vec<FileReport> {
    let next = AtomicUsize::new(0);
    let reports: Mutex<Vec<Option<FileReport>>> = Mutex::new(jobs.iter().map(|_| None).collect());
    std::thread::scope(|scope| {
        for _ in 0..threads().min(jobs.len()) {
            scope.spawn(|| loop {
                let index = next.fetch_add(1, Ordering::SeqCst);
                let Some((database, name)) = jobs.get(index) else {
                    return;
                };
                let path = dir.join(format!("{name}.json"));
                let file = load(&path).unwrap_or_else(|error| panic!("{error}"));
                let report = replay_file(&file, database, &repository());
                reports.lock().unwrap_or_else(PoisonError::into_inner)[index] = Some(report);
            });
        }
    });
    reports
        .into_inner()
        .unwrap_or_else(PoisonError::into_inner)
        .into_iter()
        .map(|report| report.expect("every job replayed"))
        .collect()
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
    let databases = databases();
    let jobs: Vec<(&Database, &str)> = databases
        .iter()
        .flat_map(|database| ENGINE_FILES.iter().map(move |name| (database, *name)))
        .collect();
    let mut all = replay_all(&dir, &jobs).into_iter();
    for database in &databases {
        let reports: Vec<FileReport> = all.by_ref().take(ENGINE_FILES.len()).collect();
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
            for (file, scenario, dialect, reason) in KNOWN {
                if file != report.file || dialect.is_some_and(|only| only != database.dialect()) {
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
                if KNOWN.iter().any(|(file, scenario, dialect, _)| {
                    *file == report.file
                        && dialect.is_none_or(|only| only == database.dialect())
                        && failure.starts_with(&format!("{scenario} "))
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
