//! The repositories of fixture-workflow-db, whose tables' fields are
//! Workflows, over the behaviors' core (D103, D116): the same cases on a
//! SQLite file and, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, on
//! Postgres. The SQLite repositories answer at once and the Postgres ones
//! are awaited, so the cases are written once and run through `$run`.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};

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

/// A row as JSON, by member.
fn members<T: serde::Serialize>(row: &T) -> Value {
    serde_json::to_value(row).expect("a row serializes")
}

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// The cases every dialect's repositories are held to, over `$orm`.
macro_rules! cases {
    ($orm:ident, $run:ident) => {{
        let orders = $orm.order().unwrap();
        let shipments = $orm.shipment().unwrap();

        // A create runs the core: each Workflow starts in its initial
        // state, stamped when it entered it, at version 1.
        let desk = $run!(orders.create_one(&OrderCreate {
            id: None,
            title: "Desk".to_owned(),
            placed_at: Some(serde_json::from_value(json!("2026-10-14T12:00:00.125Z")).unwrap()),
        }))
        .expect("create the order");
        let created = members(&desk);
        assert_eq!(created["title"], "Desk");
        assert_eq!(created["status"], "draft");
        assert!(created["statusEnteredAt"].is_string(), "{created}");
        assert_eq!(created["_version"], 1);
        assert_eq!(created["placedAt"], "2026-10-14T12:00:00.125Z");
        let parcel = $run!(shipments.create_one(&ShipmentCreate {
            id: None,
            reference: "S-1".to_owned(),
        }))
        .expect("create the shipment");
        let parcel = members(&parcel);
        assert_eq!(parcel["status"], "pending");
        assert_eq!(parcel["paymentStatus"], "unpaid");
        assert!(parcel["statusEnteredAt"].is_string(), "{parcel}");
        assert!(parcel["paymentStatusEnteredAt"].is_string(), "{parcel}");
        assert_eq!(parcel["_version"], 1);

        // A create that names its key keeps it, in either form of a UUID,
        // and a second one of that key is the core's conflict.
        let named = $run!(orders.untyped().create_one(&values(json!({
            "id": "11111111-1111-4111-8111-111111111111",
            "title": "Lamp",
        }))))
        .expect("create the order by its key");
        assert_eq!(named["status"], "draft");
        let again = $run!(orders.untyped().create_one(&values(json!({
            "id": "11111111-1111-4111-8111-111111111111",
            "title": "Lamp",
        }))));
        match again {
            Err(Error::Refused(refusal)) => {
                assert_eq!(refusal.code, "conflict");
                assert_eq!(refusal.status, 409);
            }
            other => panic!("a second create of the key answered {other:?}"),
        }

        // An update writes the own fields, moves the version, and leaves
        // what the behaviors own as it was.
        let renamed = $run!(orders.update_one(
            &desk.id,
            &OrderUpdate {
                title: Some("Standing desk".to_owned()),
                placed_at: Some(None),
            },
            &WriteOptions::default(),
        ))
        .expect("update the order");
        let renamed = members(&renamed);
        assert_eq!(renamed["title"], "Standing desk");
        assert_eq!(renamed["placedAt"], Value::Null);
        assert_eq!(renamed["status"], "draft");
        assert_eq!(renamed["statusEnteredAt"], created["statusEnteredAt"]);
        assert_eq!(renamed["_version"], 2);

        // An update that names a field a behavior owns is the core's
        // refusal, and writes nothing; so is a create that names one.
        let key = created["id"].clone();
        let owned = $run!(orders.untyped().update_one(
            &key,
            &values(json!({"title": "Moved", "status": "paid"})),
            &WriteOptions::default(),
        ));
        match owned {
            Err(Error::Refused(refusal)) => {
                assert_eq!(refusal.code, "invalid_instance", "{refusal:?}");
                assert_eq!(refusal.status, 422);
            }
            other => panic!("an update of an owned field answered {other:?}"),
        }
        let kept = $run!(orders.untyped().get_one(&key)).unwrap();
        assert_eq!(kept["title"], "Standing desk");
        assert_eq!(kept["_version"], 2);
        let started = $run!(orders
            .untyped()
            .create_one(&values(json!({"title": "Chair", "status": "paid"}))));
        assert!(
            matches!(&started, Err(Error::Refused(refusal)) if refusal.code == "invalid_instance"),
            "a create of an owned field answered {started:?}"
        );

        // A write of many rows at once, and an upsert, are refused by
        // name: a behavior table's rows are written a row at a time.
        let many = $run!(orders.update_many(
            None,
            &OrderUpdate {
                title: Some("All".to_owned()),
                placed_at: None,
            },
        ));
        assert!(matches!(many, Err(Error::Invalid(_))), "{many:?}");

        // A behavior's operation runs through the core too, under the
        // member its use sits at: a Workflow's transition moves its field
        // and the row's version, one the config has none for is vetoed,
        // and one that names a permission is forbidden to a caller who
        // holds none.
        let options = WriteOptions::default();
        let paid: Value = $run!(orders.operation(
            "status",
            "transition",
            &desk.id,
            &json!({"to": "paid"}),
            &options,
        ))
        .expect("pay the order");
        assert_eq!(paid["to"], "paid");
        let moved = $run!(orders.untyped().get_one(&key)).unwrap();
        assert_eq!(moved["status"], "paid");
        assert_eq!(moved["_version"], 3);
        assert_ne!(moved["statusEnteredAt"], Value::Null);
        for (to, code, status) in [("draft", "vetoed", 409), ("shipped", "forbidden", 403)] {
            let refused = $run!(orders.untyped().operation(
                "status",
                "transition",
                &key,
                &json!({"to": to}),
                &options,
            ));
            match refused {
                Err(Error::Refused(refusal)) => {
                    assert_eq!(refusal.code, code, "{refusal:?}");
                    assert_eq!(refusal.status, status);
                }
                other => panic!("a move to {to} answered {other:?}"),
            }
        }
        for (member, operation) in [("status", "archive"), ("title", "transition")] {
            let unknown = $run!(orders.untyped().operation(
                member,
                operation,
                &key,
                &json!({}),
                &options,
            ));
            assert!(matches!(unknown, Err(Error::Invalid(_))), "{unknown:?}");
        }
        let missing = $run!(orders.untyped().operation(
            "status",
            "transition",
            &json!("33333333-3333-4333-8333-333333333333"),
            &json!({"to": "paid"}),
            &options,
        ));
        assert!(matches!(missing, Err(Error::NotFound(_))), "{missing:?}");

        // A delete removes the row through the core; a key no row holds is
        // not found, for an update and a delete alike.
        $run!(orders.delete_one(&desk.id, &WriteOptions::default())).expect("delete the order");
        assert!(matches!(
            $run!(orders.get_one(&desk.id)),
            Err(Error::NotFound(_))
        ));
        assert!(matches!(
            $run!(orders.delete_one(&desk.id, &WriteOptions::default())),
            Err(Error::NotFound(_))
        ));
        let gone = $run!(orders.update_one(
            &desk.id,
            &OrderUpdate {
                title: Some("Gone".to_owned()),
                placed_at: None,
            },
            &WriteOptions::default(),
        ));
        assert!(matches!(gone, Err(Error::NotFound(_))), "{gone:?}");
    }};
}

