//! What the runner's tests share: the Go runner's hand-written plans
//! (`runtime/migrate/go/testdata/sqlite`) and the compiler's vectors
//! (`runtime/migrate/testdata/plans`), read where they live, a plan edited
//! and sealed again, a SQLite file of its own for each test, and the
//! checks the Go runner's tests make after every chain.

// Each test target uses part of this module.
#![allow(dead_code)]

use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::rc::Rc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use rusqlite::types::Value as SqlValue;
use rusqlite::Connection;
use serde_json::{Map, Value};
use superschematic_migrate::{
    plan_hash, read_model, read_plan, Applied, Dialect, Error, Phase, Plan, Runner, Status,
    PLAN_PHASE_EXPANDED,
};

/// The Go runner's hand-written SQLite plans.
pub fn fixtures_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../go/testdata/sqlite")
}

/// The plans the compiler writes for the runners: one directory per case.
pub fn vectors_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../testdata/plans")
}

pub fn read(path: &Path) -> Vec<u8> {
    std::fs::read(path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()))
}

/// Reads `<name>.plan.json` of the hand-written SQLite plans.
pub fn fixture(name: &str) -> Vec<u8> {
    read(&fixtures_dir().join(format!("{name}.plan.json")))
}

/// Reads a hand-written SQLite plan.
pub fn plan(name: &str) -> Plan {
    must_read(&fixture(name))
}

pub fn must_read(doc: &[u8]) -> Plan {
    read_plan(doc).unwrap_or_else(|err| panic!("read the plan: {err}"))
}

/// Applies `edit` to a plan document and seals it again: it sets `to` from
/// `toModel`, and `expanded` from `expandedModel` when the plan has one,
/// when `set_to`, and `hash` from the content.
pub fn seal(doc: &[u8], edit: impl FnOnce(&mut Map<String, Value>), set_to: bool) -> Vec<u8> {
    let mut plan: Map<String, Value> = serde_json::from_slice(doc).expect("the plan is JSON");
    edit(&mut plan);
    if set_to {
        for (model, hash) in [("toModel", "to"), ("expandedModel", "expanded")] {
            if let Some(value) = plan.get(model) {
                let raw = serde_json::to_vec(value).expect("the model encodes");
                let model = read_model(&raw).expect("the model reads");
                plan.insert(hash.to_string(), Value::String(model.hash));
            }
        }
    }
    plan.remove("hash");
    let raw = serde_json::to_vec(&plan).expect("the plan encodes");
    let hash = plan_hash(&raw).expect("the plan hashes");
    plan.insert("hash".to_string(), Value::String(hash));
    serde_json::to_vec_pretty(&plan).expect("the plan encodes")
}

/// A hand-written plan edited and sealed again, with `to` recomputed.
pub fn edited(name: &str, edit: impl FnOnce(&mut Map<String, Value>)) -> Plan {
    must_read(&seal(&fixture(name), edit, true))
}

/// A plan document's steps, for editing.
pub fn steps(plan: &mut Map<String, Value>) -> &mut Vec<Value> {
    plan.get_mut("steps")
        .and_then(Value::as_array_mut)
        .expect("the plan has steps")
}

/// The steps of `plan` in `phase`.
pub fn steps_in(plan: &Plan, phase: Phase) -> usize {
    plan.steps.iter().filter(|step| step.phase == phase).count()
}

/// The hash of the plan's expanded model, or empty when it has none.
pub fn expanded(plan: &Plan) -> &str {
    plan.between_phases()
        .map_or("", |model| model.hash.as_str())
}

/// The SQLite cases every checkout holds.
pub const CASES: [&str; 3] = ["sqlite-columns", "sqlite-rebuild", "sqlite-supersede"];

/// The paths of a case's plan documents, in name order.
pub fn case_paths(case: &str) -> Vec<PathBuf> {
    let dir = vectors_dir().join(case);
    let mut paths: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|err| panic!("list {}: {err}", dir.display()))
        .map(|entry| entry.expect("read the case's directory").path())
        .filter(|path| path.to_string_lossy().ends_with(".plan.json"))
        .collect();
    paths.sort();
    paths
}

/// A case's plans, in name order.
pub fn case_plans(case: &str) -> Vec<Plan> {
    case_paths(case)
        .iter()
        .map(|path| must_read(&read(path)))
        .collect()
}

/// Every case of the vectors whose plans are for SQLite, with the phases
/// each of its plans runs.
pub fn sqlite_cases() -> Vec<(String, Vec<Plan>, Vec<Phase>)> {
    let dir = vectors_dir();
    let mut names: Vec<String> = std::fs::read_dir(&dir)
        .unwrap_or_else(|err| panic!("list {}: {err}", dir.display()))
        .map(|entry| entry.expect("read the vectors' directory"))
        .filter(|entry| entry.path().is_dir())
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    names.sort();
    let mut cases = Vec::new();
    for name in names {
        let chain = case_plans(&name);
        if chain.first().map(|plan| plan.dialect) != Some(Dialect::Sqlite) {
            continue;
        }
        let phases = chain_phases(&name, &chain);
        cases.push((name, chain, phases));
    }
    for want in CASES {
        assert!(
            cases.iter().any(|(name, ..)| name == want),
            "no case {want}"
        );
    }
    cases
}

