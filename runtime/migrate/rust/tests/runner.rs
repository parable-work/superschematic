//! The runner on a SQLite file, held to the Go runner test for test:
//! `runner_test.go`, `supersede_test.go` and `sqlite_test.go` of
//! `runtime/migrate/go`, over the same hand-written plans
//! (`runtime/migrate/go/testdata/sqlite`).

mod support;

use std::cell::RefCell;
use std::path::Path;
use std::sync::mpsc;
use std::time::Duration;

use serde_json::{json, Map, Value};
use superschematic_migrate::{
    hash, Applied, Dialect, Error, Phase, Plan, Runner, PLAN_PHASE_EXPAND, PLAN_PHASE_EXPANDED,
};
use support::{
    apply, catalog, count, edited, exec, expanded, ints, must_read, open, plan, quiet, read,
    refused, runner, seed, status, steps, steps_in, strings, Lines, TempDir,
};

/// The first plan runs every step on an empty database, records its model,
/// and a second apply of it runs nothing and says the plan is applied.
#[test]
fn apply_from_an_empty_database() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let lines = Lines::default();
    let mut r = runner(&conn, &lines);
    let create = plan("01-create");

    let result = apply(&mut r, &create, Phase::All);
    assert!(!result.already_applied && result.finished, "{result:?}");
    assert_eq!(result.ran, ints(1, create.steps.len()));
    let st = status(&mut r, "shop");
    assert_eq!(st.model_hash, create.to);
    assert_eq!(st.model.as_deref(), Some(create.model().canonical.as_str()));
    assert_eq!(st.plan_hash, None);
    assert_eq!(st.dialect, "sqlite");
    seed(&path);

    let again = apply(&mut r, &create, Phase::All);
    assert!(again.already_applied && again.ran.is_empty(), "{again:?}");
    assert!(lines.contains("is already applied"));
    assert_eq!(count(&path, r#""order""#), "3");
}

/// Plans apply one after another, each from the previous one's model; rows
/// survive every step that does not drop them; a plan taken back and forth
/// (A to B, B to A, A to B again) runs again.
#[test]
fn apply_a_chain() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let mut r = quiet(&conn);
    let (create, evolve, audit, drop_audit) = (
        plan("01-create"),
        plan("02-evolve"),
        plan("03-audit"),
        plan("04-drop-audit"),
    );

    apply(&mut r, &create, Phase::All);
    seed(&path);
    apply(&mut r, &evolve, Phase::All);
    assert_eq!(count(&path, r#""order""#), "3");
    assert_eq!(count(&path, "customer"), "2");
    apply(&mut r, &audit, Phase::All);
    assert_eq!(status(&mut r, "shop").model_hash, audit.to);

    // Back to 02's model, then 03 again: the same plan, so the same hash,
    // whose earlier log rows must not make it skip its steps.
    assert_eq!(drop_audit.to, evolve.to);
    assert_eq!(drop_audit.from, audit.to);
    apply(&mut r, &drop_audit, Phase::All);
    let result = apply(&mut r, &audit, Phase::All);
    assert_eq!(result.ran, [1]);
    assert!(result.finished);
    assert_eq!(count(&path, "audit"), "0");
}

/// A failure injected after any step of any plan of the chain leaves the
/// plan in progress with the steps before it logged; the next apply runs
/// only the rest, and the database ends with the same catalog as one that
/// never failed.
#[test]
fn resume_after_a_failure() {
    let chain = [plan("01-create"), plan("02-evolve"), plan("03-audit")];
    support::resume_after_every_step(&chain, &[Phase::All; 3]);
}

/// A new step for a plan document.
fn new_step(index: usize, phase: &str, op: &str, subject: &str, statements: &[&str]) -> Value {
    json!({
        "index": index, "phase": phase, "op": op, "subject": subject,
        "statements": statements, "transactional": true,
    })
}

/// Appends a step to a plan document.
fn append_step(
    plan: &mut Map<String, Value>,
    phase: &str,
    op: &str,
    subject: &str,
    statements: &[&str],
) {
    let index = steps(plan).len() + 1;
    steps(plan).push(new_step(index, phase, op, subject, statements));
}

/// A statement that fails stops the run with an error naming the step's
/// index, subject and statement; the plan stays in progress at that step
/// and the steps before it stay done.
#[test]
fn a_failed_step_names_itself() {
    let dir = TempDir::new();
    let conn = open(&dir.file("shop.db"));
    let mut r = quiet(&conn);
    let statement = "CREATE INDEX missing_idx ON missing (id)";
    let broken = edited("01-create", |p| {
        append_step(
            p,
            "expand",
            "createIndex",
            "table/missing/index/missing_idx",
            &[statement],
        );
    });
    let last = broken.steps.len();
    let err = match r.apply(&broken, Phase::All) {
        Err(Error::Step(err)) => err,
        other => panic!("apply = {other:?}, want a failed step"),
    };
    assert_eq!(err.index, last);
    assert_eq!(err.subject, "table/missing/index/missing_idx");
    assert_eq!(err.statement.as_deref(), Some(statement));
    let message = err.to_string();
    for want in [
        format!("step {last} (table/missing/index/missing_idx) failed: "),
        "no such table: main.missing".to_string(),
        format!("\nstatement:\n{statement}"),
    ] {
        assert!(
            message.contains(&want),
            "{message:?} does not name {want:?}"
        );
    }
    let st = status(&mut r, "shop");
    assert_eq!(st.plan_hash.as_deref(), Some(broken.hash.as_str()));
    assert_eq!(st.steps.len(), last - 1);
    assert_eq!(st.model_hash, "");
    // The step's log row is the SHA-256 of its statements joined by
    // "\n;\n".
    let first = &st.steps[0];
    assert_eq!(
        first.sql_hash,
        hash(broken.steps[0].statements.join("\n;\n").as_bytes())
    );
    assert_eq!(first.subject, "table/customer");
    assert_eq!(first.phase, "expand");
    assert_eq!(first.step, 1);
    for time in [Some(&first.started_at), first.finished_at.as_ref()] {
        assert_rfc3339_micros(time.expect("the step finished"));
    }
}

/// A time the runner writes is RFC 3339 UTC text with microseconds, as the
/// Go runner writes it: `2026-10-04T16:42:18.146632Z`.
fn assert_rfc3339_micros(time: &str) {
    let shape: String = time
        .chars()
        .map(|c| if c.is_ascii_digit() { '0' } else { c })
        .collect();
    assert_eq!(shape, "0000-00-00T00:00:00.000000Z", "{time:?}");
}

/// A step of two statements logs the SHA-256 of both joined by "\n;\n"
/// (computed with shasum).
#[test]
fn sql_hash_of_many_statements() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let mut r = quiet(&conn);
    let two = edited("01-create", |p| {
        append_step(
            p,
            "expand",
            "createTable",
            "table/audit",
            &["CREATE TABLE a (x)", "CREATE TABLE b (y)"],
        );
    });
    apply(&mut r, &two, Phase::All);
    let got = strings(
        &path,
        "SELECT sql_hash FROM superschematic_migrations WHERE step = 4",
    );
    assert_eq!(
        got,
        ["f056266fcc43c63f791f91c201e7dcc3616e12fba4e011b3a1f60c372d1336a8"]
    );
}

/// A step with an empty statements list, such as the one a version graph's
/// content change gets, runs nothing, and the runner logs it with the
/// SHA-256 of no SQL.
#[test]
fn apply_a_step_with_no_statements() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let lines = Lines::default();
    let mut r = runner(&conn, &lines);
    let p = edited("01-create", |p| {
        append_step(p, "expand", "changeGraphContent", "table/order", &[]);
    });
    let last = p.steps.len();
    let result = apply(&mut r, &p, Phase::All);
    assert!(result.finished);
    assert_eq!(result.ran, ints(1, last));
    let st = status(&mut r, "shop");
    assert_eq!(
        (st.model_hash.as_str(), st.plan_hash),
        (p.to.as_str(), None)
    );
    assert!(
        lines.contains(&format!("step {last}/{last} expand table/order: done")),
        "{:?}",
        lines.all()
    );
    let got = strings(
        &path,
        &format!("SELECT sql_hash FROM superschematic_migrations WHERE plan_hash = '{}' AND step = {last}", p.hash),
    );
    assert_eq!(got, [hash(b"")]);
}

