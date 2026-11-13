//! The chain start (D116, "Migrations a binary carries"): what a binary
//! that carries its plans does to a database file before it serves a
//! request, over the Go runner's hand-written plans and the compiler's
//! vectors.

mod support;

use std::path::{Path, PathBuf};

use serde_json::json;
use superschematic_migrate::{
    read_model, start, Adoption, AppliedPlan, Error, Phase, Plan, StartOptions, Started,
};
use support::{
    apply, case_paths, catalog, count, exec, fixture, ints, must_read, open, plan, quiet, read,
    seal, seed, status, steps, strings, Lines, TempDir,
};

/// The hand-written chain: 01 creates, 02 has a contract step, 03 has
/// none, and 04 is one contract step.
const CHAIN: [&str; 4] = ["01-create", "02-evolve", "03-audit", "04-drop-audit"];

/// The documents of the chain's first `plans` plans.
fn carried(plans: usize) -> Vec<Vec<u8>> {
    CHAIN[..plans].iter().map(|name| fixture(name)).collect()
}

fn started(file: &Path, plans: &[Vec<u8>]) -> Started {
    start(file, plans, StartOptions::default()).unwrap_or_else(|err| panic!("start: {err}"))
}

/// What a start that applied `plans` in full reports for each.
fn in_full(plans: &[Plan]) -> Vec<AppliedPlan> {
    plans
        .iter()
        .map(|plan| AppliedPlan {
            hash: plan.hash.clone(),
            steps: ints(1, plan.steps.len()),
        })
        .collect()
}

/// The copy a start makes of `file` before the model `hash`.
fn copy_of(file: &Path, hash: &str) -> PathBuf {
    PathBuf::from(format!("{}.before-{hash}", file.display()))
}

/// The state rows of a file: every column but the time.
fn state(file: &Path) -> Vec<String> {
    strings(
        file,
        "SELECT service, dialect, model_hash, plan_hash, plan_phase FROM superschematic_schema_state",
    )
}

/// An empty file starts from the first plan and runs every plan. It makes
/// no copy, though a plan has a contract step: the file held nothing. The
/// next start finds the last plan's model and does nothing.
#[test]
fn an_empty_file_starts_from_the_first_plan() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let plans = [plan("01-create"), plan("02-evolve"), plan("03-audit")];
    let lines = Lines::default();
    let mut log = |line: &str| lines.push(line);
    let outcome = start(
        &file,
        &carried(3),
        StartOptions {
            log: Some(&mut log),
            ..StartOptions::default()
        },
    )
    .expect("start an empty file");
    assert_eq!(
        outcome,
        Started {
            service: "shop".to_string(),
            adopted: false,
            backup: None,
            plans: in_full(&plans),
            model: plans[2].to.clone(),
        }
    );
    assert!(
        lines.contains("step 4/4 contract table/customer/column/name: done"),
        "{:?}",
        lines.all()
    );
    assert_eq!(
        state(&file),
        [format!("shop|sqlite|{}|NULL|NULL", plans[2].to)]
    );
    assert_eq!(dir.names(), ["shop.sqlite"]);
    // Foreign keys are on and the journal mode is SQLite's default.
    assert_eq!(strings(&file, "PRAGMA journal_mode"), ["delete"]);

    let bytes = read(&file);
    let again = started(&file, &carried(3));
    assert!(
        again.plans.is_empty() && !again.adopted && again.backup.is_none(),
        "{again:?}"
    );
    assert_eq!(again.model, plans[2].to);
    assert_eq!(
        read(&file),
        bytes,
        "a start with nothing to do changed the file"
    );
}

