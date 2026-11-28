//! merge_into of fixture-merge-sqlite-db's Label and Customer (D147, D116),
//! fixture-merge-db's tables but LabelSlot, on a
//! SQLite file and, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, on
//! Postgres, through `$run`: a document that carries both labels keeps the
//! one that pointed at the label that stays and loses the other; a
//! subscription a person holds to both is stamped deleted, softly, where
//! its unique index holds live rows alone; a code the index over an
//! expression would refuse is deleted; a note is repointed with who changed
//! it; the label merged away is deleted softly; a merge of a label into
//! itself, into a label under it, of one gone, by a party whose grant
//! reaches one of the two labels alone (D156, D181), and by one whose
//! grants reach both labels and none of the rows that reference them, is
//! refused and writes nothing; and a customer, whose status is a Workflow,
//! is deleted through
//! the core once its purchases and its profile are folded.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};
use superschematic_http_runtime::{orm::AccessCaller, GrantReach, PrincipalKind};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";

/// A party a grant names (D156).
const PARTY: &str = "7a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e01";

/// A call of a SQLite repository, which answers at once.
macro_rules! at_once {
    ($call:expr) => {
        $call
    };
}

/// A call of a Postgres repository, which is awaited.
macro_rules! awaited {
    ($call:expr) => {
        $call.await
    };
}

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// A merge's result as JSON, to compare.
fn counts<T: serde::Serialize>(result: &T) -> Value {
    serde_json::to_value(result).unwrap()
}

