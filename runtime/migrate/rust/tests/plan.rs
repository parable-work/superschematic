//! Reading a plan and a model, as the Go runner's `plan_test.go` holds
//! `ReadPlan` and `ReadModel`: the same documents are accepted, and the
//! same ones refused with the same words.

mod support;

use serde_json::{json, Map, Value};
use superschematic_migrate::{canonical_json, hash, read_model, read_plan, Dialect, Error, Phase};
use support::{fixture, must_read, read, seal, steps};

/// The indented fixture and its canonical JSON read as the same plan.
#[test]
fn read_plan_accepts_any_encoding() {
    let doc = fixture("02-evolve");
    let canonical = canonical_json(&doc).expect("the fixture is JSON");
    let (indented, compact) = (must_read(&doc), must_read(canonical.as_bytes()));
    assert_eq!(indented, compact);
    assert_eq!(indented.model().canonical, compact.model().canonical);
    assert_eq!(indented.steps.len(), 4);
    assert!(indented.steps[1].foreign_keys_off);
    assert_eq!(indented.steps[3].phase, Phase::Contract);
    assert_eq!(indented.dialect, Dialect::Sqlite);
    assert_eq!(indented.service, "shop");
    assert_eq!(indented.steps[0].op, "addColumn");
    assert_eq!(indented.steps[0].subject, "table/order/column/note");
    assert_eq!(
        indented.steps[0].statements,
        [r#"ALTER TABLE "order" ADD COLUMN note TEXT"#]
    );
}

/// A plan's hazards and renames read, though the runner does not act on
/// them.
#[test]
fn read_plan_reads_what_the_runner_ignores() {
    let doc = read(&support::vectors_dir().join("sqlite-supersede/02-replace-email.plan.json"));
    let plan = must_read(&doc);
    let hazards = &plan.steps[1].hazards;
    assert_eq!(hazards.len(), 2);
    assert_eq!(hazards[0].class, "destructive");
    assert_eq!(hazards[0].id, "destructive:table/account/column/email");
    assert_eq!(hazards[1].subject, "table/account/column/email");
    assert!(plan.steps[0].hazards.is_empty());
    assert!(plan.steps[0].recovery.is_empty());

    let renamed = must_read(&seal(
        &doc,
        |plan| {
            plan.insert(
                "renames".to_string(),
                json!(["account.email=account.nickname"]),
            );
        },
        true,
    ));
    assert_eq!(renamed.renames, ["account.email=account.nickname"]);
}

fn step(plan: &mut Map<String, Value>, at: usize) -> &mut Map<String, Value> {
    steps(plan)[at]
        .as_object_mut()
        .expect("a step is an object")
}

fn model<'a>(plan: &'a mut Map<String, Value>, name: &str) -> &'a mut Map<String, Value> {
    plan.get_mut(name)
        .and_then(Value::as_object_mut)
        .expect("the plan has the model")
}

