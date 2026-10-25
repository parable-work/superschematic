//! A memory store on Redis or Valkey (D75), with the `memorystore-redis`
//! feature: [`RedisMemoryStore`], which a stack server, job or worker opens
//! once for each memory store its APIs list, from the store's connection
//! field ([`stackconfig::MemoryStore`]).
//!
//! It is the Go runtime's store in Rust:
//! - Opening never fails for a server that is down. It pings once within
//!   one second, and a background task pings every two seconds while the
//!   server is up, and reconnects with jittered backoff from 100ms to 5s
//!   while it is down. [`MemoryStore::available`] follows it, and every
//!   call returns [`MemoryStoreError::Unavailable`] at once while it is
//!   false.
//! - A command waits at most 500ms for its reply. A call that fails on the
//!   connection is tried once more on a new connection, which covers a
//!   stale one after a failover, and then marks the store down.
//! - No connection sends a `CLIENT` command: the client's `CLIENT SETINFO`
//!   is off, and no connection is named, since a managed server refuses
//!   both.
//! - A `rediss://` server is verified against the connection's CA (the
//!   system's roots when it has none), over TLS 1.2 or 1.3.
//! - With `auth: iam`, each new connection authenticates as user `default`
//!   with the workload's OAuth access token as its password, which the
//!   `gcs` feature's client asks the metadata server for and holds until
//!   shortly before it expires.
//! - The process has one subscriber connection per store, opened when the
//!   first subscription starts, which subscribes again to every channel it
//!   holds after it reconnects.
//! - No log line holds the URL, a token or a part's value: a line names
//!   the store, and a channel by its declaration.
//!
//! The commands: a publish is `PUBLISH`; an increment `INCR` then `PEXPIRE
//! <ttl> NX` in one `MULTI`; a count `GET`; a claim `SET <name> <token> PX
//! <ttl> NX GET`, whose random token lets a retried claim that the first
//! attempt made read back its own token and still win; a delete `DEL`.

use std::collections::HashMap;
use std::future::{poll_fn, Future};
use std::panic::AssertUnwindSafe;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, Weak};
use std::time::Duration;

use async_trait::async_trait;
use futures_core::Stream;
use redis::aio::{MultiplexedConnection, PubSubSink};
use redis::{
    AsyncConnectionConfig, Client, ConnectionAddr, ConnectionInfo, ErrorKind, IntoConnectionInfo,
    RedisError, RedisResult, TlsCertificates,
};
use ring::rand::{SecureRandom, SystemRandom};
use tokio::sync::{mpsc, Notify};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use super::{
    check_message, prefix, ttl_millis, DisabledMemoryStore, Handler, MemoryStore, MemoryStoreError,
    Ref, Subscription,
};
use crate::gcs::{GcsClient, GcsOptions};
use crate::stackconfig::{self, MemoryStoreAuth};

/// The durations that bound every call to the server. A test shortens
/// them.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Timings {
    /// One connection attempt, its authentication included.
    pub dial: Duration,
    /// One command's reply.
    pub op: Duration,
    /// One health ping.
    pub ping: Duration,
    /// Between pings while the server is up.
    pub health_interval: Duration,
    /// The first wait after a failure, and the cap it doubles to.
    pub min_backoff: Duration,
    pub max_backoff: Duration,
    /// How long the subscriber connection waits idle before it pings.
    pub receive_idle: Duration,
}

impl Default for Timings {
    fn default() -> Self {
        Self {
            dial: Duration::from_secs(1),
            op: Duration::from_millis(500),
            ping: Duration::from_secs(1),
            health_interval: Duration::from_secs(2),
            min_backoff: Duration::from_millis(100),
            max_backoff: Duration::from_secs(5),
            receive_idle: Duration::from_secs(15),
        }
    }
}

/// What [`open_with`] changes from [`open`]'s defaults.
#[derive(Clone, Debug, Default)]
pub struct RedisOptions {
    metadata_host: Option<String>,
    timings: Timings,
}

impl RedisOptions {
    pub fn new() -> Self {
        Self::default()
    }

