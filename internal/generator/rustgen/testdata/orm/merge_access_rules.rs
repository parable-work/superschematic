//! fixture-merge-access-db's tables under per-record access on Postgres
//! (D147, D156, D181), through repositories opened for a caller: a merge
//! of a label the caller may not write, or into one, is not found; one
//! that would repoint a note the caller may not write is refused,
//! `out_of_reach` naming the table and no key of the note, and writes
//! nothing, where the same merge by repositories opened for no caller
//! repoints it; a party merges two labels only when its grants reach the
//! notes on the one merged away; and Aliased's resolve answers a party no
//! row for a brand its grants do not reach, a name another brand holds is
//! refused naming nothing of that brand, and a party whose grants reach two
//! brands merges one into the other. The brands' audience is everyone and
//! their callers parties, since the Rust behaviors' core reads no private
//! row yet.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::*;
use db::*;
use serde_json::{json, Value};
use superschematic_http_runtime::{orm::AccessCaller, GrantReach, PrincipalKind};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e02";

/// A party a grant names (D156).
const PARTY: &str = "7a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e01";

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// A user's access (D156).
fn user(subject: &str) -> AccessCaller {
    AccessCaller {
        subject: Some(subject.to_owned()),
        kind: PrincipalKind::User,
        ..AccessCaller::default()
    }
}

/// A party's access through grants, each a target and the paths its role
/// reaches rows by.
fn party(grants: &[(&str, &Value, &[(&str, &str)])]) -> AccessCaller {
    AccessCaller {
        subject: Some(PARTY.to_owned()),
        kind: PrincipalKind::Party,
        grants: Some(
            grants
                .iter()
                .enumerate()
                .map(|(i, (target, row, paths))| GrantReach {
                    id: format!("g{i}"),
                    target_type: (*target).to_owned(),
                    target_id: row["id"].as_str().unwrap().to_owned(),
                    reaches: paths
                        .iter()
                        .map(|(table, path)| ((*table).to_owned(), vec![(*path).to_owned()]))
                        .collect(),
                })
                .collect(),
        ),
        ..AccessCaller::default()
    }
}

/// The key of a row.
fn key(row: &Values) -> IdentityUUID {
    serde_json::from_value(row["id"].clone()).unwrap()
}

#[tokio::test]
async fn a_merge_keeps_to_its_caller_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_merge_access_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config
        .connect(tokio_postgres::NoTls)
        .await
        .expect("connect");
    tokio::spawn(connection);
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager)
        .max_size(2)
        .build()
        .expect("the pool");
    let ran = on_postgres(&pool).await;
    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
    ran.expect("the cases on Postgres");
    println!("the repositories ran on Postgres");
}