/// Applies `plan` with two runners at once, each on a connection and a
/// thread of its own: `second` starts while `first`, which has run step 1,
/// pauses. Returns what each did.
fn two_runners(path: &Path, plan: &Plan, pause: bool) -> (Applied, Applied) {
    let (started_tx, started_rx) = mpsc::channel::<()>();
    std::thread::scope(|scope| {
        let first = scope.spawn(move || {
            let conn = open(path);
            let mut r = quiet(&conn).after_step(move |step| {
                if step.index == 1 {
                    let _ = started_tx.send(());
                    if pause {
                        std::thread::sleep(Duration::from_millis(300));
                    }
                }
                Ok(())
            });
            r.apply(plan, Phase::All)
        });
        let second = scope.spawn(move || {
            let conn = open(path);
            // The first runner's step 1 is done, or the first found none
            // to run and dropped its end.
            let _ = started_rx.recv();
            let mut r = quiet(&conn);
            r.apply(plan, Phase::All)
        });
        let first = first.join().expect("the first runner's thread");
        let second = second.join().expect("the second runner's thread");
        match (first, second) {
            (Ok(first), Ok(second)) => (first, second),
            (first, second) => panic!("plan {}: first: {first:?}; second: {second:?}", plan.hash),
        }
    })
}

/// Two runners applying one plan at once never run a step twice: on
/// SQLite the two take turns per step and split the steps between them.
#[test]
fn two_runners_serialize() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    for name in ["01-create", "02-evolve"] {
        let p = plan(name);
        let (first, second) = two_runners(&path, &p, true);
        let mut ran = [first.ran.clone(), second.ran.clone()].concat();
        ran.sort_unstable();
        assert_eq!(ran, ints(1, p.steps.len()), "{first:?} and {second:?}");
        assert!(
            first.finished && second.finished,
            "{first:?} and {second:?}"
        );
        let logged = strings(
            &path,
            &format!(
                "SELECT count(*) FROM superschematic_migrations WHERE plan_hash = '{}'",
                p.hash
            ),
        );
        assert_eq!(logged, [p.steps.len().to_string()]);
        let conn = open(&path);
        let st = status(&mut quiet(&conn), "shop");
        assert_eq!(
            (st.model_hash.as_str(), st.plan_hash),
            (p.to.as_str(), None)
        );
    }
}

