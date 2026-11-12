//! This runner and the Go runner on one file (D116): a file the Go runner
//! migrated is continued by this one, and the reverse, and the file ends
//! with the state rows, the log rows and the schema one runner alone
//! leaves.
//!
//! The Go runner is its binary, `superschematic-migrate`, which
//! `SUPERSCHEMATIC_MIGRATE_BIN` names. Without the variable the tests
//! return at once and say why.

mod support;

use std::path::{Path, PathBuf};
use std::process::Command;

use superschematic_migrate::{Error, Phase, Plan};
use support::{
    apply, case_paths, chain_phases, exec, must_read, open, plan, quiet, read, seed, steps_in,
    strings, TempDir, CASES,
};

/// Names the Go runner's binary.
const BIN_ENV: &str = "SUPERSCHEMATIC_MIGRATE_BIN";

/// The Go runner's binary, or `None`, with the reason printed, when the
/// environment names none.
fn go_runner() -> Option<PathBuf> {
    let Some(bin) = std::env::var_os(BIN_ENV).filter(|bin| !bin.is_empty()) else {
        println!("{BIN_ENV} is not set: no Go runner to compare with, so the test checks nothing");
        return None;
    };
    let bin = PathBuf::from(bin);
    assert!(
        bin.is_file(),
        "{BIN_ENV} names {}, which is no file",
        bin.display()
    );
    Some(bin)
}

