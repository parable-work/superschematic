//! The memory store (D75): a Redis or Valkey server that the replicas of a
//! stack's servers, jobs and workers share, and whose loss costs a sample,
//! never a record. The twin of the Go runtime's `memorystore` package and
//! of the TypeScript runtime's `MemoryStore`.
//!
//! An API lists the stores it uses in its config's `memoryStores`, and the
//! generated `Deps` holds an `Arc<dyn MemoryStore>` per listed store. The
//! generated entrypoint opens each store once: the `memorystore-redis`
//! feature's [`redis`] client when the store's field is set, else a
//! [`DisabledMemoryStore`]. Nothing in this module needs a feature, so an
//! API crate that names it builds without a Redis client.
//!
//! A caller never builds a name. The store's schema declares each channel
//! and key, and its generated definitions crate gives one value per
//! declaration whose `for_` takes the parts in order and returns the
//! typed [`Channel`] or [`Key`] here:
//!
//! ```ignore
//! let changed = shop_cache::STOCK_CHANGED.for_(&product_id)?;
//! changed.publish(&*deps.shop_cache, &Signal).await?;
//! ```
//!
//! Three rules hold:
//! - The store holds no fact. A channel carries a wake-up or a sample a
//!   replica can lose, and every key expires.
//! - A disabled or unreachable store refuses every call at once with
//!   [`MemoryStoreError::Unavailable`], and the caller takes the path it had
//!   before the store existed.
//! - No log line holds the server's URL, a token or a part's value.
//!
//! A full name is `<prefix>:<name>`: the prefix is the store's name, or
//! `<store>@<member>` in a member of a parameterized environment, and a
//! [`Ref`]'s name is the declaration then `:<part>` for each part. Every
//! segment is checked ([`check_segment`]) and refused, never escaped.

use std::collections::HashMap;
use std::fmt;
use std::marker::PhantomData;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Deserializer, Serialize, Serializer};

#[cfg(feature = "memorystore-redis")]
pub mod redis;
#[cfg(test)]
mod tests;

/// The most bytes one segment holds: a store's name, a member's, a
/// declaration's and each part's value.
pub const SEGMENT_MAX_BYTES: usize = 128;

/// The most bytes one encoded message holds: 64 KiB. A larger message is
/// refused before it is sent.
pub const MAX_MESSAGE_BYTES: usize = 64 * 1024;

/// The longest TTL a key may have: seven days.
pub const MAX_TTL: Duration = Duration::from_secs(7 * 24 * 60 * 60);

/// The reserved declaration the core makes in every MemoryStore schema:
/// the counters of the shared rate limit of each API whose
/// `rateLimitStore` the store is.
pub const RATE_LIMIT_DECLARATION: &str = "rate-limit";

/// What [`RATE_LIMIT_DECLARATION`]'s counter TTL adds to two windows.
pub const RATE_LIMIT_SLACK: Duration = Duration::from_secs(5);

/// Why a memory store refused a call.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum MemoryStoreError {
    /// The store is disabled, closed or not connected, or the call failed
    /// on the connection. It is returned at once while the store is
    /// unavailable; the caller takes its fallback path.
    Unavailable,
    /// A segment of a name is refused: empty, too long, or holding a
    /// separator, a glob character, `@`, `\` or a control byte. The
    /// message names the declaration and the part's position, never its
    /// value.
    Name(String),
    /// A key's TTL is under one millisecond or over its maximum.
    Ttl(String),
    /// A message is over [`MAX_MESSAGE_BYTES`] encoded: its size.
    TooLarge(usize),
    /// A message could not be encoded.
    Encode(String),
    /// The server refused the command with a reply, such as an
    /// out-of-memory refusal. The store stays available.
    Failed(String),
    /// A connection the store cannot use: opening refuses it, and the
    /// entrypoint runs the store disabled. The message holds no URL.
    Config(String),
}

impl fmt::Display for MemoryStoreError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unavailable => f.write_str("memory store unavailable"),
            Self::Name(message) => write!(f, "memory store name: {message}"),
            Self::Ttl(message) => write!(f, "memory store TTL: {message}"),
            Self::TooLarge(bytes) => write!(
                f,
                "memory store message is {bytes} bytes, over the {MAX_MESSAGE_BYTES} byte limit"
            ),
            Self::Encode(message) => write!(f, "memory store message: {message}"),
            Self::Failed(message) => write!(f, "memory store: {message}"),
            Self::Config(message) => write!(f, "memory store connection: {message}"),
        }
    }
}

