//! The database of a generated Rust server in a stack (D57, section 8.6 of
//! `docs/stack-model.md`): a [`Pool`] of tokio-postgres connections
//! (`deadpool-postgres`) opened from the database field its sql edge
//! derives, which [`crate::stackconfig::load_database`] reads, and the
//! [`ping`] its `/readyz` makes of each pool. With the `identity-postgres`
//! feature too, `identity::PostgresPool` runs the identity store over the
//! same pool.
//!
//! A pool connects when it is first used, so the server starts while its
//! database is not up, and `/readyz` reports it. Each connection the pool
//! opens is opened as below.
//!
//! **A connection string** connects as it says, with tokio-postgres's
//! reading of it, and its `sslmode` read as libpq and the Go server's pgx
//! read it:
//!
//! | `sslmode` | TLS |
//! |-----------|-----|
//! | `disable` | none |
//! | `prefer`, and no `sslmode` | when the server offers it, without verifying its certificate |
//! | `require` | always, without verifying the certificate |
//! | `verify-ca` | always, with a certificate the system's roots sign |
//! | `verify-full` | always, with a certificate the system's roots sign for the host |
//!
//! The TLS is native-tls's, the `http-client` feature's and the generated
//! SDKs'. tokio-postgres reads neither `verify-ca` nor `verify-full`, so
//! the runtime connects with `require` and verifies itself; nor does it read
//! `sslrootcert`, which it refuses: a private root goes into the image's
//! trust store.
//!
//! **A Cloud SQL instance** connects over the Unix socket Cloud Run mounts
//! for it, `/cloudsql/<connection name>/.s.PGSQL.5432`, which gcp's
//! workload adds for each instance its databases are on, as the IAM
//! database user gcp's connector creates, `<account>@<project>.iam`. The
//! password is an OAuth access token for the workload's account, which the
//! connection asks the metadata server for (`GET
//! http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token`
//! with `Metadata-Flavor: Google`, the host from `GCE_METADATA_HOST` when it
//! is set), through the `http-client` feature's client. Each new connection
//! asks for a fresh token, so a pool outlives any one token, and a
//! connection lives on after its token expires, since Postgres checks the
//! password only when it connects. The socket carries no TLS: Cloud Run's
//! Cloud SQL connection encrypts the hop to the instance and ends it at the
//! socket. So no Google library enters the runtime (D6).

use std::error::Error;
use std::fmt;
use std::io;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};
use std::time::Duration;

use serde::Deserialize;
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::config::SslMode;
use tokio_postgres::NoTls;

/// The crate the pool is, re-exported so a generated crate names the
/// version this one builds with.
pub use deadpool_postgres;
/// The pool of a server's database, which every API on the database
/// shares: `pool.get().await` lends a connection, a
/// `deadpool_postgres::Object` that derefs to a [`tokio_postgres::Client`].
pub use deadpool_postgres::Pool;
/// The driver the pool's connections are, re-exported as the pool is.
pub use tokio_postgres;

use crate::stackconfig::{CloudSql, Database};
use crate::HttpGetter;

/// Where Cloud Run mounts the Unix socket of each Cloud SQL instance a
/// workload connects to, in a directory named for its connection name.
pub const CLOUD_SQL_SOCKET_DIR: &str = "/cloudsql";

/// The metadata server's path for an access token of the workload's
/// account.
const ACCESS_TOKEN_PATH: &str = "/computeMetadata/v1/instance/service-accounts/default/token";

/// How long the pool waits for a new connection before it gives up.
const CREATE_TIMEOUT: Duration = Duration::from_secs(10);

/// A database the runtime could not open, or that did not answer. Its
/// message holds the chain of causes, since tokio-postgres's errors print
/// only their kind, and never a password.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PostgresError {
    message: String,
}

impl PostgresError {
    fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }

    fn caused(context: &str, err: &(dyn Error + 'static)) -> Self {
        Self::new(format!("{context}: {}", chain(err)))
    }
}

impl fmt::Display for PostgresError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl Error for PostgresError {}

