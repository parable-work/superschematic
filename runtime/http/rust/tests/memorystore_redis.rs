//! The Redis memory store (D75) against a real server: the Valkey
//! `SUPERSCHEMATIC_TEST_MEMORY_STORE` names, a `redis://` URL whose host
//! is loopback and whose database is not 0, so a test never touches the
//! keys of a running `stack dev`; unset skips those tests. Each test keys
//! under a store name of its own. A TCP proxy in the test's process stands
//! between the store and the server where a test takes the server away.
//!
//! The `auth: iam` test runs over TLS when
//! `SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS` names a `rediss://` Valkey whose
//! default user's password is `token-1`, with the PEM of its CA at the
//! path `SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CA` names, against a fake
//! metadata server; it skips otherwise.

use std::net::SocketAddr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::http::{HeaderMap, StatusCode};
use axum::routing::get;
use axum::Router;
use superschematic_http_runtime::memorystore::redis::{
    open, open_or_disabled, open_with, RedisOptions, Timings,
};
use superschematic_http_runtime::memorystore::{
    Channel, Key, MemoryStore, MemoryStoreError, Signal, MAX_MESSAGE_BYTES,
};
use superschematic_http_runtime::stackconfig::{MemoryStore as Connection, MemoryStoreAuth};
use tokio::net::{TcpListener, TcpStream};
use tokio::task::JoinHandle;

const STORE_ENV: &str = "SUPERSCHEMATIC_TEST_MEMORY_STORE";
const TLS_ENV: &str = "SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS";
const TLS_CA_ENV: &str = "SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CA";

/// The test server's URL, refused unless its host is loopback and its
/// database is not 0; `None` when the variable is unset.
fn server_url() -> Option<String> {
    let url = std::env::var(STORE_ENV)
        .ok()
        .filter(|url| !url.is_empty())?;
    let rest = url
        .strip_prefix("redis://")
        .unwrap_or_else(|| panic!("{STORE_ENV} is no redis:// URL"));
    let (host, db) = rest
        .split_once('/')
        .unwrap_or_else(|| panic!("{STORE_ENV} names no database; name one other than 0"));
    let host = host.rsplit_once(':').map_or(host, |(host, _)| host);
    assert!(
        matches!(host, "127.0.0.1" | "localhost" | "[::1]"),
        "{STORE_ENV} must name a loopback host"
    );
    assert!(
        db.parse::<u32>().is_ok_and(|db| db != 0),
        "{STORE_ENV} must name a database other than 0"
    );
    Some(url)
}

macro_rules! server_or_skip {
    () => {
        match server_url() {
            Some(url) => url,
            None => {
                eprintln!("{STORE_ENV} is unset; skipping");
                return;
            }
        }
    };
}