impl std::error::Error for MemoryStoreError {}

impl MemoryStoreError {
    /// Whether the error is [`MemoryStoreError::Unavailable`].
    pub fn is_unavailable(&self) -> bool {
        matches!(self, Self::Unavailable)
    }
}

/// A channel or a key in a store: its declaration's name, which metrics
/// and logs carry, and its name without the store's prefix
/// (`stock-changed:<part>`), which the store prepends `<prefix>:` to.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Ref {
    pub declaration: String,
    pub name: String,
}

impl Ref {
    pub fn new(declaration: impl Into<String>, name: impl Into<String>) -> Self {
        Self {
            declaration: declaration.into(),
            name: name.into(),
        }
    }
}

/// What a subscription calls with each message's bytes. It runs on the
/// store's one receiving task, in order: it returns quickly, and hands
/// slow work to a task of its own.
pub type Handler = Arc<dyn Fn(&[u8]) + Send + Sync>;

/// A subscription to a channel. Dropping it, or [`Subscription::unsubscribe`],
/// stops its handler; the store unsubscribes from the channel when its last
/// subscription stops.
#[must_use = "a subscription stops when it is dropped"]
pub struct Subscription {
    cancel: Option<Box<dyn FnOnce() + Send + Sync>>,
}

impl Subscription {
    /// A subscription that runs `cancel` once, when it stops.
    pub fn new(cancel: impl FnOnce() + Send + Sync + 'static) -> Self {
        Self {
            cancel: Some(Box::new(cancel)),
        }
    }

    /// Stops the handler.
    pub fn unsubscribe(mut self) {
        self.stop();
    }

    fn stop(&mut self) {
        if let Some(cancel) = self.cancel.take() {
            cancel();
        }
    }
}

impl Drop for Subscription {
    fn drop(&mut self) {
        self.stop();
    }
}

impl fmt::Debug for Subscription {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Subscription")
            .field("active", &self.cancel.is_some())
            .finish()
    }
}

/// One memory store. Every call returns [`MemoryStoreError::Unavailable`]
/// at once while the store is disabled, closed or not connected.
#[async_trait]
pub trait MemoryStore: Send + Sync {
    /// The store's prefix: its name, or `<store>@<member>`.
    fn name(&self) -> &str;

    /// Whether the store is connected. False for a disabled or closed
    /// store.
    fn available(&self) -> bool;

    /// Publishes `data` on the channel `channel`. A message over
    /// [`MAX_MESSAGE_BYTES`] is refused before it is sent.
    async fn publish(&self, channel: &Ref, data: &[u8]) -> Result<(), MemoryStoreError>;

    /// Calls `handler` with each message published on `channel` until the
    /// subscription stops. While the server is down the store holds the
    /// subscription and subscribes again when it reconnects; a message
    /// published meanwhile is lost.
    async fn subscribe(
        &self,
        channel: &Ref,
        handler: Handler,
    ) -> Result<Subscription, MemoryStoreError>;

    /// Adds one to the counter `key` and returns its new count. The
    /// increment that creates the key gives it `ttl`; later ones keep it.
    async fn increment(&self, key: &Ref, ttl: Duration) -> Result<i64, MemoryStoreError>;

    /// The counter `key`'s count, 0 when it is absent.
    async fn count(&self, key: &Ref) -> Result<i64, MemoryStoreError>;

    /// Sets `key` with `ttl` if it is absent, and reports whether this call
    /// set it: of the calls that claim one key before it expires, one gets
    /// true.
    async fn claim(&self, key: &Ref, ttl: Duration) -> Result<bool, MemoryStoreError>;

    /// Removes `key`: a count starts again from zero, and the next claim
    /// gets true. Removing an absent key is no error.
    async fn delete(&self, key: &Ref) -> Result<(), MemoryStoreError>;

    /// Stops the store: every later call is unavailable. Calling it again
    /// does nothing.
    async fn close(&self);
}

