//! A reencrypt of fixture-vault-db's PlaidItem (D85, D116): after a
//! rotation puts a new key first, it reseals every envelope the old key
//! sealed, a batch of rows at a time, deleted rows among them, and answers
//! how many rows it resealed, as the Go ORM's Reencrypt and the TypeScript
//! repositories' reencrypt do; the rows then open under the new key alone.
//! The same cases run on a SQLite file and, with
//! SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, on Postgres, through `$run`.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use serde_json::{json, Value};

/// The parity vectors' keys (runtime/schema/testdata/fieldcrypt_parity.json).
const OLD: &str = "ZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5/gIGCg4Q=";
const NEW: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=";
const USER: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";

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

fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// The cases every dialect's repositories are held to, over the
/// repositories `$open` opens.
macro_rules! cases {
    ($open:expr, $run:ident) => {{
        let old = FieldKeyring::parse(OLD).expect("the old keyring");
        let rotated = FieldKeyring::parse(&format!("{NEW},{OLD}")).expect("the rotated keyring");
        let new = FieldKeyring::parse(NEW).expect("the new keyring");

        // Three items under the old key, one with both of its encrypted
        // fields, and one deleted.
        let under_old = $open.acting(Some(USER.to_owned())).with_keys(&old);
        let items = under_old.plaid_item().unwrap();
        let mut keys = Vec::new();
        for (n, secret) in [(1, None), (2, Some("whsec-2")), (3, None)] {
            let mut input = json!({
                "itemId": format!("item-{n}"),
                "institution": "First Bank",
                "accessToken": format!("access-sandbox-{n}"),
            });
            if let Some(secret) = secret {
                input["webhookSecret"] = json!(secret);
            }
            let row = $run!(items.untyped().create_one(&values(input))).expect("create an item");
            keys.push(row["id"].clone());
        }
        $run!(items.untyped().delete_one(&keys[2], &WriteOptions::default()))
            .expect("delete the third item");

        // Before the reseal, the new key alone opens none of them.
        let under_new = $open.with_keys(&new);
        let read_new = under_new.plaid_item().unwrap();
        let unopened = $run!(read_new.untyped().get_one(&keys[0]));
        assert!(matches!(unopened, Err(Error::Encryption(_))), "{unopened:?}");

        // A reencrypt under the rotated keyring reseals every row the old
        // key sealed, two at a time, the deleted one among them, and a
        // second finds none.
        let under_rotated = $open.with_keys(&rotated);
        let rotating = under_rotated.plaid_item().unwrap();
        assert_eq!($run!(rotating.reencrypt(Some(2))).expect("reencrypt"), 3);
        assert_eq!($run!(rotating.reencrypt(None)).expect("reencrypt again"), 0);

        // Each row opens under the new key alone, with what it held.
        for (n, key) in keys.iter().enumerate() {
            let found = $run!(read_new.untyped().find_one(
                Some(&values(json!({"id": {"eq": key}}))),
                &FindOptions {
                    include_deleted: true,
                    ..Default::default()
                },
            ))
            .expect("read an item under the new key")
            .expect("the item");
            assert_eq!(found["accessToken"], format!("access-sandbox-{}", n + 1));
            let secret = if n == 1 { json!("whsec-2") } else { Value::Null };
            assert_eq!(found["webhookSecret"], secret);
        }
        let stale = $run!(items.untyped().get_one(&keys[0]));
        assert!(matches!(stale, Err(Error::Encryption(_))), "{stale:?}");

        // Repositories opened with no keys have nothing to reseal with.
        let keyless = $open;
        let refused = $run!(keyless.plaid_item().unwrap().reencrypt(None));
        assert!(matches!(refused, Err(Error::Encryption(_))), "{refused:?}");
    }};
}

#[test]
fn a_reencrypt_reseals_every_older_envelope_on_sqlite() {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    cases!(db::orm::sqlite::Repositories::new(&connection).unwrap(), at_once);
}

#[tokio::test]
async fn a_reencrypt_reseals_every_older_envelope_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_vault_reencrypt_rust_{}", std::process::id());
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
    let client = pool.get().await?;
    client
        .batch_execute(include_str!("../postgres/create.sql"))
        .await?;
    cases!(db::orm::postgres::Repositories::new(&client).unwrap(), awaited);
    Ok(())
}