/// The runner refuses, and changes nothing, when the database is not at
/// the plan's from, when another plan is in progress, when contract is
/// asked for before expand has run, and when the plan, the database or the
/// recorded state are of different dialects.
#[test]
fn refusals() {
    let (create, evolve, audit) = (plan("01-create"), plan("02-evolve"), plan("03-audit"));
    let dir = TempDir::new();

    // The wrong baseline.
    {
        let conn = open(&dir.file("baseline.db"));
        let mut r = quiet(&conn);
        // With no applied model, status has no model to print: the refusal
        // says to adopt the model the database holds.
        let no_model = refused(
            &mut r,
            &evolve,
            Phase::All,
            &[
                &format!("the plan starts from model {}", evolve.from),
                "service shop has no applied model",
                "record the model it matches with superschematic-migrate adopt",
                "plan --print-model",
                "if it is empty, plan from an empty database",
            ],
        );
        assert!(!no_model.contains("status --model"), "{no_model:?}");
        apply(&mut r, &create, Phase::All);
        let other_model = refused(
            &mut r,
            &audit,
            Phase::All,
            &[
                &format!("the plan starts from model {}", audit.from),
                &format!("service shop is at model {}", create.to),
                "plan again from the applied model (superschematic-migrate status --model)",
            ],
        );
        assert!(!other_model.contains("adopt"), "{other_model:?}");
        let st = status(&mut r, "shop");
        assert_eq!((st.plan_hash, st.model_hash), (None, create.to.clone()));
    }

    // Another plan in progress.
    {
        let conn = open(&dir.file("in-progress.db"));
        let mut r = quiet(&conn);
        apply(&mut r, &create, Phase::All);
        apply(&mut r, &evolve, Phase::Expand);
        refused(
            &mut r,
            &audit,
            Phase::All,
            &[
                &format!("has plan {} in progress", evolve.hash),
                "expand steps are done",
            ],
        );
    }

    // Contract before expand.
    {
        let conn = open(&dir.file("contract.db"));
        let mut r = quiet(&conn);
        apply(&mut r, &create, Phase::All);
        refused(
            &mut r,
            &evolve,
            Phase::Contract,
            &["is not in progress", "--phase expand first"],
        );
        assert_eq!(status(&mut r, "shop").plan_hash, None);
        // Part-way through expand is still before contract.
        let mut stopping = quiet(&conn).after_step(|step| {
            if step.index == 1 {
                return Err("stop".into());
            }
            Ok(())
        });
        assert!(matches!(
            stopping.apply(&evolve, Phase::Expand),
            Err(Error::Stopped(_))
        ));
        refused(
            &mut r,
            &evolve,
            Phase::Contract,
            &["expand step 2 has not run"],
        );
    }

    // A plan for another dialect.
    {
        let conn = open(&dir.file("dialect.db"));
        let mut r = quiet(&conn);
        let postgres = must_read(&read(
            &support::fixtures_dir().join("../postgres/01-create.plan.json"),
        ));
        assert_eq!(postgres.dialect, Dialect::Postgres);
        refused(
            &mut r,
            &postgres,
            Phase::All,
            &["the plan is for postgres and the database is sqlite"],
        );
        // Refused before the state tables are made.
        assert!(catalog(&dir.file("dialect.db")).is_empty());
        assert!(!status(&mut r, "shop").recorded);
    }

    // State recorded for another dialect.
    {
        let path = dir.file("recorded.db");
        let conn = open(&path);
        let mut r = quiet(&conn);
        apply(&mut r, &create, Phase::All);
        exec(
            &path,
            &["UPDATE superschematic_schema_state SET dialect = 'elsewhere'"],
        );
        refused(&mut r, &evolve, Phase::All, &["records dialect elsewhere"]);
    }
}