/// An error and its causes, each after a colon, leaving out a cause whose
/// text the message already ends with.
fn chain(err: &(dyn Error + 'static)) -> String {
    let mut out = err.to_string();
    let mut source = err.source();
    while let Some(cause) = source {
        let text = cause.to_string();
        if !out.ends_with(&text) {
            out.push_str(": ");
            out.push_str(&text);
        }
        source = cause.source();
    }
    out
}

/// What [`connect_with`] takes beside the database, each with its default.
#[derive(Clone, Default)]
pub struct ConnectOptions {
    max_size: Option<usize>,
    socket_dir: Option<PathBuf>,
    metadata_host: Option<String>,
    getter: Option<Arc<dyn HttpGetter>>,
}

impl ConnectOptions {
    pub fn new() -> Self {
        Self::default()
    }

    /// The most connections the pool holds; deadpool's default, four per
    /// CPU, without it.
    #[must_use]
    pub fn with_max_size(mut self, max_size: usize) -> Self {
        self.max_size = Some(max_size);
        self
    }

    /// The directory holding each Cloud SQL instance's socket directory, in
    /// place of [`CLOUD_SQL_SOCKET_DIR`].
    #[must_use]
    pub fn with_socket_dir(mut self, dir: impl Into<PathBuf>) -> Self {
        self.socket_dir = Some(dir.into());
        self
    }

    /// The metadata server's `host[:port]`, in place of `GCE_METADATA_HOST`.
    #[must_use]
    pub fn with_metadata_host(mut self, host: impl Into<String>) -> Self {
        self.metadata_host = Some(host.into());
        self
    }

    /// The client that asks the metadata server, in place of the
    /// `http-client` feature's.
    #[must_use]
    pub fn with_getter(mut self, getter: Arc<dyn HttpGetter>) -> Self {
        self.getter = Some(getter);
        self
    }
}

/// Opens the pool of `db` with the default options: tokio-postgres over a
/// connection string, or the Cloud SQL socket with an access token as the
/// password. It does not connect; the pool connects when it is first used.
/// It fails for a connection string tokio-postgres does not read, which
/// its message does not quote.
pub fn connect(db: &Database) -> Result<Pool, PostgresError> {
    connect_with(db, ConnectOptions::default())
}

/// [`connect`] with `options`.
pub fn connect_with(db: &Database, options: ConnectOptions) -> Result<Pool, PostgresError> {
    let manager = match db {
        Database::Url(url) => url_manager(url)?,
        Database::CloudSql(cloud_sql) => cloud_sql_manager(cloud_sql, &options)?,
    };
    let mut builder = Pool::builder(manager)
        .runtime(deadpool_postgres::Runtime::Tokio1)
        .create_timeout(Some(CREATE_TIMEOUT));
    if let Some(max_size) = options.max_size {
        builder = builder.max_size(max_size);
    }
    builder
        .build()
        .map_err(|err| PostgresError::caused("building the database pool", &err))
}

/// Succeeds when the database answers `SELECT 1` on a connection of the
/// pool: what a server's `/readyz` asks of each pool. It waits as long as
/// the pool does for a connection, up to 10 seconds for a new one, so a
/// readiness check bounds it with `tokio::time::timeout`.
pub async fn ping(pool: &Pool) -> Result<(), PostgresError> {
    let client = pool
        .get()
        .await
        .map_err(|err| PostgresError::caused("no connection to the database", &err))?;
    client
        .simple_query("SELECT 1")
        .await
        .map_err(|err| PostgresError::caused("the database did not answer", &err))?;
    Ok(())
}

fn manager_config() -> deadpool_postgres::ManagerConfig {
    deadpool_postgres::ManagerConfig {
        recycling_method: deadpool_postgres::RecyclingMethod::Fast,
    }
}

/// How much of the server's certificate a connection string's TLS checks.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Verify {
    /// Nothing: `prefer` and `require`, as libpq reads them.
    Nothing,
    /// That the system's roots sign it: `verify-ca`.
    Chain,
    /// That the system's roots sign it for the host: `verify-full`.
    Host,
}

