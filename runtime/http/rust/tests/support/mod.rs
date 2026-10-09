//! What the identity store and route tests share: the fixture's descriptor
//! and DDL (`runtime/http/testdata/identity`), a scalar catalog standing in
//! for superscalar's, and the databases a test runs on: SQLite always, and
//! the Postgres `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL` names when it is
//! set and the `identity-postgres` feature is on.

// Each test target uses part of this module.
#![allow(dead_code)]

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use serde_json::Value;
use superschematic_http_runtime::identity::{
    parse_uuid, uuid_to_base62, Client, Rusqlite, Scalars, SqlStore,
};

/// The Postgres the tests also run on.
pub const POSTGRES_URL_ENV: &str = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL";

pub fn fixture(name: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../testdata/identity")
        .join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()))
}

/// The fixture's descriptor, edited by `edit`. A descriptor without
/// `user.nameScalar` or `role.keyScalar`, which the fixture on a branch
/// before them lacks, gets the fixture schema's: `Identity.Name` and
/// `Identity.UUID`.
pub fn descriptor(edit: impl FnOnce(&mut Value)) -> String {
    let mut d: Value = serde_json::from_str(&fixture("fixture-user-model-db.json")).unwrap();
    if let Some(user) = d["user"].as_object_mut() {
        user.entry("nameScalar")
            .or_insert_with(|| Value::from("Identity.Name"));
    }
    if let Some(role) = d.get_mut("role").and_then(Value::as_object_mut) {
        role.entry("keyScalar")
            .or_insert_with(|| Value::from("Identity.UUID"));
    }
    edit(&mut d);
    d.to_string()
}

/// The scalars the fixture's tables use, with superscalar's rules:
/// `Contact.Email` trims, lowercases and checks its pattern;
/// `Identity.Name` is 2 to 80 characters; `Identity.UUID` is base62 or
/// hyphenated, written base62. Any other scalar is unknown.
pub struct Catalog;

impl Scalars for Catalog {
    fn parse(&self, scalar: &str, value: &str) -> Option<Result<String, String>> {
        match scalar {
            "Contact.Email" => Some(email(value)),
            "Identity.Name" => {
                let length = value.chars().count();
                Some(if length < 2 {
                    Err(format!("length {length} below minimum 2"))
                } else if length > 80 {
                    Err(format!("length {length} above maximum 80"))
                } else {
                    Ok(value.to_owned())
                })
            }
            "Identity.UUID" => Some(
                parse_uuid(value)
                    .map(uuid_to_base62)
                    .ok_or_else(|| format!("invalid UUID format: {value}")),
            ),
            _ => None,
        }
    }
}

/// superscalar's `Contact.Email`: trimmed and lowercased, then
/// `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`.
fn email(input: &str) -> Result<String, String> {
    let normalized = input.trim().to_lowercase();
    if normalized.is_empty() {
        return Err("email must not be empty".to_owned());
    }
    let refused = || Err(format!("invalid email format: {input}"));
    let Some((local, domain)) = normalized.split_once('@') else {
        return refused();
    };
    let local_ok = !local.is_empty()
        && local
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "._%+-".contains(c));
    let Some((host, tld)) = domain.rsplit_once('.') else {
        return refused();
    };
    let host_ok = !host.is_empty()
        && host
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || ".-".contains(c));
    let tld_ok = tld.len() >= 2 && tld.chars().all(|c| c.is_ascii_alphabetic());
    if !(local_ok && host_ok && tld_ok) {
        return refused();
    }
    Ok(normalized)
}

pub fn catalog() -> Arc<dyn Scalars> {
    Arc::new(Catalog)
}

/// A fixed instant, 2026-10-08T12:00:00Z, plus `d`.
pub fn at(d: Duration) -> SystemTime {
    UNIX_EPOCH + Duration::from_secs(1_791_460_800) + d
}