/// read_plan refuses, before anything runs, a plan of another version, a
/// plan changed after it was sealed, a toModel that does not hash to `to`,
/// an expandedModel that does not hash to `expanded` or is of another
/// service, and steps the runner cannot run as written.
#[test]
fn read_plan_refuses() {
    let evolve = fixture("02-evolve");
    let sealed = |edit: &dyn Fn(&mut Map<String, Value>)| seal(&evolve, edit, true);
    let unsealed = |edit: &dyn Fn(&mut Map<String, Value>)| seal(&evolve, edit, false);
    let changed = |edit: &dyn Fn(&mut Map<String, Value>)| {
        let mut plan: Map<String, Value> = serde_json::from_slice(&evolve).expect("JSON");
        edit(&mut plan);
        serde_json::to_vec(&plan).expect("the plan encodes")
    };
    let postgres = read(&support::fixtures_dir().join("../postgres/02-evolve.plan.json"));
    let cases: Vec<(&str, Vec<u8>, &str)> = vec![
        (
            "an unknown version",
            sealed(&|p| {
                p.insert("version".to_string(), json!(2));
            }),
            "version 2; this runner reads version 1",
        ),
        (
            "no version",
            sealed(&|p| {
                p.remove("version");
            }),
            "no version",
        ),
        (
            "an edited statement",
            changed(&|p| {
                step(p, 0).insert("statements".to_string(), json!(["DROP TABLE customer"]));
            }),
            "changed after it was written",
        ),
        (
            "no hash",
            changed(&|p| {
                p.remove("hash");
            }),
            "no hash",
        ),
        (
            "a toModel that is not to",
            unsealed(&|p| {
                model(p, "toModel").insert("service".to_string(), json!("other"));
            }),
            "not to the plan's to",
        ),
        (
            "no toModel",
            unsealed(&|p| {
                p.remove("toModel");
            }),
            "no toModel",
        ),
        (
            "an expandedModel that is not expanded",
            unsealed(&|p| {
                model(p, "expandedModel").insert("tables".to_string(), json!([]));
            }),
            "the plan's expandedModel hashes to",
        ),
        (
            "expanded with no expandedModel",
            sealed(&|p| {
                p.remove("expandedModel");
            }),
            "has expanded but no expandedModel",
        ),
        (
            "an expandedModel with no expanded",
            unsealed(&|p| {
                p.remove("expanded");
            }),
            "has an expandedModel but no expanded",
        ),
        (
            "an expandedModel of another service",
            sealed(&|p| {
                model(p, "expandedModel").insert("service".to_string(), json!("other"));
            }),
            "expandedModel is of sqlite service other",
        ),
        (
            "an unknown dialect",
            sealed(&|p| {
                p.insert("dialect".to_string(), json!("mysql"));
            }),
            r#"dialect is "mysql""#,
        ),
        (
            "no service",
            sealed(&|p| {
                p.insert("service".to_string(), json!(""));
            }),
            "names no service",
        ),
        (
            "a step out of order",
            sealed(&|p| {
                step(p, 1).insert("index".to_string(), json!(7));
            }),
            "step 2 has index 7",
        ),
        (
            "a null step",
            sealed(&|p| steps(p)[2] = Value::Null),
            "the plan's step 3 is null",
        ),
        (
            "an expand step after a contract step",
            sealed(&|p| {
                step(p, 3).insert("phase".to_string(), json!("expand"));
                step(p, 2).insert("phase".to_string(), json!("contract"));
            }),
            "expand step after a contract step",
        ),
        (
            "an unknown phase",
            sealed(&|p| {
                step(p, 0).insert("phase".to_string(), json!("later"));
            }),
            r#"phase "later""#,
        ),
        (
            "a SQLite step outside a transaction",
            sealed(&|p| {
                step(p, 0).insert("transactional".to_string(), json!(false));
            }),
            "SQLite runs every step in a transaction",
        ),
        (
            "a Postgres step with foreign keys off",
            seal(
                &postgres,
                |p| {
                    step(p, 0).insert("foreignKeysOff".to_string(), json!(true));
                },
                true,
            ),
            "only a SQLite step does",
        ),
        (
            "a document that is not an object",
            b"[]".to_vec(),
            "a plan is a JSON object",
        ),
        (
            "trailing data",
            [evolve.as_slice(), b"{}"].concat(),
            "trailing data",
        ),
        (
            "a statement that is no string",
            sealed(&|p| {
                step(p, 0).insert("statements".to_string(), json!([1]));
            }),
            "read the plan: statements is not an array of strings",
        ),
        (
            "an index that is no integer",
            sealed(&|p| {
                step(p, 0).insert("index".to_string(), json!(1.5));
            }),
            "read the plan: index is not an integer",
        ),
        (
            "a toModel that is no object",
            unsealed(&|p| {
                p.insert("toModel".to_string(), json!([]));
            }),
            "the plan's toModel: read the model: a model is a JSON object",
        ),
    ];
    for (name, doc, refuse) in cases {
        match read_plan(&doc) {
            Err(err) if err.to_string().contains(refuse) => {}
            other => panic!("{name}: read_plan = {other:?}, want it refused with {refuse:?}"),
        }
    }
}

/// A plan the runner will not run is a refusal; a document that does not
/// read is not one, as in the Go runner.
#[test]
fn refusals_are_refusals() {
    let doc = seal(
        &fixture("01-create"),
        |p| {
            p.insert("version".to_string(), json!(2));
        },
        true,
    );
    assert!(matches!(read_plan(&doc), Err(Error::Refused(_))));
    assert!(matches!(read_plan(b"{"), Err(Error::Document(_))));
    assert!(matches!(read_plan(b"[]"), Err(Error::Document(_))));
}

/// The Postgres fixtures read: the runner refuses them when it is asked to
/// apply one, not when it reads one.
#[test]
fn read_plan_reads_a_postgres_plan() {
    let doc = read(&support::fixtures_dir().join("../postgres/02-evolve.plan.json"));
    let plan = must_read(&doc);
    assert_eq!(plan.dialect, Dialect::Postgres);
    assert!(plan.steps.iter().any(|step| !step.transactional));
}

/// A model's service and dialect key its state row; the hash is over its
/// canonical JSON.
#[test]
fn read_model_reads_the_keys() {
    let model =
        read_model(br#"{"version": 1, "service": "shop", "dialect": "sqlite", "tables": []}"#)
            .expect("the model reads");
    assert_eq!(model.service, "shop");
    assert_eq!(model.dialect, Dialect::Sqlite);
    assert_eq!(
        model.canonical,
        r#"{"dialect":"sqlite","service":"shop","tables":[],"version":1}"#
    );
    assert_eq!(model.hash, hash(model.canonical.as_bytes()));
    for (doc, refuse) in [
        (r#"{"dialect": "sqlite"}"#, "names no service"),
        (
            r#"{"service": "shop", "dialect": "oracle"}"#,
            "not postgres or sqlite",
        ),
        (r#"{"service": "shop"}"#, "not postgres or sqlite"),
        (r#"null"#, "names no service"),
        (r#"[]"#, "read the model"),
        (
            r#"{"service": 7, "dialect": "sqlite"}"#,
            "read the model: service is not a string",
        ),
        (
            r#"{"service": "shop", "dialect": "sqlite"} x"#,
            "trailing data",
        ),
    ] {
        match read_model(doc.as_bytes()) {
            Err(err) if err.to_string().contains(refuse) => {}
            other => panic!("read_model({doc}) = {other:?}, want {refuse:?}"),
        }
    }
}