/// Checks one segment of a full name: 1 to [`SEGMENT_MAX_BYTES`] bytes,
/// with no `:`, glob character (`*`, `?`, `[`, `]`), `@`, `\` or control
/// byte (below 0x20, or 0x7f). The message never holds the segment.
pub fn check_segment(segment: &str) -> Result<(), String> {
    if segment.is_empty() {
        return Err(format!(
            "a segment is empty; it holds 1 to {SEGMENT_MAX_BYTES} bytes"
        ));
    }
    if segment.len() > SEGMENT_MAX_BYTES {
        return Err(format!(
            "a segment is {} bytes; it holds at most {SEGMENT_MAX_BYTES}",
            segment.len()
        ));
    }
    for (i, byte) in segment.bytes().enumerate() {
        if byte < 0x20 || byte == 0x7f {
            return Err(format!(
                "a segment holds the control byte 0x{byte:02x} at {i}"
            ));
        }
        if b":*?[]@\\".contains(&byte) {
            return Err(format!(
                "a segment holds {:?} at {i}; a segment has no ':', '*', '?', '[', ']', '@' or '\\'",
                char::from(byte)
            ));
        }
    }
    Ok(())
}

/// Joins a declaration's name and its parts' wire strings into a name
/// without the store's prefix (`stock-changed:<part>`), checking each as a
/// segment.
///
/// # Errors
///
/// [`MemoryStoreError::Name`] for a segment [`check_segment`] refuses.
pub fn name(declaration: &str, parts: &[&str]) -> Result<String, MemoryStoreError> {
    check_segment(declaration)
        .map_err(|err| MemoryStoreError::Name(format!("declaration {declaration:?}: {err}")))?;
    let mut out = String::with_capacity(
        declaration.len() + parts.iter().map(|part| part.len() + 1).sum::<usize>(),
    );
    out.push_str(declaration);
    for (i, part) in parts.iter().enumerate() {
        check_segment(part).map_err(|err| {
            MemoryStoreError::Name(format!("declaration {declaration} part {i}: {err}"))
        })?;
        out.push(':');
        out.push_str(part);
    }
    Ok(out)
}

/// A store's prefix: its name, then `@` and the member's name in a member
/// of a parameterized environment.
///
/// # Errors
///
/// [`MemoryStoreError::Name`] for a store or a member that is no segment.
pub fn prefix(store: &str, member: Option<&str>) -> Result<String, MemoryStoreError> {
    check_segment(store).map_err(|err| MemoryStoreError::Name(format!("store: {err}")))?;
    match member.filter(|member| !member.is_empty()) {
        None => Ok(store.to_string()),
        Some(member) => {
            check_segment(member)
                .map_err(|err| MemoryStoreError::Name(format!("member: {err}")))?;
            Ok(format!("{store}@{member}"))
        }
    }
}

/// A part's wire string: the value as its JSON encoding writes it, a
/// string as it is, an integer in base 10 and a boolean `true` or `false`.
/// A scalar is written in its canonical form (a UUID in its wire form), an
/// enum as its member's wire value.
///
/// # Errors
///
/// [`MemoryStoreError::Name`] for a value whose encoding is no string,
/// integer or boolean.
pub fn part<T: Serialize + ?Sized>(value: &T) -> Result<String, MemoryStoreError> {
    match serde_json::to_value(value) {
        Ok(serde_json::Value::String(text)) => Ok(text),
        Ok(serde_json::Value::Bool(flag)) => Ok(flag.to_string()),
        Ok(serde_json::Value::Number(number)) if number.is_i64() || number.is_u64() => {
            Ok(number.to_string())
        }
        Ok(_) => Err(MemoryStoreError::Name(
            "a part's value encodes as no string, integer or boolean".to_string(),
        )),
        Err(err) => Err(MemoryStoreError::Name(format!(
            "a part's value does not encode: {err}"
        ))),
    }
}

/// The TTL a caller gives one key of a declaration with `maxTtlSeconds`,
/// checked: at least one millisecond and at most `max`, in whole
/// milliseconds.
///
/// # Errors
///
/// [`MemoryStoreError::Ttl`] for a TTL out of range.
pub fn key_ttl(
    declaration: &str,
    ttl: Duration,
    max: Duration,
) -> Result<Duration, MemoryStoreError> {
    if ttl < Duration::from_millis(1) || ttl > max {
        return Err(MemoryStoreError::Ttl(format!(
            "key {declaration}: the TTL {ttl:?} is not between 1ms and the declared maximum {max:?}"
        )));
    }
    Ok(Duration::from_millis(
        u64::try_from(ttl.as_millis()).unwrap_or(u64::MAX),
    ))
}

