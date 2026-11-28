//! The generated wiring of fixture-audience-queue-api's Rust crate on
//! Postgres (D156, #734): `Wired::run_audience_resolving`, the worker a
//! generated Rust worker runs for the pages' @audience queue, claims the
//! queue in the runtime's claim loop and resolves a folder's pages after
//! its change of audience commits. Until it runs, a page keeps the
//! audience it had; once it has, a page read shows the folder's new one.

use std::sync::Arc;
use std::time::{Duration, Instant};

use api::database_types::orm::postgres::{deadpool_postgres, tokio_postgres, Repositories};
use api::database_types::orm::{FindOptions, Values, WriteOptions};
use api::runtime::orm::AccessCaller;
use api::runtime::worker::Options;
use api::runtime::{PrincipalKind, Shutdown};
use api::{Wire, Wired, WiringDeps};
use serde_json::{json, Value};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e02";

/// How many pages the folder holds: more than one batch of
/// resolve_audience's.
const PAGES: i64 = 1234;

/// The implementation's Wire, which keeps every part as built.
struct Wiring;

impl Wire for Wiring {}

fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

fn user(subject: &str) -> AccessCaller {
    AccessCaller {
        subject: Some(subject.to_owned()),
        kind: PrincipalKind::User,
        ..AccessCaller::default()
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_generated_worker_resolves_a_folders_pages_through_its_queue() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        println!("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is unset: skipped");
        return;
    };
    let create_sql = std::env::var("AUDIENCE_CREATE_SQL").expect("AUDIENCE_CREATE_SQL names the build's create.sql");
    let schema = format!("audience_wiring_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config.connect(tokio_postgres::NoTls).await.expect("connect");
    tokio::spawn(connection);
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(4).build().expect("the pool");
    let ran = run(&pool, &std::fs::read_to_string(create_sql).expect("the build's create.sql")).await;
    drop(pool);
    admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.expect("drop the schema");
    ran.expect("the generated audience worker on Postgres");
    println!("the generated audience worker ran on Postgres");
}

async fn run(pool: &deadpool_postgres::Pool, create_sql: &str) -> Result<(), Box<dyn std::error::Error>> {
    let client = pool.get().await?;
    client.batch_execute(create_sql).await?;
    let client = &client;
    let as_user = |who: &str| {
        Repositories::new(client)
            .unwrap()
            .acting(Some(ALICE.to_owned()))
            .with_access(user(who))
    };
    // A folder of alice's, everyone's, holding PAGES pages that inherit its
    // audience, written in one statement.
    let folder = as_user(ALICE)
        .folder()?
        .untyped()
        .create_one(&values(json!({"name": "a large folder", "owner": ALICE, "audience": "everyone"})))
        .await?;
    let key: String = client
        .query_one("SELECT id::text FROM folder ORDER BY created_at DESC LIMIT 1", &[])
        .await?
        .get(0);
    client
        .execute(
            "INSERT INTO page (folder_id, title, audience, created_at, created_by) SELECT $1::text::uuid, 'page ' || n, 'inherit', now(), $2::text::uuid FROM generate_series(1, $3::bigint) AS n",
            &[&key, &ALICE, &PAGES],
        )
        .await?;
    let folder = folder["id"].clone();
    let visible = |who: &'static str| {
        let folder = folder.clone();
        async move {
            let filter = values(json!({"folderId": {"eq": folder}}));
            let page = as_user(who)
                .page()?
                .untyped()
                .find_many(Some(&filter), &FindOptions { limit: Some(1), ..FindOptions::default() })
                .await?;
            Ok::<_, Box<dyn std::error::Error>>(page.total.unwrap_or(u64::MAX))
        }
    };
    let set_audience = |audience: &'static str| {
        let folder = folder.clone();
        async move {
            as_user(ALICE)
                .folder()?
                .untyped()
                .update_one(&folder, &values(json!({"audience": audience})), &WriteOptions::default())
                .await?;
            Ok::<_, Box<dyn std::error::Error>>(())
        }
    };
    let until = async |what: &str, want: u64, who: &'static str| -> Result<(), Box<dyn std::error::Error>> {
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            let seen = visible(who).await?;
            if seen == want {
                return Ok(());
            }
            if Instant::now() > deadline {
                return Err(format!("{what}: {who} sees {seen} pages, want {want}").into());
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    };

    println!("case: a folder's change of audience commits without its pages, which keep theirs until the worker runs");
    assert_eq!(visible(BOB).await?, PAGES as u64);
    set_audience("private").await?;
    assert_eq!(visible(BOB).await?, PAGES as u64, "before the worker runs bob sees the pages he saw");

    println!("case: the generated worker claims the folder's message and resolves its pages");
    // The API's config, from the environment as an entrypoint reads it;
    // the wiring is built over the test's pool.
    let config = api::config::load_env_config().map_err(|err| err.to_string())?;
    let deps = WiringDeps { db: pool.clone(), config };
    let wired = Wired::new(&deps, Arc::new(Wiring)).map_err(|err| err.to_string())?;
    let shutdown = Shutdown::new();
    let worker = {
        let (wired, shutdown) = (wired.clone(), shutdown.clone());
        tokio::spawn(async move { wired.run_audience_resolving(Options::default(), shutdown).await })
    };
    until("the worker resolving the folder made private", 0, BOB).await?;
    assert_eq!(visible(ALICE).await?, PAGES as u64);

    println!("case: a folder made everyone's again reaches its pages through the queue");
    set_audience("everyone").await?;
    until("the worker resolving the folder made everyone's", PAGES as u64, BOB).await?;
    shutdown.cancel();
    worker.await?.map_err(|err| err.to_string())?;
    let left: i64 = client
        .query_one("SELECT count(*) FROM audience_resolving_queue WHERE state = 'ready'", &[])
        .await?
        .get(0);
    assert_eq!(left, 0, "no message is left ready");
    Ok(())
}