/// Phase::Expand runs the expand steps and records the phase, with the
/// plan's expanded model as the applied model; the contract-only objects
/// stay until Phase::Contract runs the rest and records the plan's model.
#[test]
fn phases() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let mut r = quiet(&conn);
    let (create, evolve) = (plan("01-create"), plan("02-evolve"));
    apply(&mut r, &create, Phase::All);
    seed(&path);
    let expand_steps = steps_in(&evolve, Phase::Expand);

    let result = apply(&mut r, &evolve, Phase::Expand);
    assert!(result.expand_done && !result.finished, "{result:?}");
    assert_eq!(result.ran, ints(1, expand_steps));
    let st = status(&mut r, "shop");
    assert_eq!(st.plan_hash.as_deref(), Some(evolve.hash.as_str()));
    assert_eq!(st.plan_phase.as_deref(), Some(PLAN_PHASE_EXPANDED));
    assert_eq!(st.steps.len(), expand_steps);
    let between = evolve.between_phases().expect("02 has an expanded model");
    assert_eq!(st.model_hash, between.hash);
    assert_eq!(st.model.as_deref(), Some(between.canonical.as_str()));
    // customer.name is dropped in contract, so it is still there.
    assert_eq!(
        strings(&path, "SELECT name FROM customer WHERE id = 1"),
        ["Ada"]
    );
    // Expand again runs nothing.
    let again = apply(&mut r, &evolve, Phase::Expand);
    assert!(again.ran.is_empty() && !again.finished, "{again:?}");

    let result = apply(&mut r, &evolve, Phase::Contract);
    assert!(result.finished, "{result:?}");
    assert_eq!(result.ran, ints(expand_steps + 1, evolve.steps.len()));
    let st = status(&mut r, "shop");
    assert_eq!((st.plan_hash, st.plan_phase), (None, None));
    assert_eq!(st.model_hash, evolve.to);
    assert_eq!(count(&path, "customer"), "2");
}

/// Status on a file the runner never touched reports no state and creates
/// no table.
#[test]
fn status_changes_nothing() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let mut r = quiet(&conn);
    let st = status(&mut r, "shop");
    assert!(
        !st.recorded && st.model_hash.is_empty() && st.model.is_none(),
        "{st:?}"
    );
    assert!(catalog(&path).is_empty());
    assert_eq!(
        strings(
            &path,
            "SELECT count(*) FROM sqlite_master WHERE name LIKE 'superschematic%'"
        ),
        ["0"]
    );
}