/// A TTL in whole milliseconds, refused under one millisecond or over
/// [`MAX_TTL`]: what every store checks before it writes a key.
pub(crate) fn ttl_millis(ttl: Duration) -> Result<u64, MemoryStoreError> {
    if ttl < Duration::from_millis(1) || ttl > MAX_TTL {
        return Err(MemoryStoreError::Ttl(format!(
            "the TTL {ttl:?} is not between 1ms and {MAX_TTL:?}"
        )));
    }
    Ok(u64::try_from(ttl.as_millis()).unwrap_or(u64::MAX))
}

/// Refuses a message over [`MAX_MESSAGE_BYTES`].
pub(crate) fn check_message(data: &[u8]) -> Result<(), MemoryStoreError> {
    if data.len() > MAX_MESSAGE_BYTES {
        return Err(MemoryStoreError::TooLarge(data.len()));
    }
    Ok(())
}

// --- The rate limit's rule (D75) ---------------------------------------

/// What the shared rate limit decides for one request: httprate's sliding
/// window, over the counts of the window the request falls in and the one
/// before it.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct WindowDecision {
    /// Whether the request is allowed.
    pub allowed: bool,
    /// The estimate of the requests in the sliding window.
    pub estimate: f64,
    /// The start of the request's window, and of the one before it, in
    /// Unix seconds.
    pub curr_window_start: i64,
    pub prev_window_start: i64,
    /// The TTL the increment of the current window's counter gives it:
    /// two windows and [`RATE_LIMIT_SLACK`], `None` when refused.
    pub increment_ttl: Option<Duration>,
    /// Retry-After of a refusal: the window's length in whole seconds, at
    /// least 1; `None` when allowed.
    pub retry_after_seconds: Option<u64>,
}

/// httprate's rule for a request at `now_ms` (Unix milliseconds), with
/// windows of `window_ms` and `limit` requests: `curr = floor(now / W) * W`,
/// `prev = curr - W`, `elapsed = now - curr`, and
/// `estimate = prev_count * (W - elapsed) / W + curr_count` in doubles.
/// The request is allowed when `round(estimate) + 1 <= limit`, rounding
/// half away from zero.
pub fn window_decision(
    limit: i64,
    window_ms: i64,
    now_ms: i64,
    prev_count: i64,
    curr_count: i64,
) -> WindowDecision {
    let window = window_ms.max(1);
    let curr = now_ms.div_euclid(window) * window;
    let prev = curr - window;
    let elapsed = now_ms - curr;
    #[allow(clippy::cast_precision_loss)]
    let estimate =
        prev_count as f64 * (window - elapsed) as f64 / window as f64 + curr_count as f64;
    #[allow(clippy::cast_precision_loss)]
    let allowed = estimate.round() + 1.0 <= limit as f64;
    let window_ttl = Duration::from_millis(u64::try_from(window).unwrap_or(0));
    WindowDecision {
        allowed,
        estimate,
        curr_window_start: curr.div_euclid(1000),
        prev_window_start: prev.div_euclid(1000),
        increment_ttl: allowed.then(|| window_ttl * 2 + RATE_LIMIT_SLACK),
        retry_after_seconds: (!allowed).then(|| u64::try_from(window / 1000).unwrap_or(0).max(1)),
    }
}

/// The name, without the store's prefix, of the reserved rate-limit
/// counter of `service`'s `operation` for the window that starts at
/// `window_start` (Unix seconds) and the client `client_key`:
/// `rate-limit:<service>:<operation>:<window_start>:<hex SHA-256 of the key>`,
/// so the client's address is no part of a name.
///
/// # Errors
///
/// [`MemoryStoreError::Name`] for a service or an operation that is no
/// segment.
pub fn rate_limit_name(
    service: &str,
    operation: &str,
    window_start: i64,
    client_key: &str,
) -> Result<String, MemoryStoreError> {
    let digest = ring::digest::digest(&ring::digest::SHA256, client_key.as_bytes());
    let mut client = String::with_capacity(64);
    for byte in digest.as_ref() {
        use std::fmt::Write as _;
        let _ = write!(client, "{byte:02x}");
    }
    name(
        RATE_LIMIT_DECLARATION,
        &[service, operation, &window_start.to_string(), &client],
    )
}

// --- The typed layer ---------------------------------------------------

/// The message of a channel declared without a payload: the channel's
/// name is the whole message. It encodes as `{}`, and any message decodes
/// to it.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Signal;

