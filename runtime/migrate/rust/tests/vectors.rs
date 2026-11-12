//! The compiler's plan vectors (`runtime/migrate/testdata/plans`, D27,
//! "Testing"), applied as the Go runner's `TestCompilerVectors` applies
//! them to a SQLite file: one directory per case, each with
//! `NN-<name>.plan.json` files applied in name order on a fresh file, the
//! first from an empty one. A plan starts from the model the plan before it
//! ends at or, superseding that plan's contract, from the model it holds
//! between its phases; then only the earlier plan's expand steps run (D27,
//! amended).

mod support;

use superschematic_migrate::{read_model, Phase, PLAN_PHASE_EXPANDED};
use support::{
    apply, expanded, open, quiet, resume_after_every_step, sqlite_cases, status, steps_in, strings,
    TempDir,
};

/// The chain ends at the last plan's to, a second run of each plan does
/// nothing, and a plan from the model the one before it holds between its
/// phases supersedes that plan's contract.
#[test]
fn applies() {
    for (case, chain, phases) in sqlite_cases() {
        let dir = TempDir::new();
        let conn = open(&dir.file("vector.db"));
        let mut r = quiet(&conn);
        for (i, plan) in chain.iter().enumerate() {
            let result = apply(&mut r, plan, phases[i]);
            let superseded =
                (i > 0 && phases[i - 1] == Phase::Expand).then(|| chain[i - 1].hash.clone());
            assert_eq!(result.superseded, superseded, "{case}: plan {}", plan.hash);
            if phases[i] == Phase::Expand {
                // Expand only: the database holds the plan's expanded
                // model, and a second run of expand does nothing.
                assert!(result.expand_done && !result.finished, "{case}: {result:?}");
                assert_eq!(result.ran.len(), steps_in(plan, Phase::Expand), "{case}");
                let st = status(&mut r, &plan.service);
                assert_eq!(st.model_hash, expanded(plan), "{case}");
                assert_eq!(
                    st.plan_phase.as_deref(),
                    Some(PLAN_PHASE_EXPANDED),
                    "{case}"
                );
                let again = apply(&mut r, plan, Phase::Expand);
                assert!(again.ran.is_empty() && !again.finished, "{case}: {again:?}");
                continue;
            }
            assert!(result.finished, "{case}: {result:?}");
            assert_eq!(result.ran.len(), plan.steps.len(), "{case}");
            let again = apply(&mut r, plan, Phase::All);
            assert!(
                again.already_applied && again.ran.is_empty(),
                "{case}: {again:?}"
            );
        }
        let last = chain.last().expect("the case has a plan");
        let st = status(&mut r, &last.service);
        assert_eq!(
            (st.model_hash.as_str(), st.plan_hash),
            (last.to.as_str(), None),
            "{case}"
        );
    }
}

/// A failure injected after any step resumes to the same catalog.
#[test]
fn resumes_after_a_failure() {
    for (_, chain, phases) in sqlite_cases() {
        resume_after_every_step(&chain, &phases);
    }
}

/// Two runners at once, each on a connection and a thread of its own, run
/// every step once between them.
#[test]
fn two_runners_serialize() {
    for (case, chain, phases) in sqlite_cases() {
        let dir = TempDir::new();
        let path = dir.file("vector.db");
        for (plan, phase) in chain.iter().zip(&phases) {
            std::thread::scope(|scope| {
                let runners: Vec<_> = (0..2)
                    .map(|_| {
                        scope.spawn(|| {
                            let conn = open(&path);
                            let mut r = quiet(&conn);
                            r.apply(plan, *phase).map(|_| ())
                        })
                    })
                    .collect();
                for runner in runners {
                    if let Err(err) = runner.join().expect("a runner's thread") {
                        panic!("{case}: plan {}: {err}", plan.hash);
                    }
                }
            });
            let steps = match phase {
                Phase::Expand => steps_in(plan, Phase::Expand),
                _ => plan.steps.len(),
            };
            let logged = strings(
                &path,
                &format!(
                    "SELECT count(*) FROM superschematic_migrations WHERE plan_hash = '{}'",
                    plan.hash
                ),
            );
            assert_eq!(logged, [steps.to_string()], "{case}: plan {}", plan.hash);
        }
        let last = chain.last().expect("the case has a plan");
        let conn = open(&path);
        assert_eq!(
            status(&mut quiet(&conn), &last.service).model_hash,
            last.to,
            "{case}"
        );
    }
}

/// Every plan of a case is refused on a file at another model.
#[test]
fn the_wrong_baseline_is_refused() {
    for (case, chain, _) in sqlite_cases() {
        let dir = TempDir::new();
        let conn = open(&dir.file("vector.db"));
        let mut r = quiet(&conn);
        let decoy = read_model(
            format!(
                r#"{{"version":1,"dialect":"sqlite","service":"{}","decoy":true}}"#,
                chain[0].service
            )
            .as_bytes(),
        )
        .expect("the decoy reads");
        r.adopt(&decoy).expect("adopt the decoy");
        for plan in &chain {
            match r.apply(plan, Phase::All) {
                Err(err) if err.is_refusal() => {}
                other => panic!("{case}: plan {} on another model = {other:?}", plan.hash),
            }
        }
        assert_eq!(
            status(&mut r, &decoy.service).model_hash,
            decoy.hash,
            "{case}"
        );
    }
}
