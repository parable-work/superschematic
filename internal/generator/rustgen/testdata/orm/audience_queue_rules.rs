//! fixture-audience-queue-db's pages, which inherit their folder's
//! audience through a queue (D156, #734), on Postgres: a folder's change
//! of audience commits without its pages and enqueues the folder once, a
//! page keeps the audience it had until the queue's worker runs
//! `resolve_audience` for the message, and a page read right after the
//! worker drains the queue shows the folder's new audience.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::*;
use serde_json::{json, Value};
use superschematic_http_runtime::orm::{resolve_audience, AccessCaller, AudienceResolving};
use superschematic_http_runtime::PrincipalKind;

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e02";

/// How many pages a folder holds: more than one batch of resolve_audience's.
const PAGES: i64 = 1234;

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

#[tokio::test]
async fn an_audience_resolves_through_its_queue_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_audience_queue_rust_{}", std::process::id());
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
    let client = &client;
    let definition = postgres_definition()?;
    let open = || postgres::Repositories::new(client).unwrap().acting(Some(ALICE.to_owned()));
    let as_user = |who: &str| open().with_access(user(who));

    // A folder of alice's, everyone's, holding PAGES pages that inherit its
    // audience, written in one statement.
    let folder_of_pages = || async {
        let folder = as_user(ALICE)
            .folder()?
            .untyped()
            .create_one(&values(json!({"name": "a large folder", "owner": ALICE, "audience": "everyone"})))
            .await?;
        let text: String = client
            .query_one("SELECT id::text FROM folder ORDER BY created_at DESC LIMIT 1", &[])
            .await?
            .get(0);
        client
            .execute(
                "INSERT INTO page (folder_id, title, audience, created_at, created_by) SELECT $1::text::uuid, 'page ' || n, 'inherit', now(), $2::text::uuid FROM generate_series(1, $3::bigint) AS n",
                &[&text, &ALICE, &PAGES],
            )
            .await?;
        Ok::<_, Box<dyn std::error::Error>>((folder["id"].clone(), text))
    };
    let visible = |who: &'static str, folder: Value| async move {
        let filter = values(json!({"folderId": {"eq": folder}}));
        let page = as_user(who)
            .page()?
            .untyped()
            .find_many(Some(&filter), &FindOptions { limit: Some(1), ..FindOptions::default() })
            .await?;
        Ok::<_, Box<dyn std::error::Error>>(page.total.unwrap_or(u64::MAX))
    };
    let set_audience = |folder: Value, audience: &'static str| async move {
        as_user(ALICE)
            .folder()?
            .untyped()
            .update_one(&folder, &values(json!({"audience": audience})), &WriteOptions::default())
            .await?;
        Ok::<_, Box<dyn std::error::Error>>(())
    };
    let messages = |text: String| async move {
        let rows = client
            .query("SELECT child_table FROM audience_resolving_queue WHERE parent_key = $1", &[&text])
            .await?;
        Ok::<_, Box<dyn std::error::Error>>(rows.iter().map(|row| row.get::<_, String>(0)).collect::<Vec<_>>())
    };
    // What a worker of the queue does: each ready message resolved, then
    // deleted.
    let drain = || async {
        let claimed = client
            .query("SELECT id::text, child_table, parent_key FROM audience_resolving_queue WHERE state = 'ready'", &[])
            .await?;
        let mut resolved = 0;
        for row in claimed {
            let message = AudienceResolving { child_table: row.get(1), parent_key: row.get(2) };
            resolved += resolve_audience(client, definition, &message).await?;
            let id: String = row.get(0);
            client
                .execute("DELETE FROM audience_resolving_queue WHERE id = $1::text::uuid", &[&id])
                .await?;
        }
        Ok::<_, Box<dyn std::error::Error>>(resolved)
    };

    println!("case: a folder's change of audience commits without its pages, enqueued once on the folder's queue");
    let (folder, text) = folder_of_pages().await?;
    assert_eq!(visible(BOB, folder.clone()).await?, PAGES as u64);
    set_audience(folder.clone(), "private").await?;
    set_audience(folder.clone(), "everyone").await?;
    set_audience(folder.clone(), "private").await?;
    assert_eq!(visible(BOB, folder.clone()).await?, PAGES as u64, "before the worker runs bob sees the pages he saw");
    assert_eq!(messages(text.clone()).await?, vec!["Page".to_owned()]);
    drain().await?;

    println!("case: a page read right after the worker drains the queue shows the folder's new audience");
    let (folder, text) = folder_of_pages().await?;
    set_audience(folder.clone(), "private").await?;
    assert_eq!(drain().await?, PAGES as u64);
    assert!(messages(text).await?.is_empty());
    assert_eq!(visible(BOB, folder.clone()).await?, 0);
    assert_eq!(visible(ALICE, folder.clone()).await?, PAGES as u64);
    set_audience(folder.clone(), "everyone").await?;
    assert_eq!(visible(BOB, folder.clone()).await?, 0, "before the worker runs bob sees none");
    drain().await?;
    assert_eq!(visible(BOB, folder).await?, PAGES as u64);
    Ok(())
}