impl Serialize for Signal {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap as _;
        serializer.serialize_map(Some(0))?.end()
    }
}

impl<'de> Deserialize<'de> for Signal {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        serde::de::IgnoredAny::deserialize(deserializer).map(|_| Signal)
    }
}

/// How a channel's messages decode: from their bytes, or why not.
pub type Decoder<T> = fn(&[u8]) -> Result<T, String>;

/// A message's JSON decoded as a `T`. A generated type ignores keys it
/// does not declare unless the schema says otherwise; the generated
/// definitions decode such a payload with a decoder of their own.
pub fn decode_json<T: DeserializeOwned>(data: &[u8]) -> Result<T, String> {
    serde_json::from_slice(data).map_err(|err| err.to_string())
}

fn decode_signal(_: &[u8]) -> Result<Signal, String> {
    Ok(Signal)
}

/// One channel, whose messages are `T`s encoded as JSON. Get one from a
/// generated definition's `for_`.
pub struct Channel<T> {
    reference: Ref,
    decode: Decoder<T>,
    _message: PhantomData<fn() -> T>,
}

impl<T> Clone for Channel<T> {
    fn clone(&self) -> Self {
        Self {
            reference: self.reference.clone(),
            decode: self.decode,
            _message: PhantomData,
        }
    }
}

impl<T> fmt::Debug for Channel<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Channel")
            .field("declaration", &self.reference.declaration)
            .finish_non_exhaustive()
    }
}

impl<T: DeserializeOwned> Channel<T> {
    /// The channel `name` (without the store's prefix) of `declaration`,
    /// whose messages decode as JSON.
    pub fn new(declaration: impl Into<String>, name: impl Into<String>) -> Self {
        Self::with_decoder(declaration, name, decode_json::<T>)
    }
}

impl Channel<Signal> {
    /// The signal channel `name` of `declaration`: every message is a
    /// [`Signal`], whatever its bytes.
    pub fn signal(declaration: impl Into<String>, name: impl Into<String>) -> Self {
        Self::with_decoder(declaration, name, decode_signal)
    }
}

impl<T> Channel<T> {
    /// The channel `name` of `declaration`, whose messages `decode` reads.
    pub fn with_decoder(
        declaration: impl Into<String>,
        name: impl Into<String>,
        decode: Decoder<T>,
    ) -> Self {
        Self {
            reference: Ref::new(declaration, name),
            decode,
            _message: PhantomData,
        }
    }

    /// The channel in its store.
    pub fn reference(&self) -> &Ref {
        &self.reference
    }

    /// Its name without the store's prefix. It holds the parts' values:
    /// no log or metric carries it.
    pub fn name(&self) -> &str {
        &self.reference.name
    }

    /// Its declaration's name.
    pub fn declaration(&self) -> &str {
        &self.reference.declaration
    }
}

impl<T: Serialize + 'static> Channel<T> {
    /// Encodes `message` as JSON and publishes it on the channel in
    /// `store`.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] at once while the store is, and
    /// [`MemoryStoreError::TooLarge`] for a message over 64 KiB encoded.
    pub async fn publish(
        &self,
        store: &dyn MemoryStore,
        message: &T,
    ) -> Result<(), MemoryStoreError> {
        if !store.available() {
            return Err(MemoryStoreError::Unavailable);
        }
        let data = serde_json::to_vec(message).map_err(|err| {
            MemoryStoreError::Encode(format!("a {} message: {err}", self.declaration()))
        })?;
        check_message(&data)?;
        store.publish(&self.reference, &data).await
    }

    /// Calls `handler` with each message of the channel in `store` until
    /// the subscription stops. A message that does not decode is dropped
    /// and logged at debug, by its declaration alone.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] for a disabled or closed store.
    pub async fn subscribe(
        &self,
        store: &dyn MemoryStore,
        handler: impl Fn(T) + Send + Sync + 'static,
    ) -> Result<Subscription, MemoryStoreError> {
        let decode = self.decode;
        let declaration = self.reference.declaration.clone();
        let deliver: Handler = Arc::new(move |data: &[u8]| match decode(data) {
            Ok(message) => handler(message),
            Err(err) => {
                tracing::debug!(
                    declaration = %declaration,
                    error = %err,
                    "memory store message did not decode; dropped"
                );
            }
        });
        store.subscribe(&self.reference, deliver).await
    }
}