/// Checks that each plan of a case follows the one before it, and returns
/// the phases each runs: every phase, or expand alone for a plan the next
/// one supersedes.
pub fn chain_phases(case: &str, chain: &[Plan]) -> Vec<Phase> {
    let mut phases = vec![Phase::All; chain.len()];
    for (i, plan) in chain.iter().enumerate() {
        let mut from = "";
        if i > 0 {
            let previous = &chain[i - 1];
            assert_eq!(
                (plan.dialect, &plan.service),
                (chain[0].dialect, &chain[0].service),
                "{case}: plan {}",
                i + 1
            );
            from = &previous.to;
            if !expanded(previous).is_empty() && plan.from == expanded(previous) {
                from = expanded(previous);
                phases[i - 1] = Phase::Expand;
            }
        }
        assert_eq!(plan.from, from, "{case}: plan {} starts from", i + 1);
    }
    phases
}

/// A directory of its own for one test, removed when the test ends.
pub struct TempDir {
    path: PathBuf,
}

impl TempDir {
    pub fn new() -> TempDir {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "superschematic-migrate-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir_all(&path).expect("create the test's directory");
        TempDir { path }
    }

    /// The path of a file in the directory.
    pub fn file(&self, name: &str) -> PathBuf {
        self.path.join(name)
    }

    /// The names of the directory's files, sorted.
    pub fn names(&self) -> Vec<String> {
        let mut names: Vec<String> = std::fs::read_dir(&self.path)
            .expect("list the test's directory")
            .map(|entry| {
                entry
                    .expect("read the test's directory")
                    .file_name()
                    .to_string_lossy()
                    .into_owned()
            })
            .collect();
        names.sort();
        names
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.path);
    }
}

pub fn open(path: &Path) -> Connection {
    Connection::open(path).unwrap_or_else(|err| panic!("open {}: {err}", path.display()))
}

/// Runs statements on a connection of their own, with foreign keys on.
pub fn exec(path: &Path, statements: &[&str]) {
    let conn = open(path);
    conn.execute_batch("PRAGMA foreign_keys = ON")
        .expect("turn foreign keys on");
    for statement in statements {
        conn.execute_batch(statement)
            .unwrap_or_else(|err| panic!("{statement}: {err}"));
    }
}

/// The rows of a query on a connection of its own, each row's columns as
/// text joined by `|`, a NULL as `NULL`.
pub fn strings(path: &Path, sql: &str) -> Vec<String> {
    let conn = open(path);
    let mut statement = conn
        .prepare(sql)
        .unwrap_or_else(|err| panic!("{sql}: {err}"));
    let columns = statement.column_count();
    let rows = statement
        .query_map([], |row| {
            let mut cells = Vec::with_capacity(columns);
            for column in 0..columns {
                cells.push(match row.get::<_, SqlValue>(column)? {
                    SqlValue::Null => "NULL".to_string(),
                    SqlValue::Integer(value) => value.to_string(),
                    SqlValue::Real(value) => value.to_string(),
                    SqlValue::Text(value) => value,
                    SqlValue::Blob(value) => format!("{value:?}"),
                });
            }
            Ok(cells.join("|"))
        })
        .unwrap_or_else(|err| panic!("{sql}: {err}"));
    rows.collect::<Result<_, _>>()
        .unwrap_or_else(|err| panic!("{sql}: {err}"))
}

/// Every object a plan made, by name and definition, sorted: the Go
/// tests' catalog of a SQLite file.
pub fn catalog(path: &Path) -> Vec<String> {
    let mut lines = strings(
        path,
        r"SELECT type || ' ' || name || ' on ' || tbl_name || ': ' || COALESCE(sql, '')
FROM sqlite_master
WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name NOT LIKE 'superschematic\_%' ESCAPE '\'",
    );
    lines.sort();
    lines
}

/// Writes two customers and three orders into a file at 01-create's model.
pub fn seed(path: &Path) {
    exec(
        path,
        &[
            "INSERT INTO customer (id, name) VALUES (1, 'Ada'), (2, 'Grace')",
            r#"INSERT INTO "order" (id, customer_id, total) VALUES (1, 1, 100), (2, 1, 200), (3, 2, 300)"#,
        ],
    );
}

/// The rows of `table`.
pub fn count(path: &Path, table: &str) -> String {
    strings(path, &format!("SELECT count(*) FROM {table}")).remove(0)
}

/// The runner's lines, kept for a test to read.
#[derive(Clone, Default)]
pub struct Lines(Rc<RefCell<Vec<String>>>);

impl Lines {
    pub fn contains(&self, text: &str) -> bool {
        self.0.borrow().iter().any(|line| line.contains(text))
    }

    pub fn all(&self) -> Vec<String> {
        self.0.borrow().clone()
    }

    pub fn push(&self, line: &str) {
        self.0.borrow_mut().push(line.to_string());
    }
}

