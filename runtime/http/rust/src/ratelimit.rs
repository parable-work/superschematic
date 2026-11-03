//! `@rateLimit`, with the Go runtime's semantics: a limiter counting each
//! client's requests per minute, 429 Too Many Requests past the limit with
//! `Retry-After`. In process, the limiter is a token bucket of the
//! route's limit per minute, refilled continuously, in each replica's own
//! memory, so replicas do not share a budget. (The Go runtime's limiter,
//! httprate, counts a sliding window instead: the previous minute's count
//! weighted by the part of it still in the window, plus this minute's.)
//!
//! An API whose config names a `rateLimitStore` (D75) shares each
//! operation's budget between its replicas: the generated entrypoint
//! builds its registry with [`RateLimits::with_store`], and each limiter
//! counts in that memory store by httprate's rule
//! ([`crate::memorystore::window_decision`]), over the reserved
//! `rate-limit` counters of the operation, the minute and the client,
//! whose key is a SHA-256 in hex. While the store is unavailable, or a
//! call to it fails, a request falls back to the limiter's token bucket.
//!
//! A generated router takes each route's limiter from a [`RateLimits`],
//! one per operation, so everything in a process that holds the same
//! registry (two routers, a Topcoat app's procedures and pages) counts a
//! client against the operation's one budget (D44, amended).
//!
//! A client is its IP address, as the Go runtime keys it: the [`ClientIp`]
//! a service's own layer put on the request, else the peer address axum
//! records when the router is served with
//! `into_make_service_with_connect_info::<SocketAddr>()`. Neither is read
//! from a forwarding header the client can write; a service behind a proxy
//! it trusts sets [`ClientIp`] from that proxy's header. Without either,
//! every client of the route shares one bucket.

use axum::extract::ConnectInfo;
use http::Extensions;
use std::collections::HashMap;
use std::fmt;
use std::net::{IpAddr, SocketAddr};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use crate::memorystore::{
    rate_limit_name, window_decision, MemoryStore, Ref, RATE_LIMIT_DECLARATION,
};
use crate::OperationInfo;

/// The client address a rate limit counts requests by. A service behind a
/// proxy it trusts inserts it into the request's extensions, from the
/// proxy's forwarding header, before the router runs.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ClientIp(pub IpAddr);

/// The bucket key of a request: its [`ClientIp`], its peer address, or
/// `unknown`.
pub fn client_key(extensions: &Extensions) -> String {
    if let Some(ClientIp(ip)) = extensions.get::<ClientIp>() {
        return ip.to_string();
    }
    if let Some(ConnectInfo(peer)) = extensions.get::<ConnectInfo<SocketAddr>>() {
        return peer.ip().to_string();
    }
    "unknown".to_string()
}

/// How many idle buckets a limiter keeps before it drops those full again.
const MAX_KEYS: usize = 10_000;

const MINUTE: Duration = Duration::from_secs(60);

struct Bucket {
    tokens: f64,
    refilled_at: Instant,
}

/// Token buckets of `requests_per_minute` capacity, refilled continuously
/// at that rate, one per client; and for a limiter of a `rateLimitStore`,
/// the shared count it takes first.
pub struct RateLimiter {
    requests_per_minute: u32,
    buckets: Mutex<HashMap<String, Bucket>>,
    shared: Option<SharedCount>,
}

/// Where a shared limiter counts: the store, and the service and operation
/// its counters' names hold.
struct SharedCount {
    store: Arc<dyn MemoryStore>,
    service: String,
    operation: String,
    clock: Arc<dyn Fn() -> SystemTime + Send + Sync>,
}

impl RateLimiter {
    pub fn per_minute(requests_per_minute: u32) -> Self {
        Self {
            requests_per_minute,
            buckets: Mutex::new(HashMap::new()),
            shared: None,
        }
    }

    /// A limiter of `requests_per_minute` that counts `service`'s
    /// `operation` in `store`, by httprate's sliding window, and falls back
    /// to a token bucket of its own while the store is unavailable (D75).
    pub fn shared(
        requests_per_minute: u32,
        store: Arc<dyn MemoryStore>,
        service: impl Into<String>,
        operation: impl Into<String>,
    ) -> Self {
        Self::shared_with_clock(
            requests_per_minute,
            store,
            service,
            operation,
            SystemTime::now,
        )
    }