/// One key, a counter or a claim, with the TTL its writes give it. Get one
/// from a generated definition's `for_` or `for_ttl`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Key {
    reference: Ref,
    ttl: Duration,
}

impl Key {
    /// The key `name` (without the store's prefix) of `declaration`, which
    /// lives `ttl` after the write that creates it.
    pub fn new(declaration: impl Into<String>, name: impl Into<String>, ttl: Duration) -> Self {
        Self {
            reference: Ref::new(declaration, name),
            ttl,
        }
    }

    /// The key in its store.
    pub fn reference(&self) -> &Ref {
        &self.reference
    }

    /// Its name without the store's prefix. It holds the parts' values:
    /// no log or metric carries it.
    pub fn name(&self) -> &str {
        &self.reference.name
    }

    /// Its declaration's name.
    pub fn declaration(&self) -> &str {
        &self.reference.declaration
    }

    /// How long the key lives after the write that creates it.
    pub fn ttl(&self) -> Duration {
        self.ttl
    }

    /// Adds one to the count and returns the new count; the increment that
    /// creates the key gives it the key's TTL.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] at once while the store is.
    pub async fn increment(&self, store: &dyn MemoryStore) -> Result<i64, MemoryStoreError> {
        store.increment(&self.reference, self.ttl).await
    }

    /// The count, 0 when the key is absent.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] at once while the store is.
    pub async fn count(&self, store: &dyn MemoryStore) -> Result<i64, MemoryStoreError> {
        store.count(&self.reference).await
    }

    /// Claims the key for its TTL: true when this call set it.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] at once while the store is.
    pub async fn claim(&self, store: &dyn MemoryStore) -> Result<bool, MemoryStoreError> {
        store.claim(&self.reference, self.ttl).await
    }

    /// Removes the key. Only the caller that holds a claim gives it back.
    ///
    /// # Errors
    ///
    /// [`MemoryStoreError::Unavailable`] at once while the store is.
    pub async fn delete(&self, store: &dyn MemoryStore) -> Result<(), MemoryStoreError> {
        store.delete(&self.reference).await
    }
}

// --- A disabled store --------------------------------------------------

/// A store that is off: every call is [`MemoryStoreError::Unavailable`],
/// and it is never available. A workload whose store field is unset, or
/// whose store's config is refused, runs with one.
#[derive(Clone, Debug)]
pub struct DisabledMemoryStore {
    name: String,
}

impl DisabledMemoryStore {
    pub fn new(name: impl Into<String>) -> Self {
        Self { name: name.into() }
    }
}

#[async_trait]
impl MemoryStore for DisabledMemoryStore {
    fn name(&self) -> &str {
        &self.name
    }

    fn available(&self) -> bool {
        false
    }

    async fn publish(&self, _: &Ref, _: &[u8]) -> Result<(), MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn subscribe(&self, _: &Ref, _: Handler) -> Result<Subscription, MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn increment(&self, _: &Ref, _: Duration) -> Result<i64, MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn count(&self, _: &Ref) -> Result<i64, MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn claim(&self, _: &Ref, _: Duration) -> Result<bool, MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn delete(&self, _: &Ref) -> Result<(), MemoryStoreError> {
        Err(MemoryStoreError::Unavailable)
    }

    async fn close(&self) {}
}

// --- An in-process store -------------------------------------------------

/// The clock an in-process store reads a key's expiry with.
pub type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// A store in the process's memory, for tests and for a process that is
/// its own only replica: keys expire against its clock, and a message
/// reaches the subscriptions of this store alone, at once, on the
/// publishing task.
pub struct InProcessMemoryStore {
    name: String,
    clock: Clock,
    closed: AtomicBool,
    next_id: AtomicU64,
    state: Arc<Mutex<InProcessState>>,
}

#[derive(Default)]
struct InProcessState {
    /// Each key's value and when it expires, by full name.
    keys: HashMap<String, (i64, SystemTime)>,
    /// Each channel's handlers, by full name.
    channels: HashMap<String, Vec<(u64, Handler)>>,
}

impl InProcessMemoryStore {
    /// A store whose prefix is `name`, on the system clock.
    pub fn new(name: impl Into<String>) -> Self {
        Self::with_clock(name, SystemTime::now)
    }

    /// A store whose keys expire against `clock`.
    pub fn with_clock(
        name: impl Into<String>,
        clock: impl Fn() -> SystemTime + Send + Sync + 'static,
    ) -> Self {
        Self {
            name: name.into(),
            clock: Arc::new(clock),
            closed: AtomicBool::new(false),
            next_id: AtomicU64::new(1),
            state: Arc::new(Mutex::new(InProcessState::default())),
        }
    }

