//! The `postgres` feature's pool on a real Postgres, the one
//! `SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL` names (the tests skip
//! without it): over the connection string, and over a Cloud SQL socket.
//! `SUPERSCHEMATIC_POSTGRES_TEST_TLS=1` says the server offers TLS with a
//! certificate the system's roots do not sign, such as a self-signed one.
//! The Cloud SQL test serves a Unix socket at
//! `<dir>/<connection name>/.s.PGSQL.5432` that relays each connection to
//! the database, as Cloud Run's Cloud SQL connection does, and a fake
//! metadata server whose access token is the password of a role the test
//! creates for the IAM user.

use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use axum::http::{HeaderMap, StatusCode};
use axum::routing::get;
use axum::Router;
use superschematic_http_runtime::postgres::deadpool_postgres::Object;
use superschematic_http_runtime::postgres::{
    connect, connect_with, ping, tokio_postgres, ConnectOptions, Pool,
};
use superschematic_http_runtime::stackconfig::{CloudSql, Database};

const URL_ENV: &str = "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL";

fn database_url() -> Option<String> {
    let url = std::env::var(URL_ENV).ok().filter(|url| !url.is_empty());
    if url.is_none() {
        eprintln!("skipped: {URL_ENV} is unset");
    }
    url
}

async fn current_user(pool: &Pool) -> String {
    let client = pool.get().await.unwrap();
    client
        .query_one("SELECT current_user::text", &[])
        .await
        .unwrap()
        .get(0)
}

#[tokio::test]
async fn a_connection_string_connects_as_it_says() {
    let Some(url) = database_url() else { return };
    let pool = connect(&Database::Url(url.clone())).unwrap();
    ping(&pool).await.unwrap();
    let config: tokio_postgres::Config = url.parse().unwrap();
    assert_eq!(Some(current_user(&pool).await.as_str()), config.get_user());

    // Connections in use at once are each the pool's own.
    let (a, b) = (pool.get().await.unwrap(), pool.get().await.unwrap());
    assert_ne!(backend_pid(&a).await, backend_pid(&b).await);
    assert_eq!(pool.status().size, 2);

    // TLS follows sslmode: prefer falls back to none when the server
    // offers none, and require and verify-full, which the runtime reads
    // itself, need it. A test server with TLS answers each.
    let tls = std::env::var(TLS_ENV).is_ok_and(|on| on == "1");
    let modes = [
        ("disable", true),
        ("prefer", true),
        ("require", tls),
        ("verify-ca", false),
        ("verify-full", false),
    ];
    for (mode, connects) in modes {
        let pool = connect(&Database::Url(with_ssl_mode(&url, mode))).unwrap();
        let result = ping(&pool).await;
        assert_eq!(result.is_ok(), connects, "sslmode={mode}: {result:?}");
        if connects {
            let client = pool.get().await.unwrap();
            let row = client
                .query_one(
                    "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()",
                    &[],
                )
                .await
                .unwrap();
            assert_eq!(
                row.get::<_, bool>(0),
                tls && mode != "disable",
                "sslmode={mode}"
            );
        }
    }
}

/// Whether the server offers TLS, with a certificate the system's roots do
/// not sign.
const TLS_ENV: &str = "SUPERSCHEMATIC_POSTGRES_TEST_TLS";

/// `url` with its `sslmode` replaced by `mode`.
fn with_ssl_mode(url: &str, mode: &str) -> String {
    let (base, query) = url.split_once('?').unwrap_or((url, ""));
    let mut pairs: Vec<&str> = query
        .split('&')
        .filter(|pair| !pair.is_empty() && !pair.starts_with("sslmode="))
        .collect();
    let ssl_mode = format!("sslmode={mode}");
    pairs.push(&ssl_mode);
    format!("{base}?{}", pairs.join("&"))
}

async fn backend_pid(client: &Object) -> i32 {
    client
        .query_one("SELECT pg_backend_pid()", &[])
        .await
        .unwrap()
        .get(0)
}

/// Serves `dir/.s.PGSQL.5432`, relaying each connection to `host:port`,
/// and counts them.
fn relay(dir: PathBuf, host: String, port: u16) -> Arc<AtomicUsize> {
    std::fs::create_dir_all(&dir).unwrap();
    let listener = tokio::net::UnixListener::bind(dir.join(".s.PGSQL.5432")).unwrap();
    let accepted = Arc::new(AtomicUsize::new(0));
    let count = Arc::clone(&accepted);
    tokio::spawn(async move {
        while let Ok((mut socket, _)) = listener.accept().await {
            count.fetch_add(1, Ordering::SeqCst);
            let address = format!("{host}:{port}");
            tokio::spawn(async move {
                let mut database = tokio::net::TcpStream::connect(address).await.unwrap();
                let _ = tokio::io::copy_bidirectional(&mut socket, &mut database).await;
            });
        }
    });
    accepted
}