    fn shared_with_clock(
        requests_per_minute: u32,
        store: Arc<dyn MemoryStore>,
        service: impl Into<String>,
        operation: impl Into<String>,
        clock: impl Fn() -> SystemTime + Send + Sync + 'static,
    ) -> Self {
        Self {
            shared: Some(SharedCount {
                store,
                service: service.into(),
                operation: operation.into(),
                clock: Arc::new(clock),
            }),
            ..Self::per_minute(requests_per_minute)
        }
    }

    /// Admits one request of `key`: in the shared store when the limiter
    /// has one and it answers, else from `key`'s token bucket. `Err` holds
    /// the whole seconds of `Retry-After`.
    pub async fn admit(&self, key: &str) -> Result<(), u64> {
        if let Some(shared) = &self.shared {
            if let Some(decision) = shared.admit(self.requests_per_minute, key).await {
                return decision;
            }
        }
        self.take(key)
    }

    /// Takes one token from `key`'s bucket: `Ok` when the request may go
    /// on, `Err` with the whole seconds until a token is available again.
    pub fn take(&self, key: &str) -> Result<(), u64> {
        self.take_at(key, Instant::now())
    }

    fn take_at(&self, key: &str, now: Instant) -> Result<(), u64> {
        let capacity = f64::from(self.requests_per_minute);
        if capacity < 1.0 {
            return Err(MINUTE.as_secs());
        }
        let per_second = capacity / MINUTE.as_secs_f64();
        let mut buckets = self
            .buckets
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if !buckets.contains_key(key) && buckets.len() >= MAX_KEYS {
            buckets.retain(|_, bucket| now.saturating_duration_since(bucket.refilled_at) < MINUTE);
        }
        let bucket = buckets.entry(key.to_string()).or_insert(Bucket {
            tokens: capacity,
            refilled_at: now,
        });
        let elapsed = now
            .saturating_duration_since(bucket.refilled_at)
            .as_secs_f64();
        bucket.tokens = (bucket.tokens + elapsed * per_second).min(capacity);
        bucket.refilled_at = now;
        if bucket.tokens >= 1.0 {
            bucket.tokens -= 1.0;
            return Ok(());
        }
        // At most 60: one token comes at no less than one a minute.
        let wait = ((1.0 - bucket.tokens) / per_second).ceil();
        Err((wait as u64).max(1))
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.buckets
            .lock()
            .map(|buckets| buckets.len())
            .unwrap_or(0)
    }
}

impl SharedCount {
    /// httprate's rule over the store's counts of this window and the one
    /// before it, for one request of `key`; `None` when the store is
    /// unavailable or a call to it fails, so the request falls back.
    async fn admit(&self, limit: u32, key: &str) -> Option<Result<(), u64>> {
        if !self.store.available() {
            return None;
        }
        let window = i64::try_from(MINUTE.as_millis()).unwrap_or(60_000);
        let now = (self.clock)()
            .duration_since(SystemTime::UNIX_EPOCH)
            .ok()
            .and_then(|since| i64::try_from(since.as_millis()).ok())?;
        let curr_start = now.div_euclid(window) * window;
        let counter = |start: i64| {
            rate_limit_name(&self.service, &self.operation, start / 1000, key)
                .ok()
                .map(|name| Ref::new(RATE_LIMIT_DECLARATION, name))
        };
        let (prev, curr) = (counter(curr_start - window)?, counter(curr_start)?);
        let prev_count = self.store.count(&prev).await.ok()?;
        let curr_count = self.store.count(&curr).await.ok()?;
        let decision = window_decision(i64::from(limit), window, now, prev_count, curr_count);
        match (decision.increment_ttl, decision.retry_after_seconds) {
            (Some(ttl), _) => {
                // The request is admitted: a failed increment undercounts
                // it alone.
                let _ = self.store.increment(&curr, ttl).await;
                Some(Ok(()))
            }
            (None, retry_after) => Some(Err(retry_after.unwrap_or(MINUTE.as_secs()))),
        }
    }
}