fn url_manager(url: &str) -> Result<deadpool_postgres::Manager, PostgresError> {
    let (url, verify) = read_ssl_mode(url);
    let config: tokio_postgres::Config = url
        .parse()
        .map_err(|err| PostgresError::caused("the database's connection string", &err))?;
    let tls = native_tls::TlsConnector::builder()
        .danger_accept_invalid_certs(verify == Verify::Nothing)
        .danger_accept_invalid_hostnames(verify != Verify::Host)
        .build()
        .map_err(|err| PostgresError::caused("the database's TLS", &err))?;
    Ok(deadpool_postgres::Manager::from_config(
        config,
        postgres_native_tls::MakeTlsConnector::new(tls),
        manager_config(),
    ))
}

/// The connection string with a `verify-ca` or `verify-full` `sslmode`
/// read as `require`, which tokio-postgres reads, and what the TLS
/// verifies. A string tokio-postgres will refuse is left as it is.
fn read_ssl_mode(url: &str) -> (String, Verify) {
    let rewritten = if url.starts_with("postgres://") || url.starts_with("postgresql://") {
        rewrite_url_ssl_mode(url)
    } else {
        rewrite_pairs_ssl_mode(url)
    };
    let Some((url, mode)) = rewritten else {
        return (url.to_string(), Verify::Nothing);
    };
    let verify = match mode.as_deref() {
        Some("verify-full") => Verify::Host,
        Some("verify-ca") => Verify::Chain,
        _ => Verify::Nothing,
    };
    (url, verify)
}

fn is_verify(mode: &str) -> bool {
    matches!(mode, "verify-ca" | "verify-full")
}

/// A URL's query with its `sslmode` rewritten, and the last `sslmode`,
/// which tokio-postgres reads.
fn rewrite_url_ssl_mode(url: &str) -> Option<(String, Option<String>)> {
    let Some((base, query)) = url.split_once('?') else {
        return Some((url.to_string(), None));
    };
    let mut mode = None;
    let mut pairs = Vec::new();
    for pair in query.split('&') {
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        if percent_decode(key)? == "sslmode" {
            let value = percent_decode(value)?;
            if is_verify(&value) {
                pairs.push("sslmode=require".to_string());
                mode = Some(value);
                continue;
            }
            mode = Some(value);
        }
        pairs.push(pair.to_string());
    }
    Some((format!("{base}?{}", pairs.join("&")), mode))
}