    /// The metadata server's `host[:port]` an `auth: iam` store asks for
    /// its access token, in place of `GCE_METADATA_HOST` or Google's.
    #[must_use]
    pub fn with_metadata_host(mut self, host: impl Into<String>) -> Self {
        self.metadata_host = Some(host.into());
        self
    }

    /// The durations that bound each call, in place of the defaults.
    #[must_use]
    pub fn with_timings(mut self, timings: Timings) -> Self {
        self.timings = timings;
        self
    }
}

/// Opens the store `connection` names, as [`open_with`] does with the
/// defaults.
///
/// # Errors
///
/// [`MemoryStoreError::Config`] for a connection the store cannot use.
pub async fn open(
    connection: &stackconfig::MemoryStore,
) -> Result<RedisMemoryStore, MemoryStoreError> {
    open_with(connection, RedisOptions::new()).await
}

/// Opens the store `connection` names: it checks the connection, pings the
/// server once within one second, and returns, connected or not; a
/// background task reconnects. It must run on a tokio runtime.
///
/// # Errors
///
/// [`MemoryStoreError::Config`] for a connection the store cannot use: a
/// store or member that is no segment, a URL that is no `redis://` or
/// `rediss://` URL, a CA with `redis://` or one that holds no certificate,
/// or `auth: iam` with `redis://`. A server that does not answer is no
/// error.
pub async fn open_with(
    connection: &stackconfig::MemoryStore,
    options: RedisOptions,
) -> Result<RedisMemoryStore, MemoryStoreError> {
    let name = prefix(&connection.store, connection.member.as_deref())
        .map_err(|err| MemoryStoreError::Config(err.to_string()))?;
    let base = connection_info(connection)?;
    let auth = match connection.auth {
        MemoryStoreAuth::None => Auth::None,
        MemoryStoreAuth::Iam => {
            let mut gcs = GcsOptions::new();
            if let Some(host) = options.metadata_host {
                gcs = gcs.with_metadata_host(host);
            }
            let client = GcsClient::with_options(gcs).map_err(|err| {
                MemoryStoreError::Config(format!("the metadata server's client: {err}"))
            })?;
            Auth::Iam(client)
        }
    };
    let (commands, receiver) = mpsc::unbounded_channel();
    let inner = Arc::new(Inner {
        name,
        base,
        auth,
        url: connection.url.clone(),
        timings: options.timings,
        conn: Mutex::new(None),
        available: AtomicBool::new(false),
        closed: AtomicBool::new(false),
        wake: Notify::new(),
        stop: CancellationToken::new(),
        channels: Mutex::new(HashMap::new()),
        channels_changed: Notify::new(),
        commands,
        next_id: AtomicU64::new(1),
        tokens: Mutex::new(Vec::new()),
        tasks: Mutex::new(Vec::new()),
    });
    let up = match tokio::time::timeout(inner.timings.ping, inner.reconnect()).await {
        Ok(Ok(())) => true,
        Ok(Err(cause)) => {
            tracing::warn!(store = %inner.name, error = %inner.redact(&cause), "memory store unavailable at open; reconnecting in the background");
            false
        }
        Err(_) => {
            tracing::warn!(store = %inner.name, "memory store did not answer at open; reconnecting in the background");
            false
        }
    };
    inner.set_available(up, None);
    let watch = tokio::spawn(Arc::clone(&inner).watch(up));
    let subscriber = tokio::spawn(Arc::clone(&inner).run_subscriber(receiver));
    inner.lock_tasks().extend([watch, subscriber]);
    Ok(RedisMemoryStore { inner })
}

/// Opens the store of the API's field named `field`, the connection
/// `connection`: the Redis store when the field is set, and a
/// [`DisabledMemoryStore`] named after `service` when it is unset or
/// opening refuses it, which the error line names. What a generated
/// entrypoint opens each store with, once, for every API that lists it.
pub async fn open_or_disabled(
    service: &str,
    field: &str,
    connection: Option<&stackconfig::MemoryStore>,
) -> Arc<dyn MemoryStore> {
    let Some(connection) = connection else {
        tracing::info!(store = %service, field = %field, "memory store disabled: its field is unset");
        return Arc::new(DisabledMemoryStore::new(service));
    };
    match open(connection).await {
        Ok(store) => Arc::new(store),
        Err(err) => {
            tracing::error!(store = %service, field = %field, error = %err, "memory store disabled: its connection is refused");
            Arc::new(DisabledMemoryStore::new(service))
        }
    }
}