/// The key of an operation's limiter: its route's method and path, its
/// namespace, its name and its rate.
type LimiterKey = (&'static str, &'static str, &'static str, &'static str, u32);

/// The rate limiters of one app's operations, one per operation with a
/// `@rateLimit`, made the first time anything asks for it (D44, amended).
/// A clone shares them, so every way into an operation that holds the same
/// registry counts a client against one budget: the routers built from one
/// `RouterOptions`, and a Topcoat app's procedures and pages.
///
/// A registry is meant for one app's operations. It keys a limiter by the
/// operation's route (its method and path), its namespace, its name and
/// its rate, so two APIs' operations of one name on different routes keep
/// their own limiters; two whose routes, names and rates all match would
/// share one, so an app that serves two such APIs gives each a registry.
///
/// Two registries are equal when they are the same registry, a clone of
/// it: equality is identity, never the limiters' contents. `RouterOptions`
/// keeps its `PartialEq` and `Eq` by it, so two options are equal only
/// when they share their registry, and `RouterOptions::default()` is not
/// equal to another `RouterOptions::default()`.
#[derive(Clone, Default)]
pub struct RateLimits {
    limiters: Arc<Mutex<HashMap<LimiterKey, Arc<RateLimiter>>>>,
    store: Option<(Arc<dyn MemoryStore>, Arc<str>)>,
}

impl RateLimits {
    pub fn new() -> Self {
        Self::default()
    }

    /// A registry whose limiters count in `store`, the API's
    /// `rateLimitStore`, under the API service `service`, so the replicas
    /// of its servers share each operation's budget (D75). Each falls back
    /// to its own token bucket while the store is unavailable.
    pub fn with_store(service: impl Into<String>, store: Arc<dyn MemoryStore>) -> Self {
        Self {
            limiters: Arc::default(),
            store: Some((store, Arc::from(service.into()))),
        }
    }

    /// The limiter of `operation`'s `@rateLimit`, at its rate, made the
    /// first time it is asked for; none for an operation without one.
    pub fn of(&self, operation: &OperationInfo) -> Option<Arc<RateLimiter>> {
        let rate = operation.rate_limit?;
        let mut limiters = self
            .limiters
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let limiter = limiters
            .entry((
                operation.method,
                operation.path,
                operation.namespace,
                operation.name,
                rate,
            ))
            .or_insert_with(|| {
                Arc::new(match &self.store {
                    None => RateLimiter::per_minute(rate),
                    Some((store, service)) => RateLimiter::shared(
                        rate,
                        Arc::clone(store),
                        service.as_ref(),
                        format!("{}.{}", operation.namespace, operation.name),
                    ),
                })
            });
        Some(Arc::clone(limiter))
    }

    fn len(&self) -> usize {
        self.limiters
            .lock()
            .map(|limiters| limiters.len())
            .unwrap_or(0)
    }
}

impl fmt::Debug for RateLimits {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut debug = f.debug_struct("RateLimits");
        debug.field("limiters", &self.len());
        if let Some((store, _)) = &self.store {
            debug.field("store", &store.name());
        }
        debug.finish()
    }
}

impl PartialEq for RateLimits {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.limiters, &other.limiters)
    }
}

impl Eq for RateLimits {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_client_gets_its_limit_per_minute_then_waits_for_a_token() {
        let limiter = RateLimiter::per_minute(2);
        let start = Instant::now();
        assert_eq!(limiter.take_at("a", start), Ok(()));
        assert_eq!(limiter.take_at("a", start), Ok(()));
        assert_eq!(limiter.take_at("a", start), Err(30));
        assert_eq!(limiter.take_at("b", start), Ok(()));
        assert_eq!(
            limiter.take_at("a", start + Duration::from_secs(29)),
            Err(1)
        );
        assert_eq!(
            limiter.take_at("a", start + Duration::from_secs(30)),
            Ok(())
        );
    }