/// Adopt records a model without running anything and returns the hash it
/// replaces; a plan from that model then applies. It refuses while a plan
/// is in progress and for a model of another dialect.
#[test]
fn adopt() {
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let mut r = quiet(&conn);
    let (create, evolve, audit) = (plan("01-create"), plan("02-evolve"), plan("03-audit"));

    // A database built by hand to 01's model.
    for step in &create.steps {
        let statements: Vec<&str> = step.statements.iter().map(String::as_str).collect();
        exec(&path, &statements);
    }
    assert_eq!(r.adopt(create.model()).expect("adopt 01's model"), "");
    let st = status(&mut r, "shop");
    assert_eq!(st.model_hash, create.to);
    assert_eq!(st.model.as_deref(), Some(create.model().canonical.as_str()));
    assert!(apply(&mut r, &evolve, Phase::All).finished);

    // Adopting again replaces the model plans recorded.
    assert_eq!(
        r.adopt(create.model()).expect("adopt over a plan's model"),
        evolve.to
    );

    // The database is at 02 again but the state says 01: adopt 02.
    assert_eq!(
        r.adopt(evolve.model()).expect("adopt 02's model"),
        create.to
    );
    assert!(apply(&mut r, &audit, Phase::All).finished);

    let mut stopping = quiet(&conn).after_step(|_| Err("stop".into()));
    assert!(matches!(
        stopping.apply(&plan("04-drop-audit"), Phase::All),
        Err(Error::Stopped(_))
    ));
    assert_eq!(status(&mut r, "shop").model_hash, evolve.to);
    r.adopt(create.model()).expect("adopt 01's model again");
    // 02 again from a database that already has its objects: stop it after
    // step 1, which re-adds nothing, so it stays in progress.
    let again = edited("02-evolve", |p| {
        steps(p)[0]["statements"] = json!(["SELECT 1"]);
    });
    assert!(matches!(
        stopping.apply(&again, Phase::All),
        Err(Error::Stopped(_))
    ));
    match r.adopt(audit.model()) {
        Err(err) if err.is_refusal() && err.to_string().contains("in progress") => {}
        other => panic!("adopt during a plan = {other:?}, want a refusal"),
    }

    let postgres = must_read(&read(
        &support::fixtures_dir().join("../postgres/01-create.plan.json"),
    ));
    match r.adopt(postgres.model()) {
        Err(Error::Refused(message)) => {
            assert_eq!(
                message,
                "the model is for postgres and the database is sqlite"
            );
        }
        other => panic!("adopt of a postgres model = {other:?}, want a refusal"),
    }
}

/// 01-create, 02-evolve and supersede-02, which starts from the model
/// 02-evolve holds between its phases.
fn superseding() -> (Plan, Plan, Plan) {
    let (create, evolve, supersede) = (plan("01-create"), plan("02-evolve"), plan("supersede-02"));
    assert!(!expanded(&evolve).is_empty());
    assert_eq!(supersede.from, expanded(&evolve));
    (create, evolve, supersede)
}