fn percent_decode(text: &str) -> Option<String> {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            let hex = text.get(i + 1..i + 3)?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

/// A string of `key=value` pairs with its `sslmode` rewritten, read as
/// tokio-postgres reads one: whitespace between pairs and around `=`, and
/// a value quoted in `'` or bare, `\` escaping the character after it.
fn rewrite_pairs_ssl_mode(text: &str) -> Option<(String, Option<String>)> {
    let chars: Vec<char> = text.chars().collect();
    let mut out = String::with_capacity(text.len());
    let mut mode = None;
    let mut i = 0;
    loop {
        let start = i;
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        out.extend(&chars[start..i]);
        if i == chars.len() {
            break;
        }
        let pair_start = i;
        let key_start = i;
        while i < chars.len() && chars[i] != '=' && !chars[i].is_whitespace() {
            i += 1;
        }
        let key: String = chars[key_start..i].iter().collect();
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        if chars.get(i) != Some(&'=') {
            return None;
        }
        i += 1;
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        let mut value = String::new();
        if chars.get(i) == Some(&'\'') {
            i += 1;
            loop {
                match chars.get(i) {
                    None => return None,
                    Some('\'') => {
                        i += 1;
                        break;
                    }
                    Some('\\') => {
                        value.push(*chars.get(i + 1)?);
                        i += 2;
                    }
                    Some(c) => {
                        value.push(*c);
                        i += 1;
                    }
                }
            }
        } else {
            while let Some(&c) = chars.get(i) {
                if c.is_whitespace() {
                    break;
                }
                if c == '\\' {
                    value.push(*chars.get(i + 1)?);
                    i += 2;
                } else {
                    value.push(c);
                    i += 1;
                }
            }
        }
        if key == "sslmode" && is_verify(&value) {
            out.push_str("sslmode=require");
        } else {
            out.extend(&chars[pair_start..i]);
        }
        if key == "sslmode" {
            mode = Some(value);
        }
    }
    Some((out, mode))
}

/// The tokio-postgres config of a Cloud SQL instance's socket.
fn cloud_sql_config(
    cloud_sql: &CloudSql,
    options: &ConnectOptions,
) -> Result<tokio_postgres::Config, PostgresError> {
    let dir = options
        .socket_dir
        .clone()
        .unwrap_or_else(|| PathBuf::from(CLOUD_SQL_SOCKET_DIR));
    let mut config = tokio_postgres::Config::new();
    socket_host(&mut config, dir.join(&cloud_sql.instance))?;
    config
        .user(&cloud_sql.user)
        .dbname(&cloud_sql.database)
        .ssl_mode(SslMode::Disable);
    Ok(config)
}

#[cfg(unix)]
fn socket_host(config: &mut tokio_postgres::Config, dir: PathBuf) -> Result<(), PostgresError> {
    config.host_path(dir);
    Ok(())
}

#[cfg(not(unix))]
fn socket_host(_config: &mut tokio_postgres::Config, dir: PathBuf) -> Result<(), PostgresError> {
    Err(PostgresError::new(format!(
        "a Cloud SQL socket, {}, needs a Unix platform",
        dir.display()
    )))
}

fn cloud_sql_manager(
    cloud_sql: &CloudSql,
    options: &ConnectOptions,
) -> Result<deadpool_postgres::Manager, PostgresError> {
    let config = cloud_sql_config(cloud_sql, options)?;
    let host = options
        .metadata_host
        .clone()
        .unwrap_or_else(crate::token_source::metadata_host);
    let connect = CloudSqlConnect {
        instance: cloud_sql.instance.clone(),
        token_url: format!("http://{host}{ACCESS_TOKEN_PATH}"),
        getter: options
            .getter
            .clone()
            .unwrap_or_else(|| Arc::new(crate::HttpFetcher::new())),
    };
    Ok(deadpool_postgres::Manager::from_connect(
        config,
        connect,
        manager_config(),
    ))
}

/// Opens each connection of a Cloud SQL pool: an access token as the
/// password, then the socket.
struct CloudSqlConnect {
    instance: String,
    token_url: String,
    getter: Arc<dyn HttpGetter>,
}

#[derive(Deserialize)]
struct AccessToken {
    access_token: String,
}

impl CloudSqlConnect {
    async fn access_token(&self) -> Result<String, String> {
        let body = self
            .getter
            .get(&self.token_url, &[("Metadata-Flavor", "Google")])
            .await?;
        let token: AccessToken = serde_json::from_slice(&body)
            .map_err(|_| "the answer is not an access token".to_string())?;
        if token.access_token.is_empty() {
            return Err("the answer's access token is empty".to_string());
        }
        Ok(token.access_token)
    }
}

type ConnectFuture<'a> = Pin<
    Box<
        dyn std::future::Future<
                Output = Result<
                    (tokio_postgres::Client, tokio::task::JoinHandle<()>),
                    tokio_postgres::Error,
                >,
            > + Send
            + 'a,
    >,
>;

impl deadpool_postgres::Connect for CloudSqlConnect {
    fn connect(&self, pg_config: &tokio_postgres::Config) -> ConnectFuture<'_> {
        let mut config = pg_config.clone();
        Box::pin(async move {
            let token = match self.access_token().await {
                Ok(token) => token,
                Err(err) => {
                    return Err(io_error(io::Error::other(format!(
                        "Cloud SQL instance {}: an access token from the metadata server: {err}",
                        self.instance
                    )))
                    .await)
                }
            };
            config.password(token);
            let (client, connection) = config.connect(NoTls).await?;
            let instance = self.instance.clone();
            let handle = tokio::spawn(async move {
                if let Err(err) = connection.await {
                    tracing::warn!(
                        instance = %instance,
                        error = %chain(&err),
                        "postgres: a Cloud SQL connection failed"
                    );
                }
            });
            Ok((client, handle))
        })
    }
}