    #[test]
    fn one_request_a_minute_waits_the_whole_minute() {
        let limiter = RateLimiter::per_minute(1);
        let start = Instant::now();
        assert_eq!(limiter.take_at("a", start), Ok(()));
        assert_eq!(limiter.take_at("a", start), Err(60));
    }

    #[test]
    fn idle_buckets_are_dropped_past_the_key_bound() {
        let limiter = RateLimiter::per_minute(5);
        let start = Instant::now();
        for i in 0..MAX_KEYS {
            assert!(limiter.take_at(&i.to_string(), start).is_ok());
        }
        assert_eq!(limiter.len(), MAX_KEYS);
        assert!(limiter.take_at("late", start + MINUTE).is_ok());
        assert_eq!(limiter.len(), 1);
    }

    const PLACE_ORDER: OperationInfo = OperationInfo {
        namespace: "orders",
        name: "placeOrder",
        method: "POST",
        path: "/api/orders",
        requires_auth: false,
        permissions: &[],
        require_ownership: false,
        manual: false,
        rate_limit: Some(2),
        service: None,
    };

    #[test]
    fn an_operation_has_one_limiter_in_a_registry() {
        let limits = RateLimits::new();
        let first = limits.of(&PLACE_ORDER).unwrap();
        let second = limits.of(&PLACE_ORDER).unwrap();
        assert!(Arc::ptr_eq(&first, &second));
        assert_eq!(first.take("a"), Ok(()));
        assert_eq!(second.take("a"), Ok(()));
        assert!(first.take("a").is_err(), "one budget");

        // Another operation, or the same at another rate, has its own.
        let cancel = OperationInfo {
            name: "cancelOrder",
            ..PLACE_ORDER
        };
        assert!(!Arc::ptr_eq(&first, &limits.of(&cancel).unwrap()));
        let faster = OperationInfo {
            rate_limit: Some(5),
            ..PLACE_ORDER
        };
        assert!(!Arc::ptr_eq(&first, &limits.of(&faster).unwrap()));
        assert_eq!(format!("{limits:?}"), "RateLimits { limiters: 3 }");
    }

    #[test]
    fn two_operations_of_one_name_on_different_routes_have_their_own_limiters() {
        // Two APIs' orders.placeOrder, one under each API's path, in one
        // registry: each route keeps its own budget.
        let limits = RateLimits::new();
        let other_api = OperationInfo {
            path: "/api/wholesale/orders",
            ..PLACE_ORDER
        };
        let shop = limits.of(&PLACE_ORDER).unwrap();
        let wholesale = limits.of(&other_api).unwrap();
        assert!(!Arc::ptr_eq(&shop, &wholesale));
        assert_eq!(shop.take("a"), Ok(()));
        assert_eq!(shop.take("a"), Ok(()));
        assert!(shop.take("a").is_err());
        assert_eq!(wholesale.take("a"), Ok(()), "the other route's budget");

        // Nor does another method on the same path share it.
        let put = OperationInfo {
            method: "PUT",
            ..PLACE_ORDER
        };
        assert!(!Arc::ptr_eq(&shop, &limits.of(&put).unwrap()));
        assert_eq!(format!("{limits:?}"), "RateLimits { limiters: 3 }");
    }

    #[test]
    fn a_clone_of_a_registry_shares_its_limiters() {
        let limits = RateLimits::new();
        let clone = limits.clone();
        assert_eq!(limits, clone);
        assert!(Arc::ptr_eq(
            &limits.of(&PLACE_ORDER).unwrap(),
            &clone.of(&PLACE_ORDER).unwrap()
        ));
    }

    #[test]
    fn an_operation_without_a_rate_limit_has_no_limiter() {
        let limits = RateLimits::new();
        let open = OperationInfo {
            rate_limit: None,
            ..PLACE_ORDER
        };
        assert!(limits.of(&open).is_none());
        assert_eq!(limits.len(), 0);
    }

    #[test]
    fn a_new_registry_shares_nothing() {
        let limits = RateLimits::new();
        let other = RateLimits::new();
        assert_ne!(limits, other);
        assert!(!Arc::ptr_eq(
            &limits.of(&PLACE_ORDER).unwrap(),
            &other.of(&PLACE_ORDER).unwrap()
        ));
    }