/// The client's connection info from the field, without a password: no
/// `CLIENT SETINFO`, and with TLS the connection's CA.
fn connection_info(
    connection: &stackconfig::MemoryStore,
) -> Result<ConnectionInfo, MemoryStoreError> {
    let refused = |why: &str| MemoryStoreError::Config(why.to_string());
    let tls = connection.url.starts_with("rediss://");
    if !tls && !connection.url.starts_with("redis://") {
        return Err(refused("the URL is no redis:// or rediss:// URL"));
    }
    if !tls && connection.ca.is_some() {
        return Err(refused(
            "a CA is set with a redis:// URL; a CA verifies a rediss:// server",
        ));
    }
    if !tls && connection.auth == MemoryStoreAuth::Iam {
        return Err(refused(
            "auth iam with a redis:// URL; an access token travels only over rediss://",
        ));
    }
    let info = connection
        .url
        .as_str()
        .into_connection_info()
        .map_err(|_| refused("the URL is not a Redis URL"))?;
    if matches!(info.addr(), ConnectionAddr::TcpTls { insecure: true, .. }) {
        return Err(refused(
            "the URL asks for no verification; give the CA instead",
        ));
    }
    let redis = info.redis_settings().clone().set_skip_set_lib_name();
    let info = info.set_redis_settings(redis);
    if !tls {
        return Ok(info);
    }
    let certificates = TlsCertificates {
        client_tls: None,
        root_cert: connection.ca.as_ref().map(|ca| ca.as_bytes().to_vec()),
    };
    // rustls's provider, ring, whichever other provider a crate of the
    // build turns on.
    let _ = rustls::crypto::ring::default_provider().install_default();
    let client = Client::build_with_tls(info, certificates)
        .map_err(|_| refused("the CA holds no PEM certificate the client can use"))?;
    Ok(client.get_connection_info().clone())
}

/// A memory store on Redis or Valkey. Dropping it, or closing it, stops
/// its background tasks and connections.
pub struct RedisMemoryStore {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for RedisMemoryStore {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RedisMemoryStore")
            .field("name", &self.inner.name)
            .field("available", &self.inner.is_available())
            .finish_non_exhaustive()
    }
}

impl Drop for RedisMemoryStore {
    fn drop(&mut self) {
        self.inner.shut();
    }
}

enum Auth {
    None,
    Iam(GcsClient),
}

enum Command {
    Subscribe(String),
    Unsubscribe(String),
}

struct Inner {
    name: String,
    base: ConnectionInfo,
    auth: Auth,
    url: String,
    timings: Timings,
    /// The connection commands share, `None` while the server is down.
    conn: Mutex<Option<MultiplexedConnection>>,
    available: AtomicBool,
    closed: AtomicBool,
    /// Wakes the health task after a call failed on the connection.
    wake: Notify,
    stop: CancellationToken,
    /// Each channel's handlers, by full name, which the subscriber
    /// connection subscribes to.
    channels: Mutex<HashMap<String, Vec<(u64, Handler)>>>,
    channels_changed: Notify,
    commands: mpsc::UnboundedSender<Command>,
    next_id: AtomicU64,
    /// The access tokens the store sent, which no log line holds.
    tokens: Mutex<Vec<String>>,
    tasks: Mutex<Vec<JoinHandle<()>>>,
}

fn locked<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

/// Whether `err` is the server's reply, such as an out-of-memory refusal,
/// which leaves the connection up, rather than the connection's failure.
fn is_reply(err: &RedisError) -> bool {
    matches!(
        err.kind(),
        ErrorKind::Server(_) | ErrorKind::Extension | ErrorKind::UnexpectedReturnType
    )
}

/// `d` spread by up to 20% either way, so replicas do not retry in step.
fn jitter(d: Duration) -> Duration {
    let spread = d / 5;
    let mut bytes = [0u8; 4];
    if spread.is_zero() || SystemRandom::new().fill(&mut bytes).is_err() {
        return d;
    }
    let fraction = f64::from(u32::from_le_bytes(bytes)) / f64::from(u32::MAX);
    d - spread + spread.mul_f64(2.0 * fraction)
}

