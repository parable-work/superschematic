//! The HTTP runtime's deriver loop (D146) over the repositories of
//! fixture-derived-provenance-db, whose tables Ticket and Article implement
//! Derived, on Postgres, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set:
//! each call of the loop opens the repositories as the deriver in a
//! transaction of the pool, as a Rust deriver's generated main.rs does, and
//! runs a Derived operation of the table as a whole through the core.

use std::time::Duration;

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::*;
use serde_json::{json, Value};
use superschematic_http_runtime::deriver::{
    self, DerivationAnswer, DerivationItem, DeriverOptions,
};

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// The deriver's caller, holding the Derived configs' permissions, or
/// another who holds none.
fn caller(subject: &str, permissions: &[&str]) -> Caller {
    Caller {
        subject: subject.to_owned(),
        permissions: permissions.iter().map(|p| (*p).to_owned()).collect(),
        stands_in: false,
        ..Default::default()
    }
}

/// Runs a Derived operation of `table` as a whole through the repositories
/// opened as `caller`, in a transaction of the pool.
async fn derived_operation(
    pool: &deadpool_postgres::Pool,
    caller: Caller,
    table: &str,
    operation: &str,
    params: &Value,
) -> Result<Value, Error> {
    let mut client = pool
        .get()
        .await
        .map_err(|err| Error::Database(err.to_string()))?;
    let transaction = client
        .transaction()
        .await
        .map_err(|err| Error::Database(err.to_string()))?;
    let repositories = postgres::Repositories::new(&transaction)?.with_caller(caller);
    let result: Value = match table {
        "Ticket" => {
            repositories
                .ticket()?
                .table_operation("derived", operation, params)
                .await?
        }
        "Article" => {
            repositories
                .article()?
                .table_operation("derived", operation, params)
                .await?
        }
        other => return Err(Error::Invalid(format!("no table {other}"))),
    };
    drop(repositories);
    transaction
        .commit()
        .await
        .map_err(|err| Error::Database(err.to_string()))?;
    Ok(result)
}

/// Creates a row of `table` through the core, and answers its key.
async fn create(pool: &deadpool_postgres::Pool, table: &str, row: Value) -> String {
    let mut client = pool.get().await.expect("a connection");
    let transaction = client.transaction().await.expect("begin");
    let repositories = postgres::Repositories::new(&transaction).unwrap();
    let created = match table {
        "Ticket" => {
            repositories
                .ticket()
                .unwrap()
                .untyped()
                .create_one(&values(row))
                .await
        }
        _ => {
            repositories
                .article()
                .unwrap()
                .untyped()
                .create_one(&values(row))
                .await
        }
    }
    .expect("create the row");
    drop(repositories);
    transaction.commit().await.expect("commit");
    created["id"].as_str().expect("a key").to_owned()
}

/// A field of a row, read through the repositories, or none when it holds
/// no value.
async fn column(
    pool: &deadpool_postgres::Pool,
    table: &str,
    field: &str,
    id: &str,
) -> Option<String> {
    let client = pool.get().await.expect("a connection");
    let repositories = postgres::Repositories::new(&client).unwrap();
    let row = match table {
        "ticket" => {
            repositories
                .ticket()
                .unwrap()
                .untyped()
                .get_one(&json!(id))
                .await
        }
        _ => {
            repositories
                .article()
                .unwrap()
                .untyped()
                .get_one(&json!(id))
                .await
        }
    }
    .expect("read the row");
    row.get(field).and_then(Value::as_str).map(str::to_owned)
}