/// A file at a model a carried plan starts from is taken forward plan by
/// plan, each plan's expand and contract steps back to back. A copy is
/// made before the first step, since a contract step will run, and it
/// holds what the contract dropped.
#[test]
fn a_file_at_an_intermediate_model_is_taken_forward() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let plans = [plan("01-create"), plan("02-evolve"), plan("03-audit")];
    assert_eq!(started(&file, &carried(1)).plans, in_full(&plans[..1]));
    seed(&file);

    let outcome = started(&file, &carried(3));
    let copy = copy_of(&file, &plans[0].to);
    assert_eq!(outcome.plans, in_full(&plans[1..]));
    assert_eq!(outcome.backup.as_deref(), Some(copy.as_path()));
    assert_eq!(outcome.model, plans[2].to);
    assert_eq!(
        dir.names(),
        [
            "shop.sqlite",
            &format!("shop.sqlite.before-{}", plans[0].to)
        ]
    );
    // The file lost customer.name to 02's contract and kept its rows.
    assert!(
        strings(&file, "SELECT name FROM pragma_table_info('customer')")
            .iter()
            .all(|column| column != "name")
    );
    assert_eq!(count(&file, "customer"), "2");
    assert_eq!(count(&file, r#""order""#), "3");
    // The copy is the file as it was: at 01's model, with the names.
    assert_eq!(
        strings(&copy, "SELECT name FROM customer ORDER BY id"),
        ["Ada", "Grace"]
    );
    assert_eq!(
        state(&copy),
        [format!("shop|sqlite|{}|NULL|NULL", plans[0].to)]
    );
    assert_eq!(strings(&copy, "PRAGMA integrity_check"), ["ok"]);
}

/// A copy is made only by a start that runs a contract step, and the
/// newest copy is the only one.
#[test]
fn only_the_newest_copy_is_kept() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let plans: Vec<Plan> = CHAIN.iter().map(|name| plan(name)).collect();
    let name = |hash: &str| format!("shop.sqlite.before-{hash}");

    // 01 alone: no contract step.
    assert_eq!(started(&file, &carried(1)).backup, None);
    assert_eq!(dir.names(), ["shop.sqlite"]);
    // 02 has one.
    let outcome = started(&file, &carried(2));
    assert_eq!(outcome.backup, Some(copy_of(&file, &plans[0].to)));
    assert_eq!(dir.names(), ["shop.sqlite".to_string(), name(&plans[0].to)]);
    // 03 has none: no copy, and the one from before 02 stays.
    let outcome = started(&file, &carried(3));
    assert_eq!((outcome.backup, outcome.plans.len()), (None, 1));
    assert_eq!(dir.names(), ["shop.sqlite".to_string(), name(&plans[0].to)]);
    // Files beside the copy that are no copy of this file stay.
    std::fs::write(dir.file("other.sqlite.before-x"), b"another file's").expect("write a file");
    std::fs::write(copy_of(&file, "stale.partial"), b"a copy that died").expect("write a file");
    // 04 is a contract step: a copy from before it, and the older ones go.
    let outcome = started(&file, &carried(4));
    assert_eq!(outcome.backup, Some(copy_of(&file, &plans[2].to)));
    assert_eq!(
        dir.names(),
        [
            "other.sqlite.before-x".to_string(),
            "shop.sqlite".to_string(),
            name(&plans[2].to)
        ]
    );
    assert_eq!(count(&copy_of(&file, &plans[2].to), "audit"), "0");
    // Nothing to do: no copy, and the last one stays.
    assert_eq!(started(&file, &carried(4)).backup, None);
    assert_eq!(dir.names().len(), 3);
}

/// A chain may pass a model twice: 04 takes the file back to 02's model. A
/// file at that model is at the model the chain ends at, whichever way it
/// came, and needs nothing.
#[test]
fn a_chain_that_passes_a_model_twice() {
    let dir = TempDir::new();
    let plans: Vec<Plan> = CHAIN.iter().map(|name| plan(name)).collect();
    assert_eq!(plans[3].to, plans[1].to);

    let file = dir.file("at-02.sqlite");
    started(&file, &carried(2));
    let outcome = started(&file, &carried(4));
    assert!(
        outcome.plans.is_empty() && outcome.backup.is_none(),
        "{outcome:?}"
    );
    assert_eq!(outcome.model, plans[1].to);

    let file = dir.file("at-03.sqlite");
    started(&file, &carried(3));
    assert_eq!(started(&file, &carried(4)).plans, in_full(&plans[3..]));

    // A file built by hand to that model is adopted, and nothing runs.
    let file = dir.file("by-hand.sqlite");
    exec(&file, &["CREATE TABLE customer (id INTEGER)"]);
    let model = plans[1].model().canonical.as_bytes();
    let outcome = start(&file, &carried(4), adopting(model, "SELECT 1")).expect("adopt");
    assert!(outcome.adopted && outcome.plans.is_empty(), "{outcome:?}");
}