/// A database holding the fixture's tables.
pub struct TestDb {
    pub name: &'static str,
    pub client: Arc<dyn Client>,
    cleanup: Option<Cleanup>,
}

#[cfg(feature = "identity-postgres")]
type Cleanup = (tokio_postgres::Client, String);
#[cfg(not(feature = "identity-postgres"))]
type Cleanup = ();

impl TestDb {
    /// A store over the database, from `descriptor`.
    pub fn store(&self, descriptor: &str) -> SqlStore {
        SqlStore::new(Arc::clone(&self.client), descriptor, catalog()).unwrap()
    }

    /// Drops the Postgres schema the database is.
    pub async fn close(self) {
        #[cfg(feature = "identity-postgres")]
        if let Some((admin, schema)) = self.cleanup {
            let _ = admin
                .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
                .await;
        }
        #[cfg(not(feature = "identity-postgres"))]
        let _ = self.cleanup;
    }
}

/// A SQLite database in memory with `ddl` applied.
pub fn sqlite_with(ddl: &str) -> TestDb {
    let connection = rusqlite::Connection::open_in_memory().unwrap();
    connection.execute_batch(ddl).unwrap();
    TestDb {
        name: "sqlite",
        client: Arc::new(Rusqlite::new(connection).unwrap()),
        cleanup: None,
    }
}

pub fn sqlite() -> TestDb {
    sqlite_with(&fixture("sqlite/create.sql"))
}

/// The databases a test runs on: SQLite, and Postgres when
/// [`POSTGRES_URL_ENV`] names one.
pub async fn databases() -> Vec<TestDb> {
    let mut dbs = vec![sqlite()];
    if let Some(db) = postgres().await {
        dbs.push(db);
    } else {
        eprintln!("{POSTGRES_URL_ENV} is unset or identity-postgres is off: the test runs on SQLite alone");
    }
    dbs
}

#[cfg(not(feature = "identity-postgres"))]
async fn postgres() -> Option<TestDb> {
    None
}

/// A Postgres connection whose search path is a schema of its own holding
/// the fixture's `create.sql`.
#[cfg(feature = "identity-postgres")]
async fn postgres() -> Option<TestDb> {
    use std::sync::atomic::{AtomicU64, Ordering};
    use superschematic_http_runtime::identity::TokioPostgres;

    static NEXT: AtomicU64 = AtomicU64::new(0);
    let url = std::env::var(POSTGRES_URL_ENV)
        .ok()
        .filter(|u| !u.is_empty())?;
    let connect = |config: tokio_postgres::Config| async move {
        let (client, connection) = config
            .connect(tokio_postgres::NoTls)
            .await
            .unwrap_or_else(|err| panic!("connect to {POSTGRES_URL_ENV}: {err}"));
        tokio::spawn(async move {
            let _ = connection.await;
        });
        client
    };
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(config.clone()).await;
    // An extension's name is unique in the database, so create the DDL's
    // extensions once in public, where every schema's search path finds
    // them.
    for extension in ["pgcrypto", "citext"] {
        if let Err(err) = admin
            .batch_execute(&format!(
                "CREATE EXTENSION IF NOT EXISTS {extension} SCHEMA public"
            ))
            .await
        {
            assert!(
                err.code().map(|c| c.code()) == Some("23505"),
                "create {extension}: {err}"
            );
        }
    }
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let schema = format!(
        "identity_store_{nanos}_{}",
        NEXT.fetch_add(1, Ordering::SeqCst)
    );
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .unwrap();
    let mut config = config;
    config.options(format!("-c search_path={schema},public"));
    let client = connect(config).await;
    client
        .batch_execute(&fixture("create.sql"))
        .await
        .unwrap_or_else(|err| panic!("apply create.sql: {err}"));
    Some(TestDb {
        name: "postgres",
        client: Arc::new(TokioPostgres::new(client)),
        cleanup: Some((admin, schema)),
    })
}