impl Inner {
    fn is_available(&self) -> bool {
        !self.closed.load(Ordering::Acquire) && self.available.load(Ordering::Acquire)
    }

    fn lock_tasks(&self) -> MutexGuard<'_, Vec<JoinHandle<()>>> {
        locked(&self.tasks)
    }

    fn full_name(&self, reference: &Ref) -> String {
        format!("{}:{}", self.name, reference.name)
    }

    /// `text` without the URL or a token the store sent.
    fn redact(&self, text: &str) -> String {
        let mut out = text.replace(&self.url, "[redacted]");
        for token in locked(&self.tokens).iter() {
            out = out.replace(token.as_str(), "[redacted]");
        }
        out
    }

    /// A client for one new connection: with `auth: iam`, the account's
    /// access token as user `default`'s password.
    async fn client(&self) -> Result<Client, String> {
        let mut redis = self.base.redis_settings().clone();
        if let Auth::Iam(gcs) = &self.auth {
            let token = gcs.access_token().await?;
            {
                let mut tokens = locked(&self.tokens);
                if !tokens.contains(&token) {
                    tokens.push(token.clone());
                }
            }
            redis = redis.set_username("default").set_password(token);
        }
        Client::open(self.base.clone().set_redis_settings(redis)).map_err(|err| err.to_string())
    }

    /// A new connection for commands, which has answered a ping.
    async fn connect(&self) -> Result<MultiplexedConnection, String> {
        let t = self.timings;
        let attempt = async {
            let client = self.client().await?;
            let config = AsyncConnectionConfig::new()
                .set_connection_timeout(Some(t.dial))
                .set_response_timeout(Some(t.op));
            let mut conn = client
                .get_multiplexed_async_connection_with_config(&config)
                .await
                .map_err(|err| err.to_string())?;
            redis::cmd("PING")
                .query_async::<String>(&mut conn)
                .await
                .map_err(|err| err.to_string())?;
            Ok(conn)
        };
        match tokio::time::timeout(t.dial + t.ping, attempt).await {
            Ok(result) => result,
            Err(_) => Err("the server did not answer in time".to_string()),
        }
    }

    /// Replaces the command connection with a new one.
    async fn reconnect(&self) -> Result<(), String> {
        let conn = self.connect().await?;
        *locked(&self.conn) = Some(conn);
        Ok(())
    }

    /// Pings the command connection, or opens a new one when it has none
    /// or the ping fails.
    async fn check(&self) -> Result<(), String> {
        let existing = locked(&self.conn).clone();
        if let Some(mut conn) = existing {
            let ping = redis::cmd("PING");
            let ping = ping.query_async::<String>(&mut conn);
            if let Ok(Ok(_)) = tokio::time::timeout(self.timings.ping, ping).await {
                return Ok(());
            }
            *locked(&self.conn) = None;
        }
        self.reconnect().await
    }

    /// Records the connection's state and logs each change.
    fn set_available(&self, up: bool, cause: Option<&str>) {
        let was = self.available.swap(up, Ordering::AcqRel);
        match (up, was) {
            (true, false) => tracing::info!(store = %self.name, "memory store connected"),
            (false, true) => {
                tracing::warn!(store = %self.name, error = %self.redact(cause.unwrap_or("")), "memory store unavailable")
            }
            _ => {}
        }
    }

    /// Records a failed call and has the health task try at once.
    fn mark_down(&self, cause: &str) {
        *locked(&self.conn) = None;
        if self.available.load(Ordering::Acquire) {
            self.set_available(false, Some(cause));
        }
        self.wake.notify_one();
    }

    /// Pings while the server is up, and reconnects with backoff while it
    /// is down, until the store stops.
    async fn watch(self: Arc<Self>, up: bool) {
        let t = self.timings;
        let mut backoff = t.min_backoff;
        let mut wait = if up {
            t.health_interval
        } else {
            jitter(backoff)
        };
        loop {
            tokio::select! {
                () = self.stop.cancelled() => return,
                () = self.wake.notified() => {}
                () = tokio::time::sleep(wait) => {}
            }
            let result = self.check().await;
            if self.stop.is_cancelled() {
                return;
            }
            match result {
                Ok(()) => {
                    self.set_available(true, None);
                    backoff = t.min_backoff;
                    wait = t.health_interval;
                }
                Err(cause) => {
                    self.set_available(false, Some(&cause));
                    wait = jitter(backoff);
                    backoff = (backoff * 2).min(t.max_backoff);
                }
            }
        }
    }

    /// Runs `op` on the command connection, within the op timeout, and
    /// once more on a new connection when it fails on the connection.
    async fn run<T, F, Fut>(&self, op: F) -> Result<T, MemoryStoreError>
    where
        F: Fn(MultiplexedConnection) -> Fut,
        Fut: Future<Output = RedisResult<T>>,
    {
        if !self.is_available() {
            return Err(MemoryStoreError::Unavailable);
        }
        let Some(conn) = locked(&self.conn).clone() else {
            return Err(MemoryStoreError::Unavailable);
        };
        let t = self.timings;
        let cause = match tokio::time::timeout(t.op, op(conn)).await {
            Ok(Ok(value)) => return Ok(value),
            Ok(Err(err)) if is_reply(&err) => {
                return Err(MemoryStoreError::Failed(self.redact(&err.to_string())))
            }
            Ok(Err(err)) => err.to_string(),
            Err(_) => "no reply in time".to_string(),
        };
        // One retry, on a new connection.
        let retried = match tokio::time::timeout(t.dial, self.connect()).await {
            Ok(Ok(conn)) => {
                *locked(&self.conn) = Some(conn.clone());
                match tokio::time::timeout(t.op, op(conn)).await {
                    Ok(Ok(value)) => return Ok(value),
                    Ok(Err(err)) if is_reply(&err) => {
                        return Err(MemoryStoreError::Failed(self.redact(&err.to_string())))
                    }
                    Ok(Err(err)) => err.to_string(),
                    Err(_) => "no reply in time".to_string(),
                }
            }
            Ok(Err(err)) => err,
            Err(_) => "the server did not answer in time".to_string(),
        };
        tracing::debug!(store = %self.name, error = %self.redact(&cause), retry = %self.redact(&retried), "memory store call failed on the connection");
        self.mark_down(&retried);
        Err(MemoryStoreError::Unavailable)
    }

    /// Stops the background tasks and drops every connection.
    fn shut(&self) {
        self.closed.store(true, Ordering::Release);
        self.available.store(false, Ordering::Release);
        self.stop.cancel();
        *locked(&self.conn) = None;
        locked(&self.channels).clear();
    }

    /// Calls the handlers of the channel a message came on.
    fn dispatch(&self, channel: &str, payload: &[u8]) {
        let handlers: Vec<Handler> = locked(&self.channels)
            .get(channel)
            .map(|handlers| handlers.iter().map(|(_, h)| Arc::clone(h)).collect())
            .unwrap_or_default();
        for handler in handlers {
            if std::panic::catch_unwind(AssertUnwindSafe(|| handler(payload))).is_err() {
                tracing::error!(store = %self.name, "memory store handler panicked; the message is dropped");
            }
        }
    }

    /// The channels the subscriber connection holds.
    fn channel_names(&self) -> Vec<String> {
        locked(&self.channels).keys().cloned().collect()
    }

    /// Holds one subscriber connection while any channel has a handler,
    /// subscribing to each channel, and reconnects with backoff when it
    /// fails, until the store stops.
    async fn run_subscriber(self: Arc<Self>, mut commands: mpsc::UnboundedReceiver<Command>) {
        let t = self.timings;
        let mut backoff = t.min_backoff;
        loop {
            // Wait for a channel.
            while locked(&self.channels).is_empty() {
                tokio::select! {
                    () = self.stop.cancelled() => return,
                    () = self.channels_changed.notified() => {}
                    command = commands.recv() => if command.is_none() { return },
                }
            }
            let failure = match self.subscribe_connection(&mut commands).await {
                None => return,
                Some(failure) => failure,
            };
            tracing::debug!(store = %self.name, error = %self.redact(&failure), "memory store subscriber connection failed; reconnecting");
            tokio::select! {
                () = self.stop.cancelled() => return,
                () = tokio::time::sleep(jitter(backoff)) => {}
            }
            backoff = (backoff * 2).min(t.max_backoff);
            if failure == RECEIVED {
                backoff = t.min_backoff;
            }
        }
    }

    /// Opens the subscriber connection and serves it until it fails, whose
    /// cause it returns, or the store stops, `None`.
    async fn subscribe_connection(
        &self,
        commands: &mut mpsc::UnboundedReceiver<Command>,
    ) -> Option<String> {
        let t = self.timings;
        let open = async {
            let client = self.client().await?;
            client
                .get_async_pubsub()
                .await
                .map_err(|err| err.to_string())
        };
        let pubsub = tokio::select! {
            () = self.stop.cancelled() => return None,
            opened = tokio::time::timeout(t.dial + t.op, open) => match opened {
                Ok(Ok(pubsub)) => pubsub,
                Ok(Err(err)) => return Some(err),
                Err(_) => return Some("the server did not answer in time".to_string()),
            },
        };
        let (mut sink, mut stream) = pubsub.split();
        // The map is what to subscribe to: commands sent before now are
        // stale.
        while commands.try_recv().is_ok() {}
        let names = self.channel_names();
        if !names.is_empty() {
            if let Err(err) = within(t.op, sink.subscribe(&names)).await {
                return Some(err);
            }
        }
        let mut received = false;
        loop {
            tokio::select! {
                () = self.stop.cancelled() => return None,
                message = poll_fn(|cx| std::pin::Pin::new(&mut stream).poll_next(cx)) => match message {
                    Some(message) => {
                        received = true;
                        self.dispatch(message.get_channel_name(), message.get_payload_bytes());
                    }
                    None => return Some(if received { RECEIVED.to_string() } else { "the connection closed".to_string() }),
                },
                command = commands.recv() => match command {
                    None => return None,
                    Some(command) => if let Err(err) = self.apply(&mut sink, command).await {
                        return Some(err);
                    },
                },
                () = tokio::time::sleep(t.receive_idle) => {
                    if let Err(err) = within(t.ping, sink.ping::<redis::Value>()).await {
                        return Some(err);
                    }
                }
            }
        }
    }

    /// Subscribes to or unsubscribes from a channel, when the map still
    /// says to.
    async fn apply(&self, sink: &mut PubSubSink, command: Command) -> Result<(), String> {
        let t = self.timings;
        match command {
            Command::Subscribe(name) if locked(&self.channels).contains_key(&name) => {
                within(t.op, sink.subscribe(&name)).await
            }
            Command::Unsubscribe(name) if !locked(&self.channels).contains_key(&name) => {
                within(t.op, sink.unsubscribe(&name)).await
            }
            _ => Ok(()),
        }
    }
}