    fn full_name(&self, reference: &Ref) -> String {
        format!("{}:{}", self.name, reference.name)
    }

    fn state(&self) -> std::sync::MutexGuard<'_, InProcessState> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    fn ready(&self) -> Result<(), MemoryStoreError> {
        if self.closed.load(Ordering::Acquire) {
            return Err(MemoryStoreError::Unavailable);
        }
        Ok(())
    }

    /// The live value of `name`, dropping it when it has expired.
    fn live(state: &mut InProcessState, name: &str, now: SystemTime) -> Option<i64> {
        match state.keys.get(name) {
            Some((value, expires)) if now < *expires => Some(*value),
            Some(_) => {
                state.keys.remove(name);
                None
            }
            None => None,
        }
    }
}

impl fmt::Debug for InProcessMemoryStore {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("InProcessMemoryStore")
            .field("name", &self.name)
            .field("closed", &self.closed.load(Ordering::Relaxed))
            .finish_non_exhaustive()
    }
}

#[async_trait]
impl MemoryStore for InProcessMemoryStore {
    fn name(&self) -> &str {
        &self.name
    }

    fn available(&self) -> bool {
        !self.closed.load(Ordering::Acquire)
    }

    async fn publish(&self, channel: &Ref, data: &[u8]) -> Result<(), MemoryStoreError> {
        self.ready()?;
        check_message(data)?;
        let handlers: Vec<Handler> = self
            .state()
            .channels
            .get(&self.full_name(channel))
            .map(|handlers| handlers.iter().map(|(_, h)| Arc::clone(h)).collect())
            .unwrap_or_default();
        for handler in handlers {
            handler(data);
        }
        Ok(())
    }

    async fn subscribe(
        &self,
        channel: &Ref,
        handler: Handler,
    ) -> Result<Subscription, MemoryStoreError> {
        self.ready()?;
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let full = self.full_name(channel);
        self.state()
            .channels
            .entry(full.clone())
            .or_default()
            .push((id, handler));
        // The subscription holds the channels weakly, so a dropped store
        // frees its handlers.
        let state = Arc::downgrade(&self.state);
        Ok(Subscription::new(move || {
            let Some(state) = state.upgrade() else {
                return;
            };
            let mut state = state
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner());
            if let Some(handlers) = state.channels.get_mut(&full) {
                handlers.retain(|(other, _)| *other != id);
                if handlers.is_empty() {
                    state.channels.remove(&full);
                }
            }
        }))
    }

    async fn increment(&self, key: &Ref, ttl: Duration) -> Result<i64, MemoryStoreError> {
        self.ready()?;
        let ttl = Duration::from_millis(ttl_millis(ttl)?);
        let now = (self.clock)();
        let full = self.full_name(key);
        let mut state = self.state();
        let next = match Self::live(&mut state, &full, now) {
            Some(value) => {
                let next = value.saturating_add(1);
                if let Some(entry) = state.keys.get_mut(&full) {
                    entry.0 = next;
                }
                next
            }
            None => {
                state.keys.insert(full, (1, now + ttl));
                1
            }
        };
        Ok(next)
    }

    async fn count(&self, key: &Ref) -> Result<i64, MemoryStoreError> {
        self.ready()?;
        let now = (self.clock)();
        let full = self.full_name(key);
        Ok(Self::live(&mut self.state(), &full, now).unwrap_or(0))
    }

    async fn claim(&self, key: &Ref, ttl: Duration) -> Result<bool, MemoryStoreError> {
        self.ready()?;
        let ttl = Duration::from_millis(ttl_millis(ttl)?);
        let now = (self.clock)();
        let full = self.full_name(key);
        let mut state = self.state();
        if Self::live(&mut state, &full, now).is_some() {
            return Ok(false);
        }
        state.keys.insert(full, (1, now + ttl));
        Ok(true)
    }

    async fn delete(&self, key: &Ref) -> Result<(), MemoryStoreError> {
        self.ready()?;
        let full = self.full_name(key);
        self.state().keys.remove(&full);
        Ok(())
    }

    async fn close(&self) {
        self.closed.store(true, Ordering::Release);
        let mut state = self.state();
        state.keys.clear();
        state.channels.clear();
    }
}
