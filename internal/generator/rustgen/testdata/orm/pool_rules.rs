//! The typed operations of fixture-pool-db's Account (D103, D104, D116): a
//! Pool's pick runs on the table as a whole, a command without a row, and
//! its Budget's reserve on a row, each through the behaviors' core, on a
//! SQLite file and, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, on
//! Postgres, through `$run`.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};

const OWNER: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";

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

/// The cases every dialect's repositories are held to, over `$orm`.
macro_rules! cases {
    ($orm:expr, $run:ident) => {{
        let orm = $orm.acting(Some(OWNER.to_owned())).with_caller(Caller {
            subject: OWNER.to_owned(),
            permissions: vec!["accounts.pool".to_owned(), "accounts.observe".to_owned()],
            stands_in: false,
        });
        let accounts = orm.account().unwrap();
        let account = |name: &str, rank: i64| {
            values(json!({
                "owner": OWNER,
                "name": name,
                "vendor": "acme",
                "kind": "account",
                "rank": rank,
            }))
        };
        let first = $run!(accounts.untyped().create_one(&account("first", 1)))
            .expect("create the first account");
        let second = $run!(accounts.untyped().create_one(&account("second", 2)))
            .expect("create the second account");

        // A pick runs on the table as a whole: the open account lowest by
        // rank, and with it left out, the next.
        let pool = orm.account_pool().unwrap();
        let mine = AccountPoolMatch {
            kind: None,
            name: None,
            owner: Some(OWNER.to_owned()),
            rank: None,
            vendor: None,
        };
        let picked = $run!(pool.pick(&AccountPoolPickParams {
            exclude: None,
            r#match: Some(mine.clone()),
            prefer: None,
        }))
        .expect("pick an account");
        let member = picked.member.expect("a member");
        assert_eq!(Value::String(member.key.clone()), first["id"], "{member:?}");
        let next = $run!(pool.pick(&AccountPoolPickParams {
            exclude: Some(vec![member.key]),
            r#match: Some(mine),
            prefer: None,
        }))
        .expect("pick another account");
        assert_eq!(
            Value::String(next.member.expect("a member").key),
            second["id"]
        );

        // A row's operation of another use, typed: the Budget's reserve of
        // tokens on the first account, and one past its limit refused.
        let budget = orm.account_budget().unwrap();
        let key: IdentityUUID = serde_json::from_value(first["id"].clone()).unwrap();
        let tokens = |amount: i64| AccountBudgetReserveParams {
            amount: Some(amount),
            meter: Some(AccountBudgetMeter::Tokens),
        };
        let reserved = $run!(budget.reserve(&key, &tokens(400), &WriteOptions::default()))
            .expect("reserve tokens");
        assert_eq!(reserved.reserved.get("tokens"), Some(&400), "{reserved:?}");
        let over = $run!(budget.reserve(&key, &tokens(700), &WriteOptions::default()));
        assert!(matches!(over, Err(Error::Refused(_))), "{over:?}");
    }};
}

#[test]
fn a_table_operation_runs_through_the_core_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    cases!(sqlite::Repositories::new(&transaction).unwrap(), at_once);
    transaction.commit().expect("commit");
}

#[tokio::test]
async fn a_table_operation_runs_through_the_core_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_pool_rules_rust_{}", std::process::id());
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
