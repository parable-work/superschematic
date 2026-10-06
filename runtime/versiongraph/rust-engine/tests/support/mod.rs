//! What the Postgres tests share: the database they run against, a schema of
//! their own that holds the fixture's DDL, and the fixture's descriptor.

#![allow(dead_code)]

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use superschematic_versiongraph_engine::postgres::{self, TokioPostgres};
use tokio_postgres::{Client, Config, NoTls};

/// Names the Postgres the tests run against.
pub const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL";

/// The schema epoch the fixture's Recipe graph declares.
pub const FIXTURE_SCHEMA_EPOCH: i64 = 1;

/// The snapshot interval the fixture's Recipe graph declares.
pub const FIXTURE_SNAPSHOT_EVERY: usize = 3;

/// The actor of a step that names none: "Cook", a UUID in its canonical
/// form.
pub const DEFAULT_ACTOR: &str = "Cook";

/// `runtime/versiongraph/testdata`.
pub fn testdata() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata")
}

/// The fixture's descriptor.
pub fn descriptor() -> String {
    std::fs::read_to_string(testdata().join("fixture/recipe.json"))
        .expect("read the fixture's descriptor")
}

/// The database the tests run against, or `None`, having said why, when
/// the variable is unset.
pub fn database(test: &str) -> Option<String> {
    match std::env::var(DATABASE_VARIABLE) {
        Ok(dsn) if !dsn.is_empty() => Some(dsn),
        _ => {
            eprintln!("{test}: skipped; set {DATABASE_VARIABLE} to run it against Postgres");
            None
        }
    }
}

/// Connects to `dsn` with `search_path` set to `schema` (and public), and
/// drives the connection on a task of its own.
pub async fn connect(dsn: &str, schema: Option<&str>) -> Client {
    let mut config: Config = dsn.parse().expect("parse the database URL");
    if let Some(schema) = schema {
        config.options(format!("-c search_path={schema},public"));
    }
    let (client, connection) = config.connect(NoTls).await.expect("connect to Postgres");
    tokio::spawn(async move {
        let _ = connection.await;
    });
    client
}

/// A schema of its own that holds the fixture's DDL, dropped with the value.
pub struct Schema {
    pub dsn: String,
    pub name: String,
    /// The server's URL and the name of the database the schema is in, when
    /// the database is the schema's own: the value drops the database.
    database: Option<(String, String)>,
}

/// A name, after `prefix`, that no other schema or database of the tests
/// has.
fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    // Tests of one process run side by side, and a clock may tick in
    // microseconds, so a counter tells their names apart.
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
}

/// The URL `dsn`, a postgres:// URL, with the database `name` in place of
/// its own.
fn database_url(dsn: &str, name: &str) -> String {
    let (scheme, rest) = dsn
        .split_once("://")
        .expect("the database variable is a postgres:// URL");
    let authority = rest.find(['/', '?']).unwrap_or(rest.len());
    let query = rest.find('?').map_or("", |i| &rest[i..]);
    format!("{scheme}://{}/{name}{query}", &rest[..authority])
}

impl Schema {
    /// Creates a schema named after `prefix` and applies the fixture's DDL
    /// to it.
    pub async fn create(dsn: &str, prefix: &str) -> Schema {
        let admin = connect(dsn, None).await;
        // The fixture's DDL creates pgcrypto if it is missing. An extension's
        // name is unique in the database, so create it once in public, where
        // every schema's search path finds it, before tests running side by
        // side each try to create it in their own schema.
        if let Err(error) = admin
            .batch_execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
            .await
        {
            let unique = error.code().is_some_and(|c| c.code() == "23505");
            assert!(unique, "create pgcrypto: {error}");
        }
        let name = unique_name(prefix);
        admin
            .batch_execute(&format!("CREATE SCHEMA {name}"))
            .await
            .expect("create the schema");
        let create_sql = std::fs::read_to_string(testdata().join("fixture/create.sql"))
            .expect("read the fixture's DDL");
        let client = connect(dsn, Some(&name)).await;
        client
            .batch_execute(&create_sql)
            .await
            .expect("apply the fixture's DDL");
        Schema {
            dsn: dsn.to_owned(),
            name,
            database: None,
        }
    }

    /// Creates a database of its own on the server `dsn` names, and in it a
    /// schema as [`Schema::create`] does. The value drops the database.
    ///
    /// The graph's sweep lock is an advisory lock, and Postgres keys an
    /// advisory lock to the database, not to a schema. The tests of one
    /// binary run side by side, so in a database they share, one test's
    /// held lock makes another's sweep skip, and one test's sweep makes
    /// another's take of the lock fail. A test that holds or takes the lock
    /// while another test of its binary does runs in a database of its own.
    pub async fn create_in_own_database(dsn: &str, prefix: &str) -> Schema {
        let database = unique_name(prefix);
        connect(dsn, None)
            .await
            .batch_execute(&format!("CREATE DATABASE {database}"))
            .await
            .expect("create the database");
        let mut schema = Schema::create(&database_url(dsn, &database), prefix).await;
        schema.database = Some((dsn.to_owned(), database));
        schema
    }

    /// A new connection whose search path is the schema.
    pub async fn connect(&self) -> Client {
        connect(&self.dsn, Some(&self.name)).await
    }

    /// The fixture's graph over a new connection to the schema.
    pub async fn storage(
        &self,
        adapter: &Arc<postgres::Adapter>,
    ) -> Arc<postgres::PostgresStorage<TokioPostgres>> {
        Arc::new(adapter.storage(TokioPostgres::new(self.connect().await)))
    }
}

/// Drops the schema and everything in it, or the schema's own database,
/// when the test ends and when it panics. It runs on a thread of its own,
/// since a destructor cannot await, and gives up on a lock it waits on past
/// a few seconds rather than hang. A database is dropped with the
/// connections to it still open.
impl Drop for Schema {
    fn drop(&mut self) {
        let (dsn, statement) = match &self.database {
            Some((server, database)) => (
                server.clone(),
                format!("DROP DATABASE IF EXISTS {database} WITH (FORCE)"),
            ),
            None => (
                self.dsn.clone(),
                format!(
                    "SET lock_timeout = '5s'; DROP SCHEMA IF EXISTS {} CASCADE",
                    self.name
                ),
            ),
        };
        let dropped = std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("a runtime");
            runtime.block_on(async {
                let admin = connect(&dsn, None).await;
                let _ = admin.batch_execute(&statement).await;
            });
        })
        .join();
        if dropped.is_err() {
            eprintln!("could not drop schema {}", self.name);
        }
    }
}

/// The fixture's adapter.
pub fn adapter() -> Arc<postgres::Adapter> {
    Arc::new(
        postgres::Adapter::new(&descriptor(), postgres::Options::default())
            .expect("the fixture's adapter"),
    )
}

/// The hyphenated text Postgres reads of a UUID in its canonical form.
pub fn hyphenated(id: &str) -> String {
    superschematic_versiongraph_engine::canonical::uuid_hyphenated(id).expect("a UUID")
}