/// Runs the Go runner on `file` and returns what it printed.
fn go(bin: &Path, file: &Path, args: &[&str]) -> String {
    let output = Command::new(bin)
        .args(args)
        .arg("--database-url")
        .arg(format!("sqlite:{}", file.display()))
        .output()
        .unwrap_or_else(|err| panic!("run {}: {err}", bin.display()));
    assert!(
        output.status.success(),
        "{} {args:?}: {}\n{}",
        bin.display(),
        output.status,
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8_lossy(&output.stdout).into_owned()
}

/// Either runner applying one plan.
#[derive(Clone, Copy, Debug, PartialEq)]
enum By {
    Go,
    Rust,
}

impl By {
    fn other(self) -> By {
        match self {
            By::Go => By::Rust,
            By::Rust => By::Go,
        }
    }

    /// Applies the plan at `path` to `file` in `phase`.
    fn apply(self, bin: &Path, file: &Path, path: &Path, phase: Phase) {
        match self {
            By::Go => {
                let plan = path.to_string_lossy();
                go(
                    bin,
                    file,
                    &["apply", "--plan", &plan, "--phase", phase.as_str()],
                );
            }
            By::Rust => {
                let conn = open(file);
                apply(&mut quiet(&conn), &must_read(&read(path)), phase);
            }
        }
    }
}

/// What the runners leave in a file, but for the times: the state rows,
/// the log rows and the schema, the state tables' own included.
#[derive(Debug, PartialEq)]
struct Left {
    state: Vec<String>,
    log: Vec<String>,
    schema: Vec<String>,
}

/// Reads what the runners left in `file`, and checks that every time in it
/// has the one form both runners write: RFC 3339 UTC with microseconds.
fn left(file: &Path) -> Left {
    let times = [
        "SELECT updated_at FROM superschematic_schema_state",
        "SELECT started_at FROM superschematic_migrations",
        "SELECT finished_at FROM superschematic_migrations",
    ];
    for query in times {
        for time in strings(file, query) {
            let shape: String = time
                .chars()
                .map(|c| if c.is_ascii_digit() { '0' } else { c })
                .collect();
            assert_eq!(shape, "0000-00-00T00:00:00.000000Z", "{query}: {time:?}");
        }
    }
    Left {
        state: strings(
            file,
            "SELECT service, dialect, model_hash, model, plan_hash, plan_phase
FROM superschematic_schema_state ORDER BY service",
        ),
        log: strings(
            file,
            "SELECT plan_hash, step, service, phase, subject, sql_hash
FROM superschematic_migrations ORDER BY plan_hash, step",
        ),
        schema: strings(
            file,
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name",
        ),
    }
}

/// Every vector case, with the plans before each point of its chain
/// applied by one runner and the rest by the other, ends as the case
/// applied by either runner alone.
#[test]
fn a_chain_split_between_the_runners() {
    let Some(bin) = go_runner() else { return };
    for case in CASES {
        let paths = case_paths(case);
        let chain: Vec<Plan> = paths.iter().map(|path| must_read(&read(path))).collect();
        let phases = chain_phases(case, &chain);
        let dir = TempDir::new();
        let run = |name: &str, by: &dyn Fn(usize) -> By| {
            let file = dir.file(name);
            for (i, path) in paths.iter().enumerate() {
                by(i).apply(&bin, &file, path, phases[i]);
            }
            left(&file)
        };

        let alone = run("rust.db", &|_| By::Rust);
        assert_eq!(alone.log.len(), {
            let ran = |(plan, phase): (&Plan, &Phase)| match phase {
                Phase::Expand => steps_in(plan, Phase::Expand),
                _ => plan.steps.len(),
            };
            chain.iter().zip(&phases).map(ran).sum::<usize>()
        });
        assert_eq!(
            run("go.db", &|_| By::Go),
            alone,
            "{case}: the Go runner alone"
        );
        for split in 1..paths.len() {
            for first in [By::Go, By::Rust] {
                let by = |i: usize| if i < split { first } else { first.other() };
                assert_eq!(
                    run(&format!("{first:?}-{split}.db"), &by),
                    alone,
                    "{case}: {first:?} applied the first {split} plans, and the other runner the rest"
                );
            }
        }

        // The Go runner reads the state this runner wrote.
        let last = chain.last().expect("the case has a plan");
        if phases[chain.len() - 1] == Phase::All {
            let model = go(
                &bin,
                &dir.file("rust.db"),
                &["status", "--service", &last.service, "--model"],
            );
            assert_eq!(model, format!("{}\n", last.model().canonical), "{case}");
        }
    }
}

/// One runner runs a plan's expand steps and the other its contract, and a
/// plan this runner stopped in is resumed by the Go runner.
#[test]
fn a_plan_split_between_the_runners() {
    let Some(bin) = go_runner() else { return };
    for case in CASES {
        let paths = case_paths(case);
        let chain: Vec<Plan> = paths.iter().map(|path| must_read(&read(path))).collect();
        let phases = chain_phases(case, &chain);
        let dir = TempDir::new();
        // The chain with plan `at` applied by `split`, the rest by this
        // runner.
        let run = |name: &str, at: usize, split: &dyn Fn(&Path, &Path)| {
            let file = dir.file(name);
            for (i, path) in paths.iter().enumerate() {
                if i == at {
                    split(&file, path);
                } else {
                    By::Rust.apply(&bin, &file, path, phases[i]);
                }
            }
            left(&file)
        };
        let alone = run("alone.db", usize::MAX, &|_, _| {});

        for (at, plan) in chain.iter().enumerate() {
            if phases[at] != Phase::All {
                continue;
            }
            if steps_in(plan, Phase::Contract) > 0 {
                for first in [By::Go, By::Rust] {
                    let got = run(&format!("phases-{first:?}-{at}.db"), at, &|file, path| {
                        first.apply(&bin, file, path, Phase::Expand);
                        first.other().apply(&bin, file, path, Phase::Contract);
                    });
                    assert_eq!(
                        got,
                        alone,
                        "{case}: {first:?} expanded plan {} and the other runner contracted it",
                        at + 1
                    );
                }
            }
            if plan.steps.len() > 1 {
                let got = run(&format!("resume-{at}.db"), at, &|file, path| {
                    {
                        let conn = open(file);
                        let mut stopping = quiet(&conn).after_step(|_| Err("stop".into()));
                        assert!(matches!(
                            stopping.apply(plan, Phase::All),
                            Err(Error::Stopped(_))
                        ));
                    }
                    By::Go.apply(&bin, file, path, Phase::All);
                });
                assert_eq!(
                    got,
                    alone,
                    "{case}: the Go runner resumed plan {} after its step 1",
                    at + 1
                );
            }
        }
    }
}

/// A model one runner adopts is the model the other goes on from.
#[test]
fn an_adoption_by_either_runner() {
    let Some(bin) = go_runner() else { return };
    let dir = TempDir::new();
    let (create, evolve) = (plan("01-create"), plan("02-evolve"));
    let model = dir.file("model.json");
    std::fs::write(&model, &create.model().canonical).expect("write the model");
    let evolve_path = support::fixtures_dir().join("02-evolve.plan.json");
    let run = |name: &str, adopts: By| {
        let file = dir.file(name);
        for step in &create.steps {
            let statements: Vec<&str> = step.statements.iter().map(String::as_str).collect();
            exec(&file, &statements);
        }
        seed(&file);
        match adopts {
            By::Go => {
                go(&bin, &file, &["adopt", "--model", &model.to_string_lossy()]);
            }
            By::Rust => {
                let conn = open(&file);
                quiet(&conn)
                    .adopt(create.model())
                    .expect("adopt 01's model");
            }
        }
        adopts.other().apply(&bin, &file, &evolve_path, Phase::All);
        left(&file)
    };
    let (go_adopted, rust_adopted) = (run("go.db", By::Go), run("rust.db", By::Rust));
    assert_eq!(go_adopted, rust_adopted);
    assert_eq!(go_adopted.log.len(), evolve.steps.len());
    assert!(go_adopted.state[0].starts_with(&format!("shop|sqlite|{}|", evolve.to)));
}