    /// A clock a test sets, in milliseconds from the epoch.
    fn clock(ms: &Arc<std::sync::atomic::AtomicU64>) -> impl Fn() -> SystemTime + Send + Sync {
        let ms = Arc::clone(ms);
        move || {
            SystemTime::UNIX_EPOCH
                + Duration::from_millis(ms.load(std::sync::atomic::Ordering::SeqCst))
        }
    }

    #[tokio::test]
    async fn replicas_share_a_budget_in_the_store_by_the_sliding_window() {
        use crate::memorystore::InProcessMemoryStore;
        use std::sync::atomic::{AtomicU64, Ordering};
        let now = Arc::new(AtomicU64::new(1_761_350_400_000));
        let store: Arc<dyn MemoryStore> =
            Arc::new(InProcessMemoryStore::with_clock("shop-cache", clock(&now)));
        let replica = |store: &Arc<dyn MemoryStore>| {
            RateLimiter::shared_with_clock(
                3,
                Arc::clone(store),
                "shop-api",
                "orders.placeOrder",
                clock(&now),
            )
        };
        let (a, b) = (replica(&store), replica(&store));
        assert_eq!(a.admit("203.0.113.7").await, Ok(()));
        assert_eq!(b.admit("203.0.113.7").await, Ok(()));
        assert_eq!(a.admit("203.0.113.7").await, Ok(()));
        // The fourth, on either replica, is past the shared limit, and
        // waits the window.
        assert_eq!(b.admit("203.0.113.7").await, Err(60));
        assert_eq!(a.admit("198.51.100.1").await, Ok(()), "another client");
        // Half way through the next minute, half of the last one's three
        // still count: round(1.5) + 1 = 3 <= 3 admits one more.
        now.fetch_add(90_000, Ordering::SeqCst);
        assert_eq!(a.admit("203.0.113.7").await, Ok(()));
        assert_eq!(b.admit("203.0.113.7").await, Err(60));
    }

    #[tokio::test]
    async fn an_unavailable_store_falls_back_to_the_token_bucket() {
        use crate::memorystore::DisabledMemoryStore;
        let store: Arc<dyn MemoryStore> = Arc::new(DisabledMemoryStore::new("shop-cache"));
        let limiter = RateLimiter::shared(2, store, "shop-api", "orders.placeOrder");
        assert_eq!(limiter.admit("a").await, Ok(()));
        assert_eq!(limiter.admit("a").await, Ok(()));
        assert_eq!(limiter.admit("a").await, Err(30), "the bucket's wait");
    }

    #[tokio::test]
    async fn a_registry_with_a_store_makes_shared_limiters() {
        use crate::memorystore::InProcessMemoryStore;
        let store: Arc<dyn MemoryStore> = Arc::new(InProcessMemoryStore::new("shop-cache"));
        let limits = RateLimits::with_store("shop-api", Arc::clone(&store));
        let other = RateLimits::with_store("shop-api", store);
        // Two processes' registries, one store: one budget.
        let (a, b) = (
            limits.of(&PLACE_ORDER).unwrap(),
            other.of(&PLACE_ORDER).unwrap(),
        );
        assert_eq!(a.admit("c").await, Ok(()));
        assert_eq!(b.admit("c").await, Ok(()));
        assert!(a.admit("c").await.is_err());
        assert_eq!(
            format!("{limits:?}"),
            r#"RateLimits { limiters: 1, store: "shop-cache" }"#
        );
    }

    #[test]
    fn the_key_is_the_client_ip_then_the_peer_address() {
        let mut extensions = Extensions::new();
        assert_eq!(client_key(&extensions), "unknown");
        extensions.insert(ConnectInfo(SocketAddr::from(([10, 0, 0, 7], 5000))));
        assert_eq!(client_key(&extensions), "10.0.0.7");
        extensions.insert(ClientIp(IpAddr::from([203, 0, 113, 9])));
        assert_eq!(client_key(&extensions), "203.0.113.9");
    }
}
