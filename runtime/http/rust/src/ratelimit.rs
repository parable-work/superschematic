//! `@rateLimit`, with the Go runtime's semantics: a limiter counting each
//! client's requests per minute, 429 Too Many Requests past the limit with
//! `Retry-After`. The limiter is a token bucket in process memory, as the
//! Go runtime's httprate limiter is, so replicas do not share a budget.
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
use std::time::{Duration, Instant};

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
/// at that rate, one per client.
pub struct RateLimiter {
    requests_per_minute: u32,
    buckets: Mutex<HashMap<String, Bucket>>,
}

impl RateLimiter {
    pub fn per_minute(requests_per_minute: u32) -> Self {
        Self {
            requests_per_minute,
            buckets: Mutex::new(HashMap::new()),
        }
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
}

impl RateLimits {
    pub fn new() -> Self {
        Self::default()
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
            .or_insert_with(|| Arc::new(RateLimiter::per_minute(rate)));
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
        f.debug_struct("RateLimits")
            .field("limiters", &self.len())
            .finish()
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