async fn on_postgres(pool: &deadpool_postgres::Pool) -> Result<(), Box<dyn std::error::Error>> {
    let mut client = pool.get().await?;
    client
        .batch_execute(include_str!("../postgres/create.sql"))
        .await?;
    let transaction = client.transaction().await?;
    // Opened for no caller: the writes that set the rows up read and write
    // every row, as a job's do.
    let open = || postgres::Repositories::new(&transaction).unwrap().acting(Some(ALICE.to_owned()));
    let orm = open();
    let as_caller = |caller: AccessCaller| open().with_access(caller);
    let labels = orm.label()?;
    let notes = orm.label_note()?;
    let label = |name: &str, owner: &str, audience: &str| values(json!({"name": name, "owner": owner, "audience": audience, "agentAccess": "readOnly"}));
    let note = |on: &Values, author: &str, audience: &str| values(json!({"labelId": on["id"], "author": author, "text": "a note", "audience": audience}));
    let notes_on = |on: &Values| values(json!({"labelId": {"eq": on["id"]}}));

    // A user who may not write one of the two labels merges neither.
    let mine = labels.untyped().create_one(&label("bug", ALICE, "private")).await?;
    let theirs = labels.untyped().create_one(&label("defect", BOB, "private")).await?;
    notes.untyped().create_one(&note(&theirs, BOB, "private")).await?;
    for (from, into) in [(&theirs, &mine), (&mine, &theirs)] {
        let refused = as_caller(user(ALICE)).label()?.merge_into(&key(from), &key(into)).await;
        assert!(matches!(refused, Err(Error::NotFound(_))), "{refused:?}");
    }
    assert_eq!(notes.untyped().find_many(Some(&notes_on(&theirs)), &FindOptions::default()).await?.rows.len(), 1);

    // A user who may not write a note on the label merged away merges
    // neither, the refusal naming the notes and no key of the note.
    let stays = labels.untyped().create_one(&label("hang", ALICE, "private")).await?;
    let goes = labels.untyped().create_one(&label("freeze", ALICE, "private")).await?;
    notes.untyped().create_one(&note(&goes, ALICE, "private")).await?;
    let hidden = notes.untyped().create_one(&note(&goes, BOB, "private")).await?;
    let refused = as_caller(user(ALICE)).label()?.merge_into(&key(&goes), &key(&stays)).await;
    match &refused {
        Err(Error::Merge { code: "out_of_reach", message }) => {
            assert!(message.contains("LabelNote"), "{message}");
            assert!(!message.contains(hidden["id"].as_str().unwrap()), "the refusal names the note: {message}");
        }
        other => panic!("a merge past a note the caller may not write answered {other:?}"),
    }
    assert_eq!(notes.untyped().find_many(Some(&notes_on(&goes)), &FindOptions::default()).await?.rows.len(), 2);
    assert!(labels.untyped().get_one(&goes["id"]).await.is_ok(), "a refused merge deleted the label");

    // Opened for no caller, the same merge repoints both notes.
    let merged = labels.merge_into(&key(&goes), &key(&stays)).await?;
    assert_eq!(serde_json::to_value(&merged)?, json!({"repointed": {"LabelNote": 2}, "deleted": {}}));
    assert_eq!(notes.untyped().find_many(Some(&notes_on(&stays)), &FindOptions::default()).await?.rows.len(), 2);

    // A user who may write both labels and every note on the one merged
    // away merges it.
    let stays = labels.untyped().create_one(&label("crash", ALICE, "private")).await?;
    let goes = labels.untyped().create_one(&label("fault", ALICE, "private")).await?;
    notes.untyped().create_one(&note(&goes, ALICE, "private")).await?;
    notes.untyped().create_one(&note(&goes, BOB, "everyone")).await?;
    let merged = as_caller(user(ALICE)).label()?.merge_into(&key(&goes), &key(&stays)).await?;
    assert_eq!(serde_json::to_value(&merged)?, json!({"repointed": {"LabelNote": 2}, "deleted": {}}));

    // A party merges two labels only when its grants reach the notes on
    // the one merged away.
    let stays = labels.untyped().create_one(&label("tidy", ALICE, "everyone")).await?;
    let goes = labels.untyped().create_one(&label("neat", ALICE, "everyone")).await?;
    notes.untyped().create_one(&note(&goes, ALICE, "everyone")).await?;
    let refused = as_caller(party(&[("Label", &Value::Object(stays.clone()), &[]), ("Label", &Value::Object(goes.clone()), &[])]))
        .label()?
        .merge_into(&key(&goes), &key(&stays))
        .await;
    assert!(matches!(&refused, Err(Error::Merge { code: "out_of_reach", message }) if message.contains("LabelNote")), "{refused:?}");
    let reach: &[(&str, &str)] = &[("LabelNote", "label")];
    let merged = as_caller(party(&[("Label", &Value::Object(stays.clone()), reach), ("Label", &Value::Object(goes.clone()), reach)]))
        .label()?
        .merge_into(&key(&goes), &key(&stays))
        .await?;
    assert_eq!(serde_json::to_value(&merged)?, json!({"repointed": {"LabelNote": 1}, "deleted": {}}));

    // Aliased's resolve answers a party no row for a brand its grants do
    // not reach.
    let brands = orm.brand()?;
    let brand = |name: &str| values(json!({"name": name, "owner": ALICE, "audience": "everyone", "agentAccess": "followRole"}));
    let acme = Value::Object(brands.untyped().create_one(&brand("Acme")).await?);
    let globex = Value::Object(brands.untyped().create_one(&brand("Globex")).await?);
    let reached = || as_caller(party(&[("Brand", &acme, &[])]));
    let resolve = |name: &str| json!({"name": name});
    let found = reached().brand()?.untyped().table_operation("name", "resolve", &resolve("globex")).await?;
    assert_eq!(found, json!({"row": null}), "resolve globex for a party its grants do not reach");
    let found = reached().brand()?.untyped().table_operation("name", "resolve", &resolve("ACME")).await?;
    assert_eq!(found["row"]["key"], acme["id"], "{found}");
    let found = brands.untyped().table_operation("name", "resolve", &resolve("globex")).await?;
    assert_eq!(found["row"]["key"], globex["id"], "{found}");

    // A name another brand holds is refused naming nothing of that brand.
    let refused = reached()
        .brand()?
        .untyped()
        .operation("name", "rename", &acme["id"], &json!({"name": "GLOBEX"}), &WriteOptions::default())
        .await;
    match &refused {
        Err(Error::Refused(refusal)) => {
            assert_eq!(refusal.details["vetoCode"], "name_taken", "{refusal:?}");
            assert_eq!(refusal.details["vetoDetails"], json!({"name": "GLOBEX", "normalized": "globex"}), "{refusal:?}");
            let text = format!("{} {}", refusal.message, refusal.details);
            assert!(!text.contains(globex["id"].as_str().unwrap()), "the refusal names the brand: {text}");
        }
        other => panic!("a rename to a name another brand holds answered {other:?}"),
    }

    // A party whose grants reach both brands merges one into the other.
    let stays = Value::Object(brands.untyped().create_one(&brand("Pied Piper")).await?);
    let goes = Value::Object(brands.untyped().create_one(&brand("Piper Net")).await?);
    as_caller(party(&[("Brand", &stays, &[]), ("Brand", &goes, &[])]))
        .brand()?
        .untyped()
        .merge_into(&goes["id"], &stays["id"])
        .await?;
    let found = brands.untyped().table_operation("name", "resolve", &resolve("piper net")).await?;
    assert_eq!((&found["row"]["key"], &found["row"]["alias"]), (&stays["id"], &json!(true)), "{found}");

    transaction.commit().await?;
    Ok(())
}