/// Runs the loop as `who` over both tables until `done`, then stops it,
/// and answers what it returned.
async fn run_until<A, AF, D, DF>(
    pool: &deadpool_postgres::Pool,
    who: Caller,
    answer: A,
    done: D,
) -> Result<(), deriver::DeriverFailure>
where
    A: Fn(String, DerivationItem) -> AF + Send + Sync + 'static,
    AF: std::future::Future<Output = DerivationAnswer> + Send + 'static,
    D: Fn() -> DF,
    DF: std::future::Future<Output = bool>,
{
    let calls = pool.clone();
    let call = move |table: String, operation: &'static str, params: Value| {
        let (pool, who) = (calls.clone(), who.clone());
        async move { derived_operation(&pool, who, &table, operation, &params).await }
    };
    let options = DeriverOptions {
        idle: Duration::from_millis(50),
        ..DeriverOptions::default()
    };
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let running = tokio::spawn(deriver::run_deriver(
        vec!["Ticket".into(), "Article".into()],
        call,
        answer,
        options,
        async {
            let _ = stopped.await;
        },
    ));
    for _ in 0..1500 {
        if running.is_finished() || done().await {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let _ = stop.send(());
    running.await.expect("the loop did not panic")
}

#[tokio::test]
async fn the_deriver_loop_fills_the_tables_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_derived_rules_rust_{}", std::process::id());
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
        .max_size(4)
        .build()
        .expect("the pool");
    pool.get()
        .await
        .expect("a connection")
        .batch_execute(include_str!("../postgres/create.sql"))
        .await
        .expect("create the tables");
    let deriving = caller(
        "derived-rs-api-fill-all",
        &["articles.derive", "tickets.derive"],
    );

    // A deriver's loop hands each stale field to its answer and settles
    // the answers as own fields.
    let ticket = create(
        &pool,
        "Ticket",
        json!({ "subject": "I was charged twice", "body": "Please refund it." }),
    )
    .await;
    let article = create(
        &pool,
        "Article",
        json!({ "title": "Refunds", "body": "A refund takes five days." }),
    )
    .await;
    let answer = |table: String, item: DerivationItem| async move {
        let sure = Some(0.95);
        match (table.as_str(), item.field.as_str()) {
            ("Ticket", "category") => {
                assert_eq!(
                    item.inputs["subject"],
                    json!("I was charged twice"),
                    "{item:?}"
                );
                DerivationAnswer::Value {
                    value: json!("billing"),
                    confidence: sure,
                    usage: None,
                }
            }
            ("Ticket", "priority") => DerivationAnswer::Value {
                value: json!("high"),
                confidence: sure,
                usage: None,
            },
            _ => DerivationAnswer::value("Refunds take five days."),
        }
    };
    let settled = || async {
        column(&pool, "ticket", "category", &ticket)
            .await
            .as_deref()
            == Some("billing")
            && column(&pool, "ticket", "priority", &ticket)
                .await
                .as_deref()
                == Some("high")
            && column(&pool, "article", "summary", &article)
                .await
                .as_deref()
                == Some("Refunds take five days.")
    };
    run_until(&pool, deriving.clone(), answer, settled)
        .await
        .expect("the loop");
    assert!(settled().await, "the derived fields did not settle");
    let stale = derived_operation(
        &pool,
        deriving.clone(),
        "Ticket",
        "staleDerivations",
        &json!({}),
    )
    .await
    .expect("pull");
    assert!(
        !stale["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|item| item["id"] == json!(ticket)),
        "the ticket is stale after it settled: {stale}"
    );

    // A value the field's enum refuses counts an attempt and writes no own
    // field.
    let refused = create(
        &pool,
        "Ticket",
        json!({ "subject": "The page is blank", "body": "Nothing loads." }),
    )
    .await;
    let answer = |table: String, item: DerivationItem| async move {
        match (table.as_str(), item.field.as_str()) {
            ("Ticket", "category") => DerivationAnswer::value("weather"),
            ("Ticket", _) => DerivationAnswer::failure("refused", "the model refused"),
            _ => DerivationAnswer::value("A summary."),
        }
    };
    let failed = || async {
        let page = derived_operation(
            &pool,
            deriving.clone(),
            "Ticket",
            "failedDerivations",
            &json!({}),
        )
        .await
        .expect("failed derivations");
        page["items"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|item| item["id"] == json!(refused))
            .count()
            == 2
    };
    run_until(&pool, deriving.clone(), answer, failed)
        .await
        .expect("the loop");
    assert!(failed().await, "the refused ticket's fields did not fail");
    assert_eq!(column(&pool, "ticket", "category", &refused).await, None);
    assert_eq!(column(&pool, "ticket", "priority", &refused).await, None);

    // A caller without the config's permission ends the loop with the
    // table named.
    create(
        &pool,
        "Ticket",
        json!({ "subject": "Another", "body": "One more." }),
    )
    .await;
    let answer = |_: String, _: DerivationItem| async { DerivationAnswer::value("x") };
    let failure = run_until(&pool, caller("nobody", &[]), answer, || async { false })
        .await
        .expect_err("a caller without the permission");
    assert_eq!(failure.table, "Ticket");
    assert!(
        matches!(&failure.error, Error::Refused(refusal) if refusal.code == "forbidden"),
        "{failure}"
    );

    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
    println!("the repositories ran on Postgres");
}