/// Once a plan's expand steps have finished, the database is at its
/// expanded model (D27, amended). A plan from that model supersedes the
/// pending contract: the runner says so, runs the new plan and never the
/// old contract, which it refuses afterwards. A plan from the old plan's
/// from is refused while the database holds the expanded model.
#[test]
fn a_plan_from_the_expanded_model_supersedes_the_contract() {
    let (create, evolve, supersede) = superseding();
    let from_create = plan("fk-violation");
    assert_eq!(from_create.from, create.to);
    let dir = TempDir::new();
    let path = dir.file("shop.db");
    let conn = open(&path);
    let lines = Lines::default();
    let mut r = runner(&conn, &lines);
    apply(&mut r, &create, Phase::All);
    seed(&path);
    apply(&mut r, &evolve, Phase::Expand);

    // The database is at the expanded model, so a plan from the model
    // 02 started from is refused.
    refused(
        &mut r,
        &from_create,
        Phase::All,
        &[
            &format!("has plan {} in progress", evolve.hash),
            "expand steps are done",
        ],
    );

    let result = apply(&mut r, &supersede, Phase::All);
    assert_eq!(result.superseded.as_deref(), Some(evolve.hash.as_str()));
    assert!(result.finished);
    assert_eq!(result.ran, ints(1, supersede.steps.len()));
    assert!(lines.contains(&format!(
        "plan {} supersedes plan {}",
        supersede.hash, evolve.hash
    )));
    let st = status(&mut r, "shop");
    assert_eq!(st.model_hash, supersede.to);
    assert_eq!((st.plan_hash, st.plan_phase), (None, None));
    // 02's contract never ran: customer.name keeps its rows.
    assert_eq!(
        strings(&path, "SELECT name FROM customer WHERE id = 1"),
        ["Ada"]
    );
    assert_eq!(count(&path, r#""order""#), "3");

    // The superseded contract is no longer in progress.
    refused(
        &mut r,
        &evolve,
        Phase::Contract,
        &[
            &format!("the plan starts from model {}", evolve.from),
            &format!("service shop is at model {}", supersede.to),
        ],
    );
    refused(
        &mut r,
        &evolve,
        Phase::All,
        &[&format!("the plan starts from model {}", evolve.from)],
    );
    let st = status(&mut r, "shop");
    assert_eq!((st.model_hash, st.plan_hash), (supersede.to.clone(), None));
}

/// A contract that ran leaves nothing to supersede: the plan from the
/// expanded model is then from the wrong model.
#[test]
fn contract_after_expand() {
    let (create, evolve, supersede) = superseding();
    let dir = TempDir::new();
    let conn = open(&dir.file("shop.db"));
    let mut r = quiet(&conn);
    apply(&mut r, &create, Phase::All);
    apply(&mut r, &evolve, Phase::Expand);
    let result = apply(&mut r, &evolve, Phase::Contract);
    assert!(result.finished && result.superseded.is_none(), "{result:?}");
    let st = status(&mut r, "shop");
    assert_eq!((st.model_hash, st.plan_hash), (evolve.to.clone(), None));
    // The plan from the expanded model is now from the wrong model.
    refused(
        &mut r,
        &supersede,
        Phase::All,
        &[
            &format!("the plan starts from model {}", expanded(&evolve)),
            &format!("service shop is at model {}", evolve.to),
        ],
    );
}

/// A superseded plan whose expand had nothing to do: a plan with only
/// contract steps, so between its phases the database is still at its
/// from. A plan back to that model supersedes it and leaves the database
/// at the old plan's from again, so only the rule that Phase::Contract
/// runs the plan in progress keeps a stale contract from dropping what the
/// servers use.
#[test]
fn a_superseded_plan_whose_expand_had_nothing_to_do() {
    let create = plan("01-create");
    let dir = TempDir::new();
    let create_model: Value =
        serde_json::from_str(&create.model().canonical).expect("the model is JSON");
    let contract_only = edited("02-evolve", |p| {
        let mut kept: Vec<Value> = steps(p)
            .iter()
            .filter(|step| step["phase"] == "contract")
            .cloned()
            .collect();
        for (i, step) in kept.iter_mut().enumerate() {
            step["index"] = json!(i + 1);
        }
        *steps(p) = kept;
        p.insert("expandedModel".to_string(), create_model);
    });
    let back = edited("01-create", |p| {
        p.insert("from".to_string(), json!(create.to));
        steps(p).clear();
    });
    assert_eq!(expanded(&contract_only), create.to);
    assert_eq!((&back.from, &back.to), (&create.to, &create.to));

    let conn = open(&dir.file("shop.db"));
    let mut r = quiet(&conn);
    apply(&mut r, &create, Phase::All);
    apply(&mut r, &contract_only, Phase::Expand);
    let st = status(&mut r, "shop");
    assert_eq!(st.plan_hash.as_deref(), Some(contract_only.hash.as_str()));
    assert_eq!(st.model_hash, create.to);
    let result = apply(&mut r, &back, Phase::All);
    assert_eq!(
        result.superseded.as_deref(),
        Some(contract_only.hash.as_str())
    );
    refused(
        &mut r,
        &contract_only,
        Phase::Contract,
        &["is not in progress", "--phase expand first"],
    );
    let st = status(&mut r, "shop");
    assert_eq!((st.model_hash, st.plan_hash), (create.to.clone(), None));
}

/// A contract that has started is not superseded: 02 with a last contract
/// step that fails. Its other contract steps run, the plan stays in
/// progress, and a plan from its expanded model is refused.
#[test]
fn a_contract_that_has_started_is_not_superseded() {
    let (create, evolve, supersede) = superseding();
    let dir = TempDir::new();
    let conn = open(&dir.file("shop.db"));
    let mut r = quiet(&conn);
    apply(&mut r, &create, Phase::All);
    let failing = edited("02-evolve", |p| {
        append_step(
            p,
            "contract",
            "dropTable",
            "table/missing",
            &["DROP TABLE missing"],
        );
    });
    assert_eq!(expanded(&failing), expanded(&evolve));
    match r.apply(&failing, Phase::All) {
        Err(Error::Step(err)) if err.index == failing.steps.len() => {}
        other => panic!("apply = {other:?}, want the last step to fail"),
    }
    let st = status(&mut r, "shop");
    assert_eq!(st.plan_hash.as_deref(), Some(failing.hash.as_str()));
    assert_eq!(st.model_hash, expanded(&evolve));
    refused(
        &mut r,
        &supersede,
        Phase::All,
        &[&format!(
            "has plan {} in progress, and its contract has started",
            failing.hash
        )],
    );
}

/// A plan written before plans carried their expanded model keeps the rule
/// from before it (D27, amended). Its expand steps leave the applied model
/// at its from, and every other plan is refused until its contract runs.
#[test]
fn a_plan_without_expanded() {
    let dir = TempDir::new();
    let conn = open(&dir.file("shop.db"));
    let mut r = quiet(&conn);
    let create = plan("01-create");
    let old = edited("02-evolve", |p| {
        p.remove("expanded");
        p.remove("expandedModel");
    });
    assert!(old.between_phases().is_none());
    apply(&mut r, &create, Phase::All);
    apply(&mut r, &old, Phase::Expand);
    let st = status(&mut r, "shop");
    assert_eq!(st.plan_hash.as_deref(), Some(old.hash.as_str()));
    assert_eq!(st.plan_phase.as_deref(), Some(PLAN_PHASE_EXPAND));
    assert_eq!(st.model_hash, create.to);
    // The applied model is the plan's from, which the database no longer
    // holds: a plan from it is refused, not run.
    let in_progress = format!("has plan {} in progress", old.hash);
    refused(
        &mut r,
        &plan("fk-violation"),
        Phase::All,
        &[&in_progress, "expand steps are done"],
    );
    refused(&mut r, &plan("supersede-02"), Phase::All, &[&in_progress]);

    let result = apply(&mut r, &old, Phase::Contract);
    assert!(result.finished && result.superseded.is_none(), "{result:?}");
    let st = status(&mut r, "shop");
    assert_eq!((st.model_hash, st.plan_hash), (old.to.clone(), None));
}

/// A copy-table rebuild of a parent table keeps the rows of a child that
/// references it ON DELETE CASCADE when the step turns foreign keys off,
/// and loses them when it does not; a rebuild that leaves a child row
/// without its parent fails foreign_key_check and rolls back.
#[test]
fn sqlite_foreign_keys_off() {
    let dir = TempDir::new();

    // The rebuild keeps the children.
    {
        let path = dir.file("keeps.db");
        let conn = open(&path);
        let mut r = quiet(&conn);
        apply(&mut r, &plan("01-create"), Phase::All);
        seed(&path);
        apply(&mut r, &plan("02-evolve"), Phase::All);
        assert_eq!(count(&path, r#""order""#), "3");
        assert!(strings(&path, "PRAGMA foreign_key_check").is_empty());
    }

    // With foreign keys on the rebuild deletes them.
    {
        let path = dir.file("deletes.db");
        let conn = open(&path);
        let mut r = quiet(&conn);
        apply(&mut r, &plan("01-create"), Phase::All);
        seed(&path);
        let on = edited("02-evolve", |p| {
            steps(p)[1]
                .as_object_mut()
                .expect("a step")
                .remove("foreignKeysOff");
        });
        apply(&mut r, &on, Phase::All);
        assert_eq!(
            count(&path, r#""order""#),
            "0",
            "the cascade deletes the orders"
        );
    }

    // A violation rolls the step back.
    {
        let path = dir.file("violation.db");
        let conn = open(&path);
        let mut r = quiet(&conn);
        apply(&mut r, &plan("01-create"), Phase::All);
        seed(&path);
        let violation = plan("fk-violation");
        let err = match r.apply(&violation, Phase::All) {
            Err(Error::Step(err)) => err,
            other => panic!("apply = {other:?}, want step 1 to fail foreign_key_check"),
        };
        assert_eq!((err.index, err.statement.as_deref()), (1, None));
        assert_eq!(
            err.to_string(),
            "step 1 (table/customer) failed: foreign_key_check: order row 1 references a missing row of customer; order row 2 references a missing row of customer"
        );
        assert_eq!(
            strings(&path, "SELECT name FROM customer ORDER BY id"),
            ["Ada", "Grace"]
        );
        assert!(strings(
            &path,
            "SELECT name FROM sqlite_master WHERE name = 'customer__new'"
        )
        .is_empty());
        let st = status(&mut r, "shop");
        assert_eq!(st.plan_hash.as_deref(), Some(violation.hash.as_str()));
        assert!(st.steps.is_empty());
        // The runner's connection has foreign keys on again.
        let on: i64 = conn
            .query_row("PRAGMA foreign_keys", [], |row| row.get(0))
            .expect("read foreign_keys");
        assert_eq!(on, 1);
    }
}

/// A step whose BEGIN IMMEDIATE finds another connection's write lock
/// waits busy_timeout, fails with SQLITE_BUSY, and the runner retries it
/// after 1s, 2s, ... until the lock is free; when it never frees, the step
/// fails after the last retry.
#[test]
fn sqlite_busy_retry() {
    for frees in [true, false] {
        let dir = TempDir::new();
        let path = dir.file("shop.db");
        {
            let conn = open(&path);
            apply(&mut quiet(&conn), &plan("01-create"), Phase::All);
        }
        let holder = open(&path);
        let conn = open(&path);
        let lines = Lines::default();
        let kept = lines.clone();
        let slept = RefCell::new(Vec::new());
        let mut r = Runner::new(&conn)
            .and_then(|r| r.busy_timeout(Duration::from_millis(50)))
            .expect("bind the runner")
            .log(move |line| kept.push(line))
            .sleep(|wait| {
                slept.borrow_mut().push(wait);
                // The lock frees while the runner waits for its second try.
                if frees && slept.borrow().len() == 2 {
                    holder.execute_batch("COMMIT").expect("free the lock");
                }
            })
            .after_step(|step| {
                if step.index == 1 {
                    holder.execute_batch("BEGIN IMMEDIATE")?;
                }
                Ok(())
            });
        let evolve = plan("02-evolve");
        let result = r.apply(&evolve, Phase::All);
        if frees {
            let result = result.expect("apply once the lock is free");
            assert!(result.finished, "{result:?}");
            assert_eq!(
                *slept.borrow(),
                [Duration::from_secs(1), Duration::from_secs(2)]
            );
            assert!(
                lines.contains("step 2/4 expand table/customer: lock timeout; retrying in 1s"),
                "{:?}",
                lines.all()
            );
            continue;
        }
        let err = match result {
            Err(Error::Step(err)) => err,
            other => panic!("apply = {other:?}, want step 2 to give up on SQLITE_BUSY"),
        };
        assert_eq!(err.index, 2);
        assert!(err.is_busy());
        assert!(err.to_string().contains("gave up after 5 retries"), "{err}");
        assert_eq!(
            *slept.borrow(),
            [1, 2, 4, 8, 16].map(Duration::from_secs),
            "the default waits"
        );
        holder.execute_batch("ROLLBACK").expect("free the lock");
        let st = status(&mut quiet(&conn), "shop");
        assert_eq!(st.plan_hash.as_deref(), Some(evolve.hash.as_str()));
        assert_eq!(st.steps.len(), 1);
    }
}
