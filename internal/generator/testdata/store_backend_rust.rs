//! The database a test of the notifier, the inbound path or the agent
//! dispatcher runs on (#618), as tests/backend/mod.rs: RUST_E2E_STORE
//! names it. `sqlite` is a file of the test's own with the build's SQLite
//! DDL (create.sqlite.sql) applied, opened as the types crate's
//! orm::sqlite::tables; `postgres` is a schema of the test's own at the
//! URL RUST_E2E_DATABASE_URL names, with create.sql applied, opened as
//! orm::postgres::tables. Either is a runtime::orm::store::Store, which the
//! parts run over.

#![allow(dead_code)]

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
use runtime::orm::sqlite::{rusqlite, Database};
use runtime::orm::store::{PoolTransaction, Store, TxRepositories};
use runtime::orm::{FindOptions, OrderBy, Values};
use serde_json::Value;

/// Which database the test runs on.
pub fn store_name() -> String {
    std::env::var("RUST_E2E_STORE").unwrap_or_else(|_| "sqlite".to_owned())
}

pub fn on_sqlite() -> bool {
    store_name() == "sqlite"
}

/// A line the Go test compares between the two databases' runs.
pub fn answer(case: &str, value: Value) {
    println!("answer: {case}: {value}");
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

/// The database of one case: a SQLite file or a Postgres schema.
pub enum Backend {
    Sqlite {
        database: Arc<Database>,
        dir: std::path::PathBuf,
    },
    Postgres {
        pool: deadpool_postgres::Pool,
        admin: tokio_postgres::Client,
        schema: String,
    },
}

impl Backend {
    /// A database of the case's own, `prefix` naming its file or schema.
    pub async fn open(prefix: &str, sqlite_ddl: &str, postgres_ddl: &str) -> Self {
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        if on_sqlite() {
            let dir = std::env::temp_dir().join(format!("{prefix}-{nanos}"));
            std::fs::create_dir_all(&dir).unwrap();
            let path = dir.join("database.db");
            let connection = rusqlite::Connection::open(&path).unwrap();
            connection.execute_batch(sqlite_ddl).unwrap();
            drop(connection);
            return Backend::Sqlite {
                database: Arc::new(Database::open(&path).unwrap()),
                dir,
            };
        }
        let url = std::env::var("RUST_E2E_DATABASE_URL").expect("RUST_E2E_DATABASE_URL names the database");
        let config: tokio_postgres::Config = url.parse().unwrap();
        let admin = connect(&config).await;
        let schema = format!("{prefix}_{nanos}").replace('-', "_");
        admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
        let mut scoped = config;
        scoped.options(format!("-c search_path={schema},public"));
        let client = connect(&scoped).await;
        client.batch_execute(postgres_ddl).await.unwrap();
        let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
        let pool = deadpool_postgres::Pool::builder(manager).max_size(16).build().unwrap();
        Backend::Postgres { pool, admin, schema }
    }

    /// The database's tables as the parts take them.
    pub fn store(&self) -> Store {
        match self {
            Backend::Sqlite { database, .. } => types::orm::sqlite::tables(Arc::clone(database)).into(),
            Backend::Postgres { pool, .. } => types::orm::postgres::tables(pool.clone()).into(),
        }
    }

    /// Runs a statement of no rows: `postgres` on Postgres, its `$n`
    /// bound to `args`, and `sqlite` on SQLite, its `?n` bound to them.
    pub async fn execute(&self, postgres: &str, sqlite: &str, args: &[&str]) -> u64 {
        match self {
            Backend::Sqlite { database, .. } => database
                .write(|connection| {
                    connection
                        .execute(sqlite, rusqlite::params_from_iter(args.iter()))
                        .map_err(|err| runtime::orm::sqlite::failure(sqlite, &err))
                })
                .unwrap() as u64,
            Backend::Postgres { pool, .. } => {
                let client = pool.get().await.unwrap();
                let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> =
                    args.iter().map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync)).collect();
                client.execute(postgres, &params).await.unwrap()
            }
        }
    }

    /// Reads the first column of each row as text: `postgres` on Postgres
    /// and `sqlite` on SQLite.
    pub async fn texts(&self, postgres: &str, sqlite: &str) -> Vec<String> {
        match self {
            Backend::Sqlite { database, .. } => database
                .read(|connection| {
                    let mut statement = connection
                        .prepare(sqlite)
                        .map_err(|err| runtime::orm::sqlite::failure(sqlite, &err))?;
                    let rows = statement
                        .query_map([], |row| row.get::<_, String>(0))
                        .map_err(|err| runtime::orm::sqlite::failure(sqlite, &err))?;
                    Ok::<_, runtime::orm::Error>(rows.filter_map(Result::ok).collect())
                })
                .unwrap(),
            Backend::Postgres { pool, .. } => {
                let client = pool.get().await.unwrap();
                client
                    .query(postgres, &[])
                    .await
                    .unwrap()
                    .iter()
                    .map(|row| row.get::<_, String>(0))
                    .collect()
            }
        }
    }

    /// Drops the case's database.
    pub async fn drop_database(self) {
        match self {
            Backend::Sqlite { database, dir } => {
                drop(database);
                let _ = std::fs::remove_dir_all(dir);
            }
            Backend::Postgres { pool, admin, schema } => {
                pool.close();
                admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
            }
        }
    }
}

/// Runs `work` over the system's repositories in a transaction of
/// `store`'s it commits.
pub async fn in_tx<T>(store: &Store, work: impl AsyncFnOnce(&TxRepositories<'_, '_>) -> T) -> T {
    let mut client = store.client().await.unwrap();
    let tx = client.transaction().await.unwrap();
    let repos = tx.db().repositories(store.definition().unwrap()).unwrap().as_system().with_rules_of(store);
    let out = work(&repos).await;
    drop(repos);
    tx.commit().await.unwrap();
    out
}

/// The rows of `table` that `filter` finds, read as the system, in the
/// order of `order`.
pub async fn rows_of(store: &Store, table: &str, filter: Value, order: Option<&str>) -> Vec<Values> {
    let client = store.client().await.unwrap();
    let repos = client.db().repositories(store.definition().unwrap()).unwrap().as_system();
    let options = FindOptions {
        order_by: order.map(OrderBy::asc).into_iter().collect(),
        limit: Some(1000),
        ..FindOptions::default()
    };
    let filter = filter.as_object().cloned().unwrap();
    repos.table(table).unwrap().find_many(Some(&filter), &options).await.unwrap().rows
}

/// A transaction of `store`'s, for a caller that writes in it itself.
pub type Tx<'a, 't> = TxRepositories<'a, 't>;

/// The transaction type the parts' callbacks are given.
pub type PgTx<'t> = PoolTransaction<'t>;