/// A store name no other test uses.
fn unique(stem: &str) -> String {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    format!(
        "{stem}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

fn connection(store: &str, url: &str) -> Connection {
    Connection {
        store: store.to_string(),
        url: url.to_string(),
        auth: MemoryStoreAuth::None,
        ca: None,
        member: None,
    }
}

/// Short timings, so a test of a lost server runs in a second or two.
fn quick() -> Timings {
    Timings {
        dial: Duration::from_millis(300),
        op: Duration::from_millis(300),
        ping: Duration::from_millis(300),
        health_interval: Duration::from_millis(100),
        min_backoff: Duration::from_millis(20),
        max_backoff: Duration::from_millis(100),
        receive_idle: Duration::from_millis(500),
    }
}

/// A connection of the test's own, which sends no CLIENT command either.
async fn admin(url: &str) -> redis::aio::MultiplexedConnection {
    let info = redis::IntoConnectionInfo::into_connection_info(url).unwrap();
    let settings = info.redis_settings().clone().set_skip_set_lib_name();
    redis::Client::open(info.set_redis_settings(settings))
        .unwrap()
        .get_multiplexed_async_connection()
        .await
        .unwrap()
}

/// Waits until `check` holds, for at most five seconds.
async fn eventually(what: &str, check: impl Fn() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(5);
    while !check() {
        assert!(Instant::now() < deadline, "{what} did not happen in time");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

#[tokio::test]
async fn a_server_that_is_down_opens_unavailable_and_refuses_at_once() {
    // A port nothing listens on.
    let nowhere = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = nowhere.local_addr().unwrap();
    drop(nowhere);
    let started = Instant::now();
    let store = open(&connection("down", &format!("redis://{address}/1")))
        .await
        .expect("a server that is down is no error");
    assert!(
        started.elapsed() < Duration::from_millis(1500),
        "open waited"
    );
    assert!(!store.available());
    let key = Key::new("k", "k", Duration::from_secs(5));
    let started = Instant::now();
    assert_eq!(
        key.increment(&store).await,
        Err(MemoryStoreError::Unavailable)
    );
    assert!(
        started.elapsed() < Duration::from_millis(50),
        "a call waited"
    );
    store.close().await;
}

#[tokio::test]
async fn a_refused_connection_runs_disabled() {
    let mut refused = connection("shop-cache", "redis://127.0.0.1:1/1");
    refused.ca = Some("-----BEGIN CERTIFICATE-----".to_string());
    assert!(matches!(
        open(&refused).await,
        Err(MemoryStoreError::Config(_))
    ));
    let store = open_or_disabled("shop-cache", "SHOP_CACHE_MEMORY_STORE", Some(&refused)).await;
    assert!(!store.available());
    let store = open_or_disabled("shop-cache", "SHOP_CACHE_MEMORY_STORE", None).await;
    assert_eq!(store.name(), "shop-cache");
    assert!(!store.available());
}

#[tokio::test]
async fn counters_and_claims_expire_and_count_on_the_server() {
    let url = server_or_skip!();
    let name = unique("keys");
    let store = open(&connection(&name, &url)).await.unwrap();
    assert!(store.available());
    let mut admin = admin(&url).await;

    let counter = Key::new("views", "views:p1", Duration::from_secs(30));
    assert_eq!(counter.count(&store).await, Ok(0));
    assert_eq!(counter.increment(&store).await, Ok(1));
    assert_eq!(counter.increment(&store).await, Ok(2));
    assert_eq!(counter.count(&store).await, Ok(2));
    let ttl: i64 = redis::cmd("PTTL")
        .arg(format!("{name}:views:p1"))
        .query_async(&mut admin)
        .await
        .unwrap();
    assert!(
        ttl > 25_000 && ttl <= 30_000,
        "the counter's TTL is {ttl}ms"
    );

    let claim = Key::new(
        "receipt-sent",
        "receipt-sent:o1",
        Duration::from_millis(300),
    );
    assert_eq!(claim.claim(&store).await, Ok(true));
    assert_eq!(claim.claim(&store).await, Ok(false));
    claim.delete(&store).await.unwrap();
    assert_eq!(claim.claim(&store).await, Ok(true));
    tokio::time::sleep(Duration::from_millis(400)).await;
    assert_eq!(claim.claim(&store).await, Ok(true), "the claim expired");
    store.close().await;
    assert!(!store.available());
    assert_eq!(
        counter.count(&store).await,
        Err(MemoryStoreError::Unavailable)
    );
}

#[tokio::test]
async fn a_message_reaches_each_subscription_until_it_stops() {
    let url = server_or_skip!();
    let name = unique("channels");
    let publisher = open(&connection(&name, &url)).await.unwrap();
    let subscriber = open(&connection(&name, &url)).await.unwrap();
    let samples: Channel<Vec<u32>> = Channel::new("samples", "samples:p1");
    let got = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::clone(&got);
    let subscription = samples
        .subscribe(&subscriber, move |m| seen.lock().unwrap().push(m))
        .await
        .unwrap();
    let waits = Arc::new(AtomicUsize::new(0));
    let woken = Arc::clone(&waits);
    let signal = Channel::signal("stock-changed", "stock-changed:p1");
    let _signal = signal
        .subscribe(&subscriber, move |Signal| {
            woken.fetch_add(1, Ordering::SeqCst);
        })
        .await
        .unwrap();

    // The subscriber connection subscribes in the background.
    let deadline = Instant::now() + Duration::from_secs(5);
    while got.lock().unwrap().is_empty() {
        assert!(Instant::now() < deadline, "no message arrived");
        samples.publish(&publisher, &vec![1, 2]).await.unwrap();
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    signal.publish(&publisher, &Signal).await.unwrap();
    eventually("the signal", || waits.load(Ordering::SeqCst) >= 1).await;

    // A message over 64 KiB is refused before it is sent.
    let big = Channel::<String>::new("samples", "samples:p1");
    assert!(matches!(
        big.publish(&publisher, &"x".repeat(MAX_MESSAGE_BYTES))
            .await,
        Err(MemoryStoreError::TooLarge(_))
    ));

    subscription.unsubscribe();
    let before = got.lock().unwrap().len();
    tokio::time::sleep(Duration::from_millis(100)).await;
    samples.publish(&publisher, &vec![9]).await.unwrap();
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(
        got.lock().unwrap().len(),
        before,
        "a stopped subscription heard"
    );
    assert!(got.lock().unwrap().iter().all(|m| m == &vec![1, 2]));
    publisher.close().await;
    subscriber.close().await;
}

/// The calls of each CLIENT subcommand the server has run.
async fn client_calls(admin: &mut redis::aio::MultiplexedConnection) -> Vec<String> {
    let stats: String = redis::cmd("INFO")
        .arg("commandstats")
        .query_async(admin)
        .await
        .unwrap();
    stats
        .lines()
        .filter(|line| line.starts_with("cmdstat_client"))
        .map(str::to_string)
        .collect()
}

#[tokio::test]
async fn no_connection_sends_a_client_command() {
    let url = server_or_skip!();
    let mut admin = admin(&url).await;
    let before = client_calls(&mut admin).await;
    let store = open(&connection(&unique("client"), &url)).await.unwrap();
    let key = Key::new("k", "k", Duration::from_secs(5));
    key.increment(&store).await.unwrap();
    let _subscription = Channel::signal("c", "c")
        .subscribe(&store, |_| {})
        .await
        .unwrap();
    tokio::time::sleep(Duration::from_millis(300)).await;
    assert_eq!(client_calls(&mut admin).await, before);
    store.close().await;
}

/// A TCP proxy to `upstream` on a port of its own, which a test stops, so
/// every connection through it drops, and starts again on the same port.
struct Proxy {
    address: SocketAddr,
    upstream: SocketAddr,
    tasks: Arc<Mutex<Vec<JoinHandle<()>>>>,
}

impl Proxy {
    async fn new(upstream: &str) -> Self {
        let upstream = tokio::net::lookup_host(upstream)
            .await
            .unwrap()
            .next()
            .unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let proxy = Self {
            address: listener.local_addr().unwrap(),
            upstream,
            tasks: Arc::default(),
        };
        proxy.serve(listener);
        proxy
    }

    fn serve(&self, listener: TcpListener) {
        let (upstream, tasks) = (self.upstream, Arc::clone(&self.tasks));
        let accept = tokio::spawn(async move {
            while let Ok((mut client, _)) = listener.accept().await {
                let pipe = tokio::spawn(async move {
                    if let Ok(mut server) = TcpStream::connect(upstream).await {
                        let _ = tokio::io::copy_bidirectional(&mut client, &mut server).await;
                    }
                });
                tasks.lock().unwrap().push(pipe);
            }
        });
        self.tasks.lock().unwrap().push(accept);
    }

    fn stop(&self) {
        for task in self.tasks.lock().unwrap().drain(..) {
            task.abort();
        }
    }

    async fn start(&self) {
        let listener = loop {
            match TcpListener::bind(self.address).await {
                Ok(listener) => break listener,
                Err(_) => tokio::time::sleep(Duration::from_millis(20)).await,
            }
        };
        self.serve(listener);
    }
}

#[tokio::test]
async fn a_lost_server_is_unavailable_until_it_returns_and_subscriptions_come_back() {
    let url = server_or_skip!();
    let rest = url.strip_prefix("redis://").unwrap();
    let (host, db) = rest.split_once('/').unwrap();
    let proxy = Proxy::new(host).await;
    let name = unique("lost");
    let store = open_with(
        &connection(&name, &format!("redis://{}/{db}", proxy.address)),
        RedisOptions::new().with_timings(quick()),
    )
    .await
    .unwrap();
    let publisher = open(&connection(&name, &url)).await.unwrap();
    assert!(store.available());
    let heard = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&heard);
    let channel = Channel::signal("wake", "wake:1");
    let _subscription = channel
        .subscribe(&store, move |Signal| {
            counted.fetch_add(1, Ordering::SeqCst);
        })
        .await
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while heard.load(Ordering::SeqCst) == 0 {
        assert!(Instant::now() < deadline, "the subscription heard nothing");
        channel.publish(&publisher, &Signal).await.unwrap();
        tokio::time::sleep(Duration::from_millis(50)).await;
    }

    proxy.stop();
    let key = Key::new("k", "k", Duration::from_secs(5));
    // A call on the lost connection fails, once retried, and marks the
    // store down; the health ping does too.
    let _ = key.increment(&store).await;
    eventually("the store going down", || !store.available()).await;
    let started = Instant::now();
    assert_eq!(
        key.increment(&store).await,
        Err(MemoryStoreError::Unavailable)
    );
    assert!(started.elapsed() < Duration::from_millis(50));

    proxy.start().await;
    eventually("the store coming back", || store.available()).await;
    assert!(key.increment(&store).await.is_ok());
    let before = heard.load(Ordering::SeqCst);
    let deadline = Instant::now() + Duration::from_secs(5);
    while heard.load(Ordering::SeqCst) == before {
        assert!(
            Instant::now() < deadline,
            "the subscription did not come back"
        );
        channel.publish(&publisher, &Signal).await.unwrap();
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    proxy.stop();
    store.close().await;
    publisher.close().await;
}

/// A fake metadata server, which gives `token-1` to a request that carries
/// `Metadata-Flavor: Google`, and counts the requests.
async fn metadata_server() -> (String, Arc<AtomicUsize>) {
    let asked = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&asked);
    let app = Router::new().route(
        "/computeMetadata/v1/instance/service-accounts/default/token",
        get(move |headers: HeaderMap| {
            let counted = Arc::clone(&counted);
            async move {
                if headers.get("metadata-flavor").and_then(|v| v.to_str().ok()) != Some("Google") {
                    return (StatusCode::FORBIDDEN, String::new());
                }
                counted.fetch_add(1, Ordering::SeqCst);
                (
                    StatusCode::OK,
                    r#"{"access_token":"token-1","expires_in":3599,"token_type":"Bearer"}"#
                        .to_string(),
                )
            }
        }),
    );
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap().to_string();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    (address, asked)
}

#[tokio::test]
async fn iam_sends_the_metadata_servers_token_over_tls() {
    let Some(url) = std::env::var(TLS_ENV).ok().filter(|url| !url.is_empty()) else {
        eprintln!("{TLS_ENV} is unset; skipping");
        return;
    };
    let ca = std::fs::read_to_string(std::env::var(TLS_CA_ENV).expect("the CA's path")).unwrap();
    let (metadata, asked) = metadata_server().await;
    let mut tls = connection(&unique("iam"), &url);
    tls.auth = MemoryStoreAuth::Iam;
    tls.ca = Some(ca);
    let store = open_with(&tls, RedisOptions::new().with_metadata_host(&metadata))
        .await
        .unwrap();
    assert!(store.available(), "the server took the token over TLS");
    assert!(asked.load(Ordering::SeqCst) >= 1);
    let key = Key::new("k", "k", Duration::from_secs(5));
    assert_eq!(key.increment(&store).await, Ok(1));
    let _subscription = Channel::signal("c", "c")
        .subscribe(&store, |_| {})
        .await
        .unwrap();
    store.close().await;

    // A wrong CA is no server the store trusts.
    let mut wrong = tls.clone();
    wrong.ca = Some(include_str!("testdata/memorystore/other-ca.pem").to_string());
    let store = open_with(&wrong, RedisOptions::new().with_metadata_host(&metadata))
        .await
        .unwrap();
    assert!(!store.available());
    store.close().await;
}