/// A metadata server whose access token is `token`, answering only a
/// request with `Metadata-Flavor: Google`, and counting the token requests.
async fn metadata(token: &'static str) -> (String, Arc<AtomicUsize>) {
    let asked = Arc::new(AtomicUsize::new(0));
    let count = Arc::clone(&asked);
    let app = Router::new().route(
        "/computeMetadata/v1/instance/service-accounts/default/token",
        get(move |headers: HeaderMap| {
            let count = Arc::clone(&count);
            async move {
                if headers.get("metadata-flavor").map(|v| v.as_bytes()) != Some(b"Google") {
                    return Err(StatusCode::FORBIDDEN);
                }
                count.fetch_add(1, Ordering::SeqCst);
                Ok(format!(
                    r#"{{"access_token":"{token}","expires_in":3599,"token_type":"Bearer"}}"#
                ))
            }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap().to_string();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    (address, asked)
}

#[tokio::test]
async fn a_cloud_sql_instance_connects_over_its_socket_with_an_access_token() {
    let Some(url) = database_url() else { return };
    let config: tokio_postgres::Config = url.parse().unwrap();
    let (host, port) = match (config.get_hosts(), config.get_ports()) {
        ([tokio_postgres::config::Host::Tcp(host)], ports) => {
            (host.clone(), ports.first().copied().unwrap_or(5432))
        }
        _ => panic!("{URL_ENV} names one TCP host"),
    };
    let database = config.get_dbname().unwrap_or("postgres").to_string();

    // The IAM user, with the access token as its password.
    const TOKEN: &str = "ya29.a-fake-access-token";
    let user = format!("stack-runtime-{}@acme.iam", std::process::id());
    let admin = connect(&Database::Url(url.clone())).unwrap();
    admin
        .get()
        .await
        .unwrap()
        .batch_execute(&format!(
            r#"DROP ROLE IF EXISTS "{user}"; CREATE ROLE "{user}" LOGIN PASSWORD '{TOKEN}'"#
        ))
        .await
        .unwrap();

    // A short directory, since a socket's path is at most about 100 bytes.
    let dir = std::env::temp_dir().join(format!("csql-{}", std::process::id()));
    let instance = "p:r:i";
    let accepted = relay(dir.join(instance), host, port);
    let (metadata_host, asked) = metadata(TOKEN).await;

    let cloud_sql = CloudSql {
        instance: instance.to_string(),
        database: database.clone(),
        user: user.clone(),
    };
    let options = ConnectOptions::new()
        .with_socket_dir(&dir)
        .with_metadata_host(&metadata_host);
    let pool = connect_with(&Database::CloudSql(cloud_sql.clone()), options.clone()).unwrap();
    assert_eq!(
        asked.load(Ordering::SeqCst),
        0,
        "the pool connects when used"
    );
    ping(&pool).await.unwrap();
    assert_eq!(current_user(&pool).await, user);
    assert_eq!(asked.load(Ordering::SeqCst), 1);
    assert_eq!(accepted.load(Ordering::SeqCst), 1);

    // Each new connection asks for a token of its own.
    let (a, b) = (pool.get().await.unwrap(), pool.get().await.unwrap());
    drop((a, b));
    assert_eq!(asked.load(Ordering::SeqCst), 2);
    assert_eq!(accepted.load(Ordering::SeqCst), 2);
    ping(&pool).await.unwrap();
    assert_eq!(
        asked.load(Ordering::SeqCst),
        2,
        "a pooled connection is kept"
    );

    // A token that is not the password is refused by the database.
    let (wrong_host, _) = metadata("ya29.another-token").await;
    let refused = connect_with(
        &Database::CloudSql(cloud_sql),
        options.with_metadata_host(&wrong_host),
    )
    .unwrap();
    let err = ping(&refused).await.unwrap_err().to_string();
    assert!(err.contains("password authentication failed"), "{err}");
    assert!(!err.contains("ya29"), "{err}");

    drop((pool, refused));
    admin
        .get()
        .await
        .unwrap()
        .batch_execute(&format!(r#"DROP ROLE IF EXISTS "{user}""#))
        .await
        .unwrap();
    let _ = std::fs::remove_dir_all(&dir);
}

/// The identity store's client over the pool: a transaction dropped
/// without ending leaves no write and no open transaction behind.
#[cfg(feature = "identity-postgres")]
#[tokio::test]
async fn an_abandoned_identity_transaction_closes_its_connection() {
    use superschematic_http_runtime::identity::{Client, PostgresPool, SqlValue};

    let Some(url) = database_url() else { return };
    let pool = connect_with(&Database::Url(url), ConnectOptions::new().with_max_size(1)).unwrap();
    let client = PostgresPool::new(pool.clone());
    let table = format!("abandoned_{}", std::process::id());
    client
        .execute(&format!("CREATE TABLE {table} (v TEXT)"), &[])
        .await
        .unwrap();
    let pid = "SELECT pg_backend_pid()::bigint";
    let abandoned = {
        let mut tx = client.begin().await.unwrap();
        tx.execute(
            &format!("INSERT INTO {table} VALUES ($1)"),
            &[SqlValue::from("dropped")],
        )
        .await
        .unwrap();
        tx.query(pid, &[]).await.unwrap()
    };
    // The pool's one connection was the transaction's, which it closed; a
    // new one serves, and the write is gone.
    let rows = client
        .query(&format!("SELECT count(*) FROM {table}"), &[])
        .await
        .unwrap();
    assert_eq!(rows, [[SqlValue::Int(0)]]);
    assert_ne!(client.query(pid, &[]).await.unwrap(), abandoned);

    // An ended transaction's connection goes back to the pool.
    let mut tx = client.begin().await.unwrap();
    let committed = tx.query(pid, &[]).await.unwrap();
    tx.commit().await.unwrap();
    assert_eq!(client.query(pid, &[]).await.unwrap(), committed);
    let tx = client.begin().await.unwrap();
    tx.rollback().await.unwrap();
    assert_eq!(client.query(pid, &[]).await.unwrap(), committed);

    client
        .execute(&format!("DROP TABLE {table}"), &[])
        .await
        .unwrap();
}