/// The cases every dialect's repositories are held to, over `$orm`.
macro_rules! cases {
    ($orm:expr, $run:ident) => {{
        let orm = $orm.acting(Some(ALICE.to_owned()));
        let labels = orm.label().unwrap();
        let none = WriteOptions::default();
        let label = |name: &str| values(json!({"name": name}));
        let bug = $run!(labels.untyped().create_one(&label("bug"))).expect("create bug");
        let defect = $run!(labels.untyped().create_one(&label("defect"))).expect("create defect");
        let key = |row: &Values| -> IdentityUUID { serde_json::from_value(row["id"].clone()).unwrap() };

        let documents = orm.document().unwrap();
        let d1 = $run!(documents.untyped().create_one(&values(json!({"title": "one"})))).expect("d1");
        let d2 = $run!(documents.untyped().create_one(&values(json!({"title": "two"})))).expect("d2");
        let tags = orm.document_label().unwrap();
        for (document, label) in [(&d1, &bug), (&d1, &defect), (&d2, &defect)] {
            $run!(tags.untyped().create_one(&values(json!({"documentId": document["id"], "labelId": label["id"]}))))
                .expect("label a document");
        }
        let subscriptions = orm.subscription().unwrap();
        for (subscriber, label) in [("alice", &bug), ("alice", &defect), ("bob", &defect)] {
            $run!(subscriptions.untyped().create_one(&values(json!({"subscriber": subscriber, "labelId": label["id"]}))))
                .expect("subscribe");
        }
        let codes = orm.label_code().unwrap();
        for (label, code) in [(&bug, "X1"), (&defect, "x1"), (&defect, "Y2")] {
            $run!(codes.untyped().create_one(&values(json!({"labelId": label["id"], "code": code}))))
                .expect("a code");
        }
        let notes = orm.label_note().unwrap();
        let note = $run!(notes.untyped().create_one(&values(json!({"labelId": defect["id"], "text": "old"}))))
            .expect("a note");

        // Refused, writing nothing: into itself, into a label under it,
        // and a label that is gone.
        let refused = $run!(labels.merge_into(&key(&defect), &key(&defect)));
        assert!(matches!(refused, Err(Error::Merge { code: "self", .. })), "{refused:?}");
        let child = $run!(labels.untyped().create_one(&values(json!({"name": "child", "parentId": defect["id"]}))))
            .expect("a label under defect");
        let refused = $run!(labels.merge_into(&key(&defect), &key(&child)));
        assert!(matches!(refused, Err(Error::Merge { code: "self_reference", .. })), "{refused:?}");
        let held = $run!(tags.untyped().find_many(Some(&values(json!({"labelId": {"eq": defect["id"]}}))), &FindOptions::default()))
            .expect("the defect's documents");
        assert_eq!(held.rows.len(), 2, "a refused merge wrote: {:?}", held.rows);

        // Refused as not found, writing nothing, for a caller who may not
        // write one of the two (D156, D181): a party whose grant reaches
        // the defect alone, merging it into the bug it does not see, and
        // one whose grant reaches the bug alone.
        for (reached, gone) in [(&defect, &bug), (&bug, &defect)] {
            let party = $orm.acting(Some(ALICE.to_owned())).with_access(AccessCaller {
                subject: Some(PARTY.to_owned()),
                kind: PrincipalKind::Party,
                grants: Some(vec![GrantReach {
                    id: "g1".to_owned(),
                    target_type: "Label".to_owned(),
                    target_id: reached["id"].as_str().unwrap().to_owned(),
                    reaches: Default::default(),
                }]),
                ..AccessCaller::default()
            });
            let refused = $run!(party.label().unwrap().merge_into(&key(&defect), &key(&bug)));
            assert!(matches!(&refused, Err(Error::NotFound(message)) if message.contains(gone["id"].as_str().unwrap())), "{refused:?}");
        }
        // Refused, writing nothing, for a party whose grants reach both
        // labels and none of the rows that reference the defect (D156):
        // the first table that holds one, by the steps' order, names it.
        let both = $orm.acting(Some(ALICE.to_owned())).with_access(AccessCaller {
            subject: Some(PARTY.to_owned()),
            kind: PrincipalKind::Party,
            grants: Some(
                [&defect, &bug]
                    .iter()
                    .enumerate()
                    .map(|(i, row)| GrantReach {
                        id: format!("g{i}"),
                        target_type: "Label".to_owned(),
                        target_id: row["id"].as_str().unwrap().to_owned(),
                        reaches: Default::default(),
                    })
                    .collect(),
            ),
            ..AccessCaller::default()
        });
        let refused = $run!(both.label().unwrap().merge_into(&key(&defect), &key(&bug)));
        assert!(
            matches!(&refused, Err(Error::Merge { code: "out_of_reach", message }) if message.contains("DocumentLabel")),
            "{refused:?}"
        );
        let held = $run!(tags.untyped().find_many(Some(&values(json!({"labelId": {"eq": defect["id"]}}))), &FindOptions::default()))
            .expect("the defect's documents");
        assert_eq!(held.rows.len(), 2, "a merge the caller may not make wrote: {:?}", held.rows);
        let kept = $run!(labels.untyped().get_one(&defect["id"]));
        assert!(kept.is_ok(), "a merge the caller may not make deleted the defect: {kept:?}");

        let merged = $run!(labels.merge_into(&key(&defect), &key(&bug))).expect("merge defect into bug");
        assert_eq!(
            counts(&merged),
            json!({
                "repointed": {"DocumentLabel": 1, "Label": 1, "LabelCode": 1, "LabelNote": 1, "Subscription": 1},
                "deleted": {"DocumentLabel": 1, "LabelCode": 1, "Subscription": 1}
            })
        );
        let on_bug = $run!(tags.untyped().find_many(Some(&values(json!({"labelId": {"eq": bug["id"]}}))), &FindOptions::default()))
            .expect("bug's documents");
        assert_eq!(on_bug.rows.len(), 2, "{:?}", on_bug.rows);
        let moved = $run!(notes.untyped().get_one(&note["id"])).expect("the note");
        assert_eq!(moved["labelId"], bug["id"], "{moved:?}");
        let by: IdentityUUID = serde_json::from_value(moved["updatedBy"].clone()).expect("who changed the note");
        assert_eq!(by, ALICE.parse::<IdentityUUID>().unwrap(), "{moved:?}");
        let gone = $run!(labels.untyped().get_one(&defect["id"]));
        assert!(matches!(gone, Err(Error::NotFound(_))), "{gone:?}");
        let again = $run!(labels.merge_into(&key(&defect), &key(&bug)));
        assert!(matches!(again, Err(Error::NotFound(_))), "{again:?}");

        // A customer, whose status is a Workflow, deleted through the core.
        let customers = orm.customer().unwrap();
        let ann = $run!(customers.untyped().create_one(&values(json!({"name": "Ann"})))).expect("Ann");
        let anne = $run!(customers.untyped().create_one(&values(json!({"name": "Anne"})))).expect("Anne");
        let purchases = orm.purchase().unwrap();
        $run!(purchases.untyped().create_one(&values(json!({"customerId": anne["id"], "total": 10.0}))))
            .expect("a purchase");
        let profiles = orm.customer_profile().unwrap();
        for (customer, bio) in [(&ann, "one"), (&anne, "two")] {
            $run!(profiles.untyped().create_one(&values(json!({"customerId": customer["id"], "bio": bio}))))
                .expect("a profile");
        }
        let merged = $run!(customers.merge_into(&key(&anne), &key(&ann))).expect("merge Anne into Ann");
        assert_eq!(
            counts(&merged),
            json!({"repointed": {"Purchase": 1}, "deleted": {"CustomerProfile": 1}})
        );
        let gone = $run!(customers.untyped().get_one(&anne["id"]));
        assert!(matches!(gone, Err(Error::NotFound(_))), "{gone:?}");
        let plain = $run!(documents.merge_into(&key(&d1), &key(&d2)));
        assert!(matches!(plain, Err(Error::Invalid(_))), "{plain:?}");
        let _ = none;
    }};
}

#[test]
fn a_merge_folds_one_row_into_another_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    cases!(sqlite::Repositories::new(&transaction).unwrap(), at_once);
    transaction.commit().expect("commit");
    println!("the repositories ran on SQLite");
}

#[tokio::test]
async fn a_merge_folds_one_row_into_another_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_merge_rules_rust_{}", std::process::id());
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
    cases!(postgres::Repositories::new(&transaction).unwrap(), awaited);
    transaction.commit().await?;
    Ok(())
}