/// The cause a subscriber connection that had received a message closed
/// with: its next connection waits the shortest backoff.
const RECEIVED: &str = "the connection closed after it received";

/// `future`'s result within `limit`, its error as text.
async fn within<T>(
    limit: Duration,
    future: impl Future<Output = RedisResult<T>>,
) -> Result<(), String> {
    match tokio::time::timeout(limit, future).await {
        Ok(Ok(_)) => Ok(()),
        Ok(Err(err)) => Err(err.to_string()),
        Err(_) => Err("no reply in time".to_string()),
    }
}

/// A random token a claim sets its key to.
fn claim_token() -> String {
    let mut bytes = [0u8; 16];
    let _ = SystemRandom::new().fill(&mut bytes);
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[async_trait]
impl MemoryStore for RedisMemoryStore {
    fn name(&self) -> &str {
        &self.inner.name
    }

    fn available(&self) -> bool {
        self.inner.is_available()
    }

    async fn publish(&self, channel: &Ref, data: &[u8]) -> Result<(), MemoryStoreError> {
        if !self.inner.is_available() {
            return Err(MemoryStoreError::Unavailable);
        }
        check_message(data)?;
        let name = self.inner.full_name(channel);
        let data = data.to_vec();
        self.inner
            .run(|mut conn| {
                let (name, data) = (name.clone(), data.clone());
                async move {
                    redis::cmd("PUBLISH")
                        .arg(name)
                        .arg(data)
                        .query_async::<i64>(&mut conn)
                        .await
                }
            })
            .await
            .map(|_| ())
    }

    async fn subscribe(
        &self,
        channel: &Ref,
        handler: Handler,
    ) -> Result<Subscription, MemoryStoreError> {
        let inner = &self.inner;
        if inner.closed.load(Ordering::Acquire) {
            return Err(MemoryStoreError::Unavailable);
        }
        let full = inner.full_name(channel);
        let id = inner.next_id.fetch_add(1, Ordering::Relaxed);
        {
            let mut channels = locked(&inner.channels);
            let handlers = channels.entry(full.clone()).or_default();
            handlers.push((id, handler));
            if handlers.len() == 1 {
                let _ = inner.commands.send(Command::Subscribe(full.clone()));
            }
        }
        inner.channels_changed.notify_one();
        let weak: Weak<Inner> = Arc::downgrade(inner);
        Ok(Subscription::new(move || {
            let Some(inner) = weak.upgrade() else {
                return;
            };
            let mut channels = locked(&inner.channels);
            if let Some(handlers) = channels.get_mut(&full) {
                handlers.retain(|(other, _)| *other != id);
                if handlers.is_empty() {
                    channels.remove(&full);
                    let _ = inner.commands.send(Command::Unsubscribe(full));
                }
            }
        }))
    }

    async fn increment(&self, key: &Ref, ttl: Duration) -> Result<i64, MemoryStoreError> {
        let ms = ttl_millis(ttl)?;
        let name = self.inner.full_name(key);
        self.inner
            .run(|mut conn| {
                let name = name.clone();
                async move {
                    // NX gives the TTL only to a key that has none: the
                    // one this INCR created.
                    let (count,): (i64,) = redis::pipe()
                        .atomic()
                        .cmd("INCR")
                        .arg(&name)
                        .cmd("PEXPIRE")
                        .arg(&name)
                        .arg(ms)
                        .arg("NX")
                        .ignore()
                        .query_async(&mut conn)
                        .await?;
                    Ok(count)
                }
            })
            .await
    }

    async fn count(&self, key: &Ref) -> Result<i64, MemoryStoreError> {
        let name = self.inner.full_name(key);
        let value = self
            .inner
            .run(|mut conn| {
                let name = name.clone();
                async move {
                    redis::cmd("GET")
                        .arg(name)
                        .query_async::<Option<String>>(&mut conn)
                        .await
                }
            })
            .await?;
        match value {
            None => Ok(0),
            Some(text) => text.parse().map_err(|_| {
                MemoryStoreError::Failed(format!("a {} key holds no count", key.declaration))
            }),
        }
    }

    async fn claim(&self, key: &Ref, ttl: Duration) -> Result<bool, MemoryStoreError> {
        let ms = ttl_millis(ttl)?;
        let name = self.inner.full_name(key);
        let token = claim_token();
        let previous = self
            .inner
            .run(|mut conn| {
                let (name, token) = (name.clone(), token.clone());
                async move {
                    redis::cmd("SET")
                        .arg(name)
                        .arg(token)
                        .arg("PX")
                        .arg(ms)
                        .arg("NX")
                        .arg("GET")
                        .query_async::<Option<String>>(&mut conn)
                        .await
                }
            })
            .await?;
        // No key existed, so this call set it; or a retry reads back the
        // token the first attempt set.
        Ok(previous.is_none_or(|previous| previous == token))
    }

    async fn delete(&self, key: &Ref) -> Result<(), MemoryStoreError> {
        let name = self.inner.full_name(key);
        self.inner
            .run(|mut conn| {
                let name = name.clone();
                async move {
                    redis::cmd("DEL")
                        .arg(name)
                        .query_async::<i64>(&mut conn)
                        .await
                }
            })
            .await
            .map(|_| ())
    }

    async fn close(&self) {
        self.inner.shut();
        let tasks: Vec<JoinHandle<()>> = self.inner.lock_tasks().drain(..).collect();
        for task in tasks {
            let _ = tokio::time::timeout(Duration::from_secs(1), task).await;
        }
    }
}
