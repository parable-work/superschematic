//! `@rateLimit`, with the Go runtime's semantics: one limiter per route,
//! counting each client's requests per minute, 429 Too Many Requests past
//! the limit with `Retry-After`. The limiter is a token bucket in process
//! memory, as the Go runtime's httprate limiter is, so replicas do not
//! share a budget.
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
use std::net::{IpAddr, SocketAddr};
use std::sync::Mutex;
use std::time::{Duration, Instant};

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