/// A runner on `conn` that keeps its lines in `lines` and whose lock
/// timeout retries take 10ms.
pub fn runner<'c>(conn: &'c Connection, lines: &Lines) -> Runner<'c> {
    let lines = lines.clone();
    Runner::new(conn)
        .expect("bind the runner")
        .waits([Duration::from_millis(10), Duration::from_millis(10)])
        .log(move |line| lines.push(line))
}

/// A runner on `conn` that discards its lines.
pub fn quiet(conn: &Connection) -> Runner<'_> {
    runner(conn, &Lines::default())
}

/// Applies `plan` in `phase` and fails the test on an error.
pub fn apply(runner: &mut Runner<'_>, plan: &Plan, phase: Phase) -> Applied {
    runner
        .apply(plan, phase)
        .unwrap_or_else(|err| panic!("apply {}: {err}", plan.hash))
}

/// Reads a service's status.
pub fn status(runner: &mut Runner<'_>, service: &str) -> Status {
    runner.status(service).expect("read the status")
}

/// Applies `plan` in `phase` and fails the test unless the runner refuses
/// it with every one of `want` in its message. It returns the message.
pub fn refused(runner: &mut Runner<'_>, plan: &Plan, phase: Phase, want: &[&str]) -> String {
    let err = match runner.apply(plan, phase) {
        Err(err) if err.is_refusal() => err.to_string(),
        other => panic!("apply = {other:?}, want a refusal"),
    };
    for text in want {
        assert!(err.contains(text), "{err:?} does not say {text:?}");
    }
    err
}

/// The numbers `from..=to`.
pub fn ints(from: usize, to: usize) -> Vec<usize> {
    (from..=to).collect()
}

/// Applies `chain` once straight through, then once per step of every plan
/// with a failure injected after that step, and compares each final
/// catalog with the first. `phases` holds the phases each plan runs; a
/// plan that runs `Phase::Expand` only is superseded by the next.
pub fn resume_after_every_step(chain: &[Plan], phases: &[Phase]) {
    let dir = TempDir::new();
    let reference = dir.file("reference.db");
    {
        let conn = open(&reference);
        let mut r = quiet(&conn);
        for (plan, phase) in chain.iter().zip(phases) {
            apply(&mut r, plan, *phase);
        }
    }
    let want = catalog(&reference);
    let last = chain.last().expect("the chain has a plan");

    for (i, plan) in chain.iter().enumerate() {
        // The steps the plan runs are its first n.
        let n = match phases[i] {
            Phase::Expand => steps_in(plan, Phase::Expand),
            _ => plan.steps.len(),
        };
        for k in 1..=n {
            let case = format!("plan {} step {k}", i + 1);
            let path = dir.file(&format!("plan-{}-step-{k}.db", i + 1));
            let conn = open(&path);
            let mut r = quiet(&conn);
            for (before, phase) in chain[..i].iter().zip(phases) {
                apply(&mut r, before, *phase);
            }
            let mut failing = quiet(&conn).after_step(move |step| {
                if step.index == k {
                    return Err("injected".into());
                }
                Ok(())
            });
            match failing.apply(plan, phases[i]) {
                Err(Error::Stopped(cause)) if cause.to_string() == "injected" => {}
                other => panic!("{case}: apply with a failure after the step = {other:?}"),
            }
            let st = status(&mut r, &plan.service);
            if k < n {
                assert_eq!(st.plan_hash.as_deref(), Some(plan.hash.as_str()), "{case}");
                assert_eq!(st.steps.len(), k, "{case}: steps logged after the failure");
                let result = apply(&mut r, plan, phases[i]);
                let done = if n < plan.steps.len() {
                    result.expand_done && !result.finished
                } else {
                    result.finished
                };
                assert_eq!(result.ran, ints(k + 1, n), "{case}: the resume ran");
                assert!(done, "{case}: the resume ended at {result:?}");
            } else if n < plan.steps.len() {
                // The failure came after the last expand step, which
                // recorded the plan's expanded model.
                assert_eq!(st.plan_hash.as_deref(), Some(plan.hash.as_str()), "{case}");
                assert_eq!(st.model_hash, expanded(plan), "{case}");
                assert_eq!(
                    st.plan_phase.as_deref(),
                    Some(PLAN_PHASE_EXPANDED),
                    "{case}"
                );
                let result = apply(&mut r, plan, phases[i]);
                assert!(
                    result.ran.is_empty() && result.expand_done,
                    "{case}: expand after its last step: {result:?}"
                );
            } else {
                assert_eq!(st.plan_hash, None, "{case}");
                assert_eq!(st.model_hash, plan.to, "{case}");
                let result = apply(&mut r, plan, Phase::All);
                assert!(
                    result.already_applied,
                    "{case}: apply after the last step: {result:?}"
                );
            }
            for (after, phase) in chain[i + 1..].iter().zip(&phases[i + 1..]) {
                apply(&mut r, after, *phase);
            }
            assert_eq!(
                status(&mut r, &last.service).model_hash,
                last.to,
                "{case}: the final model"
            );
            assert_eq!(
                catalog(&path),
                want,
                "{case}: the catalog after resuming differs"
            );
        }
    }
}