/// A file at a model no carried plan starts from stops the start. The
/// error names the file's model and the model the carried plans end at,
/// and the file is not changed: it is a newer build's, or another app's.
#[test]
fn an_unknown_model_is_refused_and_nothing_changes() {
    let dir = TempDir::new();

    // A newer build's file: at 03's model, opened by a binary that
    // carries 01 and 02.
    let file = dir.file("newer.sqlite");
    started(&file, &carried(3));
    let bytes = read(&file);
    let (evolve, audit) = (plan("02-evolve"), plan("03-audit"));
    let err = start(
        &file,
        &carried(2),
        StartOptions {
            wal: true,
            ..StartOptions::default()
        },
    )
    .expect_err("a newer build's file");
    match &err {
        Error::UnknownModel {
            file: named,
            service,
            applied,
            in_progress,
            expected,
        } => {
            assert_eq!(named, &file);
            assert_eq!(service, "shop");
            assert_eq!(applied, &audit.to);
            assert_eq!(in_progress, &None);
            assert_eq!(expected, &evolve.to);
        }
        other => panic!("start = {other:?}, want an unknown model"),
    }
    assert!(err.is_refusal());
    let message = err.to_string();
    for want in [
        audit.to.as_str(),
        evolve.to.as_str(),
        "newer.sqlite",
        "a newer build's, or another app's",
    ] {
        assert!(message.contains(want), "{message:?} does not name {want:?}");
    }
    assert_eq!(read(&file), bytes, "a refused start changed the file");
    assert_eq!(dir.names(), ["newer.sqlite"]);

    // Another app's file: a model of the same service that no plan knows.
    let file = dir.file("other.sqlite");
    let decoy = read_model(br#"{"version":1,"dialect":"sqlite","service":"shop","decoy":true}"#)
        .expect("the decoy reads");
    {
        let conn = open(&file);
        quiet(&conn).adopt(&decoy).expect("adopt the decoy");
    }
    let bytes = read(&file);
    match start(&file, &carried(4), StartOptions::default()) {
        Err(Error::UnknownModel {
            applied, expected, ..
        }) => {
            assert_eq!((applied, expected), (decoy.hash, evolve.to.clone()));
        }
        other => panic!("start = {other:?}, want an unknown model"),
    }
    assert_eq!(read(&file), bytes, "a refused start changed the file");

    // A plan in progress that is none of the carried plans.
    let file = dir.file("in-progress.sqlite");
    started(&file, &carried(1));
    let supersede = plan("supersede-02");
    {
        let conn = open(&file);
        let mut r = quiet(&conn);
        apply(&mut r, &evolve, Phase::Expand);
        apply(&mut r, &supersede, Phase::Expand);
    }
    let bytes = read(&file);
    match start(&file, &carried(4), StartOptions::default()) {
        Err(Error::UnknownModel { in_progress, .. }) => {
            assert_eq!(in_progress, Some(supersede.hash))
        }
        other => panic!("start = {other:?}, want an unknown model"),
    }
    assert_eq!(read(&file), bytes, "a refused start changed the file");

    // An empty file, opened by a binary whose first plan is not from an
    // empty database.
    let file = dir.file("pruned.sqlite");
    match start(&file, &carried(4)[1..], StartOptions::default()) {
        Err(Error::UnknownModel { applied, .. }) => assert_eq!(applied, ""),
        other => panic!("start = {other:?}, want an unknown model"),
    }
    assert!(catalog(&file).is_empty());
}

/// 02-evolve with a last contract step that fails while two orders have
/// one total. The plan is the same document at every start, so a start
/// after the orders are fixed resumes it.
fn evolve_then_unique_totals() -> Vec<u8> {
    seal(
        &fixture("02-evolve"),
        |p| {
            let index = steps(p).len() + 1;
            steps(p).push(json!({
                "index": index, "phase": "contract", "op": "createIndex",
                "subject": "table/order/index/order_total_key", "transactional": true,
                "statements": [r#"CREATE UNIQUE INDEX order_total_key ON "order" (total)"#],
            }));
        },
        true,
    )
}

/// A step that fails stops the start with the plan and the step's index,
/// operation and subject. The steps before it stay applied, and the next
/// start goes on from it. That start makes no copy, since the plan's
/// contract had started, and the copy from before the contract stays.
#[test]
fn a_failed_step_stops_the_start_and_the_next_start_resumes() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let create = plan("01-create");
    let failing = evolve_then_unique_totals();
    let evolve = must_read(&failing);
    let plans = [fixture("01-create"), failing, fixture("03-audit")];
    // 03 follows the edited 02, which ends at 02's model.
    assert_eq!(evolve.to, plan("02-evolve").to);

    started(&file, &plans[..1]);
    seed(&file);
    exec(
        &file,
        &[r#"INSERT INTO "order" (id, customer_id, total) VALUES (4, 2, 300)"#],
    );

    let err = start(&file, &plans, StartOptions::default()).expect_err("step 5 fails");
    match &err {
        Error::ChainStep {
            file: named,
            plan,
            index,
            op,
            subject,
            source,
        } => {
            assert_eq!(named, &file);
            assert_eq!(plan, &evolve.hash);
            assert_eq!((*index, op.as_str()), (5, "createIndex"));
            assert_eq!(subject, "table/order/index/order_total_key");
            assert_eq!(
                source.statement.as_deref(),
                Some(r#"CREATE UNIQUE INDEX order_total_key ON "order" (total)"#)
            );
        }
        other => panic!("start = {other:?}, want a failed step"),
    }
    let message = err.to_string();
    for want in [
        "shop.sqlite: plan ",
        evolve.hash.as_str(),
        "step 5 (createIndex table/order/index/order_total_key) failed: ",
        "UNIQUE constraint failed",
        "\nstatement:\nCREATE UNIQUE INDEX order_total_key",
    ] {
        assert!(message.contains(want), "{message:?} does not name {want:?}");
    }
    // Steps 1 to 4 stay applied, the contract's first among them.
    {
        let conn = open(&file);
        let st = status(&mut quiet(&conn), "shop");
        assert_eq!(st.plan_hash.as_deref(), Some(evolve.hash.as_str()));
        assert_eq!(
            st.steps.iter().map(|step| step.step).collect::<Vec<_>>(),
            ints(1, 4)
        );
    }
    assert!(
        strings(&file, "SELECT name FROM pragma_table_info('customer')")
            .iter()
            .all(|column| column != "name")
    );
    let copy = copy_of(&file, &create.to);
    assert_eq!(
        strings(&copy, "SELECT name FROM customer ORDER BY id"),
        ["Ada", "Grace"]
    );

    // The same start fails at the same step, and keeps the copy.
    assert!(matches!(
        start(&file, &plans, StartOptions::default()),
        Err(Error::ChainStep { index: 5, .. })
    ));
    assert_eq!(
        strings(&copy, "SELECT name FROM customer ORDER BY id"),
        ["Ada", "Grace"]
    );

    exec(&file, &[r#"DELETE FROM "order" WHERE id = 4"#]);
    let outcome = started(&file, &plans);
    let audit = plan("03-audit");
    assert_eq!(
        outcome.plans,
        [
            AppliedPlan {
                hash: evolve.hash.clone(),
                steps: vec![5]
            },
            AppliedPlan {
                hash: audit.hash.clone(),
                steps: vec![1]
            },
        ]
    );
    assert_eq!(outcome.backup, None);
    assert_eq!(outcome.model, audit.to);
    assert_eq!(
        strings(&copy, "SELECT name FROM customer ORDER BY id"),
        ["Ada", "Grace"]
    );
    assert_eq!(
        dir.names(),
        [
            "shop.sqlite".to_string(),
            format!("shop.sqlite.before-{}", create.to)
        ]
    );
}

/// A start that stops in a plan's expand steps leaves the plan in
/// progress, and the next start copies the file before it goes on: no
/// contract step has run, so the copy still holds everything.
#[test]
fn a_start_that_resumes_before_the_contract_copies_the_file() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let (create, evolve) = (plan("01-create"), plan("02-evolve"));
    started(&file, &carried(1));
    seed(&file);
    // A table of the name step 2's rebuild makes fails that step, once
    // step 1 has run.
    exec(&file, &["CREATE TABLE customer__new (id INTEGER)"]);
    match start(&file, &carried(2), StartOptions::default()) {
        Err(Error::ChainStep { index: 2, op, .. }) => assert_eq!(op, "copyTable"),
        other => panic!("start = {other:?}, want step 2 to fail"),
    }
    let copy = copy_of(&file, &create.to);
    assert!(copy.exists());
    exec(&file, &["DROP TABLE customer__new"]);
    std::fs::remove_file(&copy).expect("remove the copy");

    let outcome = started(&file, &carried(2));
    assert_eq!(
        outcome.plans,
        [AppliedPlan {
            hash: evolve.hash.clone(),
            steps: ints(2, 4)
        }]
    );
    assert_eq!(outcome.backup.as_deref(), Some(copy.as_path()));
    assert_eq!(
        strings(&copy, "SELECT name FROM customer ORDER BY id"),
        ["Ada", "Grace"]
    );
}

/// The statements of 01-create, which build a file by hand to its model.
fn build_by_hand(file: &Path) {
    for step in &plan("01-create").steps {
        let statements: Vec<&str> = step.statements.iter().map(String::as_str).collect();
        exec(file, &statements);
    }
    exec(file, &["PRAGMA user_version = 7"]);
    seed(file);
}

/// The check an adoption of a file at the app's version 7 names.
const AT_VERSION_7: &str = "SELECT user_version = 7 FROM pragma_user_version";

fn adopting<'a>(model: &'a [u8], check: &'a str) -> StartOptions<'a> {
    StartOptions {
        adoption: Some(Adoption { model, check }),
        ..StartOptions::default()
    }
}

/// A file with tables, no state and an adoption whose check answers one
/// row holding 1 is adopted: the model is recorded as applied, and the
/// chain runs from it. The next start does not adopt again.
#[test]
fn a_file_built_by_hand_is_adopted_once() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let plans = [plan("01-create"), plan("02-evolve"), plan("03-audit")];
    let model = plans[0].model().canonical.as_bytes();
    build_by_hand(&file);

    let outcome =
        start(&file, &carried(3), adopting(model, AT_VERSION_7)).expect("adopt and start");
    assert!(outcome.adopted);
    assert_eq!(outcome.plans, in_full(&plans[1..]));
    assert_eq!(outcome.model, plans[2].to);
    // The copy is made once the model is recorded, so it is a file a
    // start goes on from.
    let copy = copy_of(&file, &plans[0].to);
    assert_eq!(outcome.backup.as_deref(), Some(copy.as_path()));
    assert_eq!(
        state(&copy),
        [format!("shop|sqlite|{}|NULL|NULL", plans[0].to)]
    );
    assert_eq!(count(&file, "customer"), "2");
    assert_eq!(
        state(&file),
        [format!("shop|sqlite|{}|NULL|NULL", plans[2].to)]
    );

    // The check no longer matters: the file has state.
    let again = start(&file, &carried(3), adopting(model, "SELECT 0")).expect("start again");
    assert!(!again.adopted && again.plans.is_empty(), "{again:?}");

    // An adoption of the model the plans end at records it and runs none.
    let file = dir.file("current.sqlite");
    build_by_hand(&file);
    let outcome = start(&file, &carried(1), adopting(model, AT_VERSION_7)).expect("adopt");
    assert!(
        outcome.adopted && outcome.plans.is_empty() && outcome.backup.is_none(),
        "{outcome:?}"
    );
    assert_eq!(
        state(&file),
        [format!("shop|sqlite|{}|NULL|NULL", plans[0].to)]
    );

    // An empty file is not adopted: it starts from the first plan.
    let file = dir.file("empty.sqlite");
    let outcome = start(&file, &carried(3), adopting(model, "SELECT 1")).expect("start");
    assert!(!outcome.adopted);
    assert_eq!(outcome.plans, in_full(&plans));
}

/// A file with tables and no state is refused, and not changed, when the
/// environment names no adoption, when the check does not answer one row
/// holding 1, when it cannot run on the file, and when it writes.
#[test]
fn a_file_built_by_hand_is_refused_without_its_adoption() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let model = plan("01-create").model().canonical.clone();
    build_by_hand(&file);
    let bytes = read(&file);
    let last = plan("03-audit").to;

    let cases: [(Option<&str>, &str); 9] = [
        (None, "the environment names no adoption"),
        (Some("SELECT 0"), "did not answer one row holding 1"),
        (
            Some("SELECT user_version = 8 FROM pragma_user_version"),
            "did not answer one row holding 1",
        ),
        (Some("SELECT 1 WHERE 0"), "did not answer one row holding 1"),
        (
            Some("SELECT 1 UNION ALL SELECT 1"),
            "did not answer one row holding 1",
        ),
        (Some("SELECT 1, 1"), "did not answer one row holding 1"),
        (Some("SELECT '1'"), "did not answer one row holding 1"),
        (
            Some("SELECT version = 7 FROM app_meta"),
            "the adoption's check did not run: no such table: app_meta",
        ),
        (
            Some("DELETE FROM customer"),
            "the adoption's check writes to the file",
        ),
    ];
    for (check, want) in cases {
        let options = match check {
            Some(check) => adopting(model.as_bytes(), check),
            None => StartOptions::default(),
        };
        match start(&file, &carried(3), options) {
            Err(Error::Refused(message)) => {
                assert!(
                    message.contains(want),
                    "{check:?}: {message:?} does not say {want:?}"
                );
                if !message.starts_with("the adoption's check") {
                    assert!(
                        message.contains(&last),
                        "{check:?}: {message:?} names no model"
                    );
                }
            }
            other => panic!("{check:?}: start = {other:?}, want a refusal"),
        }
        assert_eq!(
            read(&file),
            bytes,
            "{check:?}: a refused start changed the file"
        );
        assert_eq!(dir.names(), ["shop.sqlite"], "{check:?}");
    }
}

/// An adoption the chain cannot go on from is refused before the file is
/// opened.
#[test]
fn an_adoption_outside_the_chain_is_refused() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    for (model, want) in [
        (
            r#"{"version":1,"dialect":"sqlite","service":"shop","decoy":true}"#,
            "is no model a carried plan starts from",
        ),
        (
            r#"{"version":1,"dialect":"sqlite","service":"other"}"#,
            "the adoption's model is of sqlite service other; the carried plans are for sqlite service shop",
        ),
        (
            r#"{"version":1,"dialect":"postgres","service":"shop"}"#,
            "the adoption's model is of postgres service shop",
        ),
        (r#"{"version":1,"service":"shop"}"#, "the adoption's model: the model's dialect"),
        ("[", "the adoption's model: read the model: "),
    ] {
        match start(&file, &carried(3), adopting(model.as_bytes(), "SELECT 1")) {
            Err(err @ (Error::Refused(_) | Error::Document(_))) => {
                assert!(err.to_string().contains(want), "{err} does not say {want:?}");
            }
            other => panic!("{model}: start = {other:?}, want a refusal"),
        }
        assert!(!file.exists(), "{model}: a refused adoption made the file");
    }
}

/// A chain the start cannot follow is refused before the file is opened:
/// no plan, a plan that does not read, a plan of another dialect or
/// service, and a plan that does not start where the one before it ends.
#[test]
fn a_chain_that_does_not_follow_is_refused() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    let vector = |name: &str| read(&support::vectors_dir().join(name));
    let postgres =
        |name: &str| read(&support::fixtures_dir().join(format!("../postgres/{name}.plan.json")));
    // The supersede case's third plan starts from the model its second
    // holds between its phases, not from the one it ends at.
    let supersede: Vec<Vec<u8>> = [
        "sqlite-supersede/01-create.plan.json",
        "sqlite-supersede/02-replace-email.plan.json",
        "sqlite-supersede/03-supersede.plan.json",
    ]
    .iter()
    .map(|name| vector(name))
    .collect();
    let cases: Vec<(Vec<Vec<u8>>, &str)> = vec![
        (vec![], "was given none"),
        (
            supersede,
            "carried plan 3 starts from model 7584a2e9d13e75cf782edd24d87287027ab70b471281a892c7a7d4a1b0a997e3, but the plan before it ends at model 5b57191d77eaa303e9997ca63ba739bd4fa7eafe5ce8322b6b78804a6d2e081c",
        ),
        (
            vec![fixture("01-create"), fixture("03-audit")],
            "carried plan 2 starts from model",
        ),
        (
            vec![fixture("01-create"), vector("sqlite-columns/02-add-column-and-index.plan.json")],
            "carried plan 2 is for service ledger-db; the plans before it are for service shop",
        ),
        (
            vec![postgres("01-create")],
            "carried plan 1: the plan is for postgres and the database is sqlite",
        ),
        (
            vec![fixture("01-create"), b"{}".to_vec()],
            "carried plan 2: the plan has no version",
        ),
        (
            vec![fixture("01-create"), b"{".to_vec()],
            "carried plan 2: read the plan: unexpected end of JSON input",
        ),
    ];
    for (plans, want) in cases {
        match start(&file, &plans, StartOptions::default()) {
            Err(err @ (Error::Refused(_) | Error::Document(_))) => {
                assert!(
                    err.to_string().contains(want),
                    "{err} does not say {want:?}"
                );
            }
            other => panic!("start = {other:?}, want {want:?}"),
        }
        assert!(!file.exists(), "a refused chain made the file");
    }
}

/// The journal mode stays as it is unless the start is asked for WAL.
#[test]
fn the_journal_mode_is_wal_when_asked() {
    let dir = TempDir::new();
    let file = dir.file("shop.sqlite");
    started(&file, &carried(1));
    assert_eq!(strings(&file, "PRAGMA journal_mode"), ["delete"]);
    let wal = || StartOptions {
        wal: true,
        ..StartOptions::default()
    };
    let outcome = start(&file, &carried(2), wal()).expect("start with WAL");
    assert_eq!(outcome.plans.len(), 1);
    assert_eq!(strings(&file, "PRAGMA journal_mode"), ["wal"]);
    // The copy reads on its own.
    let copy = outcome.backup.expect("02 has a contract step");
    assert_eq!(strings(&copy, "PRAGMA integrity_check"), ["ok"]);
    // A start with nothing to do sets it too, and a later one without the
    // option leaves it.
    let file = dir.file("current.sqlite");
    started(&file, &carried(2));
    start(&file, &carried(2), wal()).expect("start with WAL");
    started(&file, &carried(2));
    assert_eq!(strings(&file, "PRAGMA journal_mode"), ["wal"]);
}

/// The compiler's vectors that are chains start an empty file and a file
/// at every model on the way, and end at the catalog the runner alone
/// makes of them.
#[test]
fn the_compiler_vectors_start() {
    for case in ["sqlite-columns", "sqlite-rebuild"] {
        let docs: Vec<Vec<u8>> = case_paths(case).iter().map(|path| read(path)).collect();
        let plans: Vec<Plan> = docs.iter().map(|doc| must_read(doc)).collect();
        let last = plans.last().expect("the case has a plan");

        let tmp = TempDir::new();
        let reference = tmp.file("reference.sqlite");
        {
            let conn = open(&reference);
            let mut r = quiet(&conn);
            for plan in &plans {
                apply(&mut r, plan, Phase::All);
            }
        }
        for at in 0..=plans.len() {
            let file = tmp.file(&format!("at-{at}.sqlite"));
            if at > 0 {
                started(&file, &docs[..at]);
            }
            let outcome = started(&file, &docs);
            assert_eq!(
                outcome.plans,
                in_full(&plans[at..]),
                "{case}: from plan {at}"
            );
            assert_eq!(outcome.model, last.to, "{case}");
            let contracts = plans[at..]
                .iter()
                .any(|plan| plan.steps.iter().any(|step| step.phase == Phase::Contract));
            assert_eq!(
                outcome.backup.is_some(),
                at > 0 && contracts,
                "{case}: from plan {at}"
            );
            assert_eq!(
                catalog(&file),
                catalog(&reference),
                "{case}: from plan {at}"
            );
            assert_eq!(state(&file), state(&reference), "{case}: from plan {at}");
        }
    }
}