#[test]
fn a_behavior_table_is_written_through_the_core_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    let orm = sqlite::Repositories::new(&transaction).unwrap();
    cases!(orm, at_once);
    transaction.commit().expect("commit");
}

#[tokio::test]
async fn a_behavior_table_is_written_through_the_core_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_behaviors_rust_{}", std::process::id());
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
    {
        let transaction = client.transaction().await?;
        let orm = postgres::Repositories::new(&transaction)?;
        cases!(orm, awaited);
        transaction.commit().await?;
    }
    // A connection outside a transaction reads, and its write of a table
    // that composes behaviors is refused by name: the core applies its
    // change set in a savepoint.
    let orm = postgres::Repositories::new(&client)?;
    let orders = orm.order()?;
    let lamp: IdentityUUID = "11111111-1111-4111-8111-111111111111".parse()?;
    assert_eq!(members(&orders.get_one(&lamp).await?)["title"], "Lamp");
    let outside = orders
        .create_one(&OrderCreate {
            id: None,
            title: "Shelf".to_owned(),
            placed_at: None,
        })
        .await;
    match outside {
        Err(Error::Invalid(message)) => assert!(message.contains("transaction"), "{message}"),
        other => panic!("a write outside a transaction answered {other:?}"),
    }
    Ok(())
}