/// A tokio-postgres error whose cause is `err`. deadpool's `Connect` returns
/// only tokio-postgres's error, which tokio-postgres builds only from a
/// failure of its own, so this hands it a stream that fails with `err`.
async fn io_error(err: io::Error) -> tokio_postgres::Error {
    let mut config = tokio_postgres::Config::new();
    config.user("-").ssl_mode(SslMode::Disable);
    let mut cause = Some(err);
    loop {
        // A stream that fails every write never starts a session, so the
        // first pass returns.
        if let Err(err) = config.connect_raw(Failing(cause.take()), NoTls).await {
            return err;
        }
    }
}

/// A stream whose every read and write fails with its error.
struct Failing(Option<io::Error>);

impl Failing {
    fn fail<T>(&mut self) -> Poll<io::Result<T>> {
        Poll::Ready(Err(self
            .0
            .take()
            .unwrap_or_else(|| io::Error::from(io::ErrorKind::BrokenPipe))))
    }
}

impl AsyncRead for Failing {
    fn poll_read(
        self: Pin<&mut Self>,
        _cx: &mut Context<'_>,
        _buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        self.get_mut().fail()
    }
}

impl AsyncWrite for Failing {
    fn poll_write(
        self: Pin<&mut Self>,
        _cx: &mut Context<'_>,
        _buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        self.get_mut().fail()
    }

    fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.get_mut().fail()
    }

    fn poll_shutdown(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use std::sync::Mutex;

    #[test]
    fn a_verify_ssl_mode_is_read_as_require_and_verified_by_the_runtime() {
        let cases = [
            ("postgres://u:p@h/d", "postgres://u:p@h/d", Verify::Nothing),
            (
                "postgres://u:p@h/d?sslmode=disable",
                "postgres://u:p@h/d?sslmode=disable",
                Verify::Nothing,
            ),
            (
                "postgresql://h/d?application_name=a&sslmode=verify-full",
                "postgresql://h/d?application_name=a&sslmode=require",
                Verify::Host,
            ),
            (
                "postgres://h/d?ssl%6dode=verify-ca&connect_timeout=5",
                "postgres://h/d?sslmode=require&connect_timeout=5",
                Verify::Chain,
            ),
            (
                "postgres://h/d?sslmode=verify-full&sslmode=require",
                "postgres://h/d?sslmode=require&sslmode=require",
                Verify::Nothing,
            ),
            (
                "host=h  dbname=d sslmode = verify-full",
                "host=h  dbname=d sslmode=require",
                Verify::Host,
            ),
            (
                "host=h password='a b\\' c' sslmode='verify-ca'",
                "host=h password='a b\\' c' sslmode=require",
                Verify::Chain,
            ),
            (
                "host=h sslmode=require",
                "host=h sslmode=require",
                Verify::Nothing,
            ),
            // tokio-postgres refuses these; the runtime leaves them to it.
            (
                "host=h password='open",
                "host=h password='open",
                Verify::Nothing,
            ),
            (
                "postgres://h/d?sslmode=%zz",
                "postgres://h/d?sslmode=%zz",
                Verify::Nothing,
            ),
        ];
        for (url, want, verify) in cases {
            assert_eq!(read_ssl_mode(url), (want.to_string(), verify), "{url}");
            if !want.contains("'open") && !want.contains("%zz") {
                assert!(
                    want.parse::<tokio_postgres::Config>().is_ok(),
                    "tokio-postgres reads {want}"
                );
            }
        }
    }

    #[test]
    fn a_connection_string_tokio_postgres_does_not_read_is_refused_without_quoting_it() {
        let err = connect(&Database::Url(
            "postgres://u:secret@h/d?sslrootcert=/etc/ca.pem".to_string(),
        ))
        .err()
        .unwrap();
        let text = err.to_string();
        assert!(text.contains("sslrootcert"), "{text}");
        assert!(!text.contains("secret"), "{text}");
    }

    #[test]
    fn a_cloud_sql_instance_is_its_socket_as_its_iam_user() {
        let cloud_sql = CloudSql {
            instance: "acme:us-central1:shop".to_string(),
            database: "shop_db".to_string(),
            user: "shop-api@acme.iam".to_string(),
        };
        let config = cloud_sql_config(&cloud_sql, &ConnectOptions::new()).unwrap();
        assert_eq!(
            config.get_hosts(),
            [tokio_postgres::config::Host::Unix(PathBuf::from(
                "/cloudsql/acme:us-central1:shop"
            ))]
        );
        assert_eq!(config.get_ports(), [] as [u16; 0], "the default, 5432");
        assert_eq!(config.get_user(), Some("shop-api@acme.iam"));
        assert_eq!(config.get_dbname(), Some("shop_db"));
        assert_eq!(config.get_ssl_mode(), SslMode::Disable);
        assert_eq!(config.get_password(), None);
    }

    type Requests = Arc<Mutex<Vec<(String, Vec<(String, String)>)>>>;

    /// Answers each GET with `answer`, recording the request.
    struct Metadata {
        answer: Result<String, String>,
        requests: Requests,
    }

    #[async_trait]
    impl HttpGetter for Metadata {
        async fn get(&self, url: &str, headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
            self.requests.lock().unwrap().push((
                url.to_string(),
                headers
                    .iter()
                    .map(|(name, value)| (name.to_string(), value.to_string()))
                    .collect(),
            ));
            self.answer.clone().map(String::into_bytes)
        }
    }

    fn connector(answer: Result<&str, &str>, requests: &Requests) -> CloudSqlConnect {
        CloudSqlConnect {
            instance: "p:r:i".to_string(),
            token_url: format!("http://metadata.test{ACCESS_TOKEN_PATH}"),
            getter: Arc::new(Metadata {
                answer: answer.map(str::to_string).map_err(str::to_string),
                requests: Arc::clone(requests),
            }),
        }
    }

    #[tokio::test]
    async fn the_password_is_an_access_token_from_the_metadata_server() {
        let requests = Requests::default();
        let token = connector(
            Ok(r#"{"access_token":"ya29.token","expires_in":3599,"token_type":"Bearer"}"#),
            &requests,
        )
        .access_token()
        .await;
        assert_eq!(token.as_deref(), Ok("ya29.token"));
        assert_eq!(
            requests.lock().unwrap().as_slice(),
            [(
                "http://metadata.test/computeMetadata/v1/instance/service-accounts/default/token"
                    .to_string(),
                vec![("Metadata-Flavor".to_string(), "Google".to_string())]
            )]
        );
        for answer in [r#"{"access_token":""}"#, "not json"] {
            assert!(connector(Ok(answer), &requests)
                .access_token()
                .await
                .is_err());
        }
    }

    #[tokio::test]
    async fn a_token_the_metadata_server_refuses_fails_the_connection_with_its_cause() {
        let requests = Requests::default();
        let config = tokio_postgres::Config::new();
        let connect = connector(Err("GET http://metadata.test: 500"), &requests);
        let err = deadpool_postgres::Connect::connect(&connect, &config)
            .await
            .err()
            .unwrap();
        assert_eq!(
            chain(&err),
            "error communicating with the server: Cloud SQL instance p:r:i: an access token \
             from the metadata server: GET http://metadata.test: 500"
        );
    }

    #[tokio::test]
    async fn the_pool_opens_without_connecting() {
        let pool = connect_with(
            &Database::CloudSql(CloudSql {
                instance: "p:r:i".to_string(),
                database: "d".to_string(),
                user: "u@p.iam".to_string(),
            }),
            ConnectOptions::new()
                .with_max_size(3)
                .with_socket_dir(std::env::temp_dir().join("no-such-cloudsql"))
                .with_getter(Arc::new(Metadata {
                    answer: Err("down".to_string()),
                    requests: Requests::default(),
                })),
        )
        .unwrap();
        assert_eq!(pool.status().max_size, 3);
        assert_eq!(pool.status().size, 0);
        let err = ping(&pool).await.unwrap_err().to_string();
        assert!(err.starts_with("no connection to the database"), "{err}");
    }
}
