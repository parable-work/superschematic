//! [`JwtBodyHashVerifier`] (D81): the webhook verifier of a provider that
//! signs each request with a JWT carrying the SHA-256 of its raw body, as
//! Plaid does (the `Plaid-Verification` header, ES256, `iat` and
//! `request_body_sha256`). It runs the checks of the Go
//! (`webhook.JWTBodyHashVerifier`) and TypeScript (`jwtBodyHashVerifier`)
//! verifiers, in their order, so a request gets the same answer from each:
//!
//! 1. The header holds a compact JWS: three base64url segments, the first
//!    two JSON objects. Its `alg` is the configured one (never `none`) and
//!    its `kid` a non-empty string.
//! 2. The key with that `kid`, held or looked up (below). One that cannot be
//!    looked up, with none held: 503 `service_unavailable`.
//! 3. The signature verifies with the key, a P-256 key whose `use`, when
//!    set, is `sig` and whose `alg`, when set, is the configured one.
//! 4. `iat` is a number, at most `max_age` in the past and `clock_skew` in
//!    the future.
//! 5. The body, read up to `max_body_bytes` (413 `payload_too_large` past
//!    it), has the SHA-256 whose lowercase hex the body hash claim holds,
//!    compared in constant time.
//!
//! Any other refusal is 401 `invalid_signature`. The request `next` runs
//! is rebuilt with the body's bytes.
//!
//! Keys are held by `kid`. A held key serves for an hour; then the next
//! request that needs it looks it up again, and a lookup that fails keeps
//! it. A `kid` not held is looked up at once, but the provider is asked at
//! most once a minute, whatever prompts it, so a flood of random kids costs
//! one lookup a minute. One lookup runs at a time: a request whose key is
//! held does not wait for it, and one whose `kid` is not held waits, then
//! looks again. A [`WebhookKeyLookup`] answers for one `kid`; a JWKS URL's
//! set replaces every key held.
//!
//! The generated crate's `WebhookVerifier` trait is the crate's own, so a
//! service wraps the verifier in a type of its own that implements it:
//!
//! ```ignore
//! struct Plaid(JwtBodyHashVerifier);
//!
//! #[async_trait]
//! impl WebhookVerifier for Plaid {
//!     async fn verify(&self, request: Request, next: Next) -> Response {
//!         self.0.verify(request, next).await
//!     }
//! }
//! ```

use crate::jws::{decode, object, PublicKey};
use crate::jwt::{Clock, KeyFetcher};
use crate::{error_response, ApiError, JwsAlgorithm};
use async_trait::async_trait;
use axum::body::Body;
use axum::extract::Request;
use axum::http::{HeaderName, StatusCode};
use axum::middleware::Next;
use axum::response::Response;
use http::header::CONTENT_LENGTH;
use serde_json::{Map, Value};
use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// The problem code of a token that is missing or does not verify.
pub const INVALID_SIGNATURE_CODE: &str = "invalid_signature";
const INVALID_SIGNATURE_DETAIL: &str = "The webhook signature does not match";
const KEY_UNAVAILABLE_DETAIL: &str = "The webhook's signing key could not be fetched";
const DEFAULT_CLOCK_SKEW: Duration = Duration::from_secs(60);
/// 1 MiB, the TypeScript router's default body cap.
const DEFAULT_MAX_BODY_BYTES: usize = 1024 * 1024;
/// A held key older than this is looked up again.
const KEY_MAX_AGE_MS: u64 = 60 * 60 * 1000;
/// The provider is asked at most this often, whatever prompts it.
const LOOKUP_MIN_INTERVAL_MS: u64 = 60 * 1000;
/// Bounds one lookup.
const LOOKUP_TIMEOUT: Duration = Duration::from_secs(10);

/// Looks a provider's public key up by `kid`, as Plaid's
/// `/webhook_verification_key/get` does. A closure
/// `Fn(String) -> impl Future<Output = Result<Option<Value>, String>>` is one.
#[async_trait]
pub trait WebhookKeyLookup: Send + Sync + 'static {
    /// The public JWK with `kid`; `Ok(None)` when the provider has none (or
    /// has expired it, Plaid's `expired_at`), `Err` when it cannot be asked.
    /// The message is for logs.
    async fn key(&self, kid: &str) -> Result<Option<Value>, String>;
}

#[async_trait]
impl<F, Fut> WebhookKeyLookup for F
where
    F: Fn(String) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<Option<Value>, String>> + Send + 'static,
{
    async fn key(&self, kid: &str) -> Result<Option<Value>, String> {
        self(kid.to_string()).await
    }
}

/// Where a [`JwtBodyHashVerifier`] finds the provider's keys.
#[derive(Clone)]
pub enum WebhookKeys {
    /// A lookup by `kid`.
    Lookup(Arc<dyn WebhookKeyLookup>),
    /// A JWKS, fetched whole with `fetcher` (the `http-client` feature's
    /// `HttpFetcher`, say).
    Jwks {
        url: String,
        fetcher: Arc<dyn KeyFetcher>,
    },
}

/// The config of a [`JwtBodyHashVerifier`].
pub struct JwtBodyHashOptions {
    /// The header that carries the compact JWS, such as `Plaid-Verification`.
    pub header: String,
    pub keys: WebhookKeys,
    /// The algorithm the token header must name: [`JwsAlgorithm::Es256`].
    pub alg: JwsAlgorithm,
    /// The oldest `iat` accepted.
    pub max_age: Duration,
    /// The claim that holds the lowercase hex SHA-256 of the raw body, such
    /// as `request_body_sha256`.
    pub body_hash_claim: String,
    /// How far in the future an `iat` may be; 60 seconds when `None`.
    pub clock_skew: Option<Duration>,
    /// The largest body read; 1 MiB when `None`.
    pub max_body_bytes: Option<usize>,
}

/// The verifier of a provider that signs each webhook with a JWT carrying
/// the SHA-256 of the raw body; the module's comment states its checks.
pub struct JwtBodyHashVerifier {
    header: HeaderName,
    alg: JwsAlgorithm,
    alg_name: &'static str,
    max_age: f64,
    clock_skew: f64,
    claim: String,
    max_body_bytes: usize,
    clock: Clock,
    keys: KeyCache,
}

/// A refusal, and why, for the log.
struct Refusal {
    error: ApiError,
    cause: String,
}

fn invalid(cause: impl Into<String>) -> Refusal {
    Refusal {
        error: ApiError::new(
            StatusCode::UNAUTHORIZED,
            INVALID_SIGNATURE_CODE,
            INVALID_SIGNATURE_DETAIL,
        ),
        cause: cause.into(),
    }
}

fn unavailable(cause: impl Into<String>) -> Refusal {
    Refusal {
        error: ApiError::service_unavailable(KEY_UNAVAILABLE_DETAIL),
        cause: cause.into(),
    }
}

impl JwtBodyHashVerifier {
    /// Checks the config: a header, ES256, a positive `max_age` and a body
    /// hash claim.
    pub fn new(options: JwtBodyHashOptions) -> Result<Self, String> {
        let fail = |message: &str| Err(format!("JwtBodyHashVerifier: {message}"));
        if options.header.trim().is_empty() {
            return fail("header is required (the header that carries the token, such as Plaid-Verification)");
        }
        let Ok(header) = HeaderName::try_from(options.header.trim()) else {
            return fail(&format!("header {:?} is no header name", options.header));
        };
        if options.alg != JwsAlgorithm::Es256 {
            let name = match options.alg {
                JwsAlgorithm::Rs256 => "RS256",
                JwsAlgorithm::Es256 => "ES256",
                JwsAlgorithm::EdDsa => "EdDSA",
            };
            return fail(&format!("alg {name} is not supported; use ES256"));
        }
        if options.max_age.is_zero() {
            return fail("max_age must be positive");
        }
        if options.body_hash_claim.is_empty() {
            return fail(
                "body_hash_claim is required (the claim that holds the body's SHA-256, such as request_body_sha256)",
            );
        }
        if options.max_body_bytes == Some(0) {
            return fail("max_body_bytes must be positive");
        }
        Ok(Self {
            header,
            alg: options.alg,
            alg_name: "ES256",
            max_age: options.max_age.as_secs_f64(),
            clock_skew: options
                .clock_skew
                .unwrap_or(DEFAULT_CLOCK_SKEW)
                .as_secs_f64(),
            claim: options.body_hash_claim,
            max_body_bytes: options.max_body_bytes.unwrap_or(DEFAULT_MAX_BODY_BYTES),
            clock: Arc::new(SystemTime::now),
            keys: KeyCache::new(options.keys),
        })
    }

    /// The clock `iat` and the key cache are checked against.
    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    /// Checks `request`, and runs `next` with it, its body's bytes in place,
    /// or answers the refusal. It is the body of a `WebhookVerifier`'s
    /// `verify`.
    pub async fn verify(&self, request: Request, next: Next) -> Response {
        match self.check(request).await {
            Ok(request) => next.run(request).await,
            Err(refusal) => {
                if refusal.error.status.is_server_error() {
                    tracing::warn!(cause = %refusal.cause, "webhook refused");
                } else {
                    tracing::debug!(cause = %refusal.cause, "webhook refused");
                }
                error_response(refusal.error)
            }
        }
    }

    fn now(&self) -> Duration {
        (self.clock)()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
    }

    async fn check(&self, request: Request) -> Result<Request, Refusal> {
        let mut values = request.headers().get_all(&self.header).iter();
        let token = match (values.next(), values.next()) {
            (Some(value), None) => value
                .to_str()
                .map_err(|_| invalid("the token is not ASCII"))?,
            (None, _) => return Err(invalid(format!("no {} header", self.header))),
            (Some(_), Some(_)) => return Err(invalid(format!("two {} headers", self.header))),
        };
        let mut segments = token.split('.');
        let (Some(encoded_header), Some(encoded_claims), Some(encoded_signature), None) = (
            segments.next(),
            segments.next(),
            segments.next(),
            segments.next(),
        ) else {
            return Err(invalid("the token is not a compact JWS"));
        };
        let (Some(header), Some(claims), Some(signature)) = (
            object(encoded_header),
            object(encoded_claims),
            decode(encoded_signature),
        ) else {
            return Err(invalid("a segment of the token is not base64url JSON"));
        };
        if header.get("alg").and_then(Value::as_str) != Some(self.alg_name) {
            return Err(invalid(format!(
                "the token's alg {:?} is not {}",
                header.get("alg"),
                self.alg_name
            )));
        }
        let kid = match header.get("kid").and_then(Value::as_str) {
            Some(kid) if !kid.is_empty() => kid,
            _ => return Err(invalid("the token has no kid")),
        };
        let key = self.keys.key(kid, || self.now(), self.alg_name).await?;
        let input = format!("{encoded_header}.{encoded_claims}");
        if !key.verify(self.alg, input.as_bytes(), &signature) {
            return Err(invalid(format!(
                "the signature does not verify with key {kid:?}"
            )));
        }

        let Some(iat) = claims.get("iat").and_then(Value::as_f64) else {
            return Err(invalid("the token has no numeric iat"));
        };
        let now = self.now().as_secs_f64();
        if iat > now + self.clock_skew {
            return Err(invalid("the token's iat is in the future"));
        }
        if now - iat > self.max_age {
            return Err(invalid("the token's iat is older than the maximum age"));
        }
        let Some(claimed) = claims.get(&self.claim).and_then(Value::as_str) else {
            return Err(invalid(format!("the token has no {} claim", self.claim)));
        };
        let claimed = claimed.to_string();

        let (parts, body) = request.into_parts();
        let declared = parts
            .headers
            .get(CONTENT_LENGTH)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<u64>().ok());
        if declared.is_some_and(|length| length > self.max_body_bytes as u64) {
            return Err(too_large());
        }
        let bytes = match axum::body::to_bytes(body, self.max_body_bytes).await {
            Ok(bytes) => bytes,
            Err(err) if is_length_limit(&err) => return Err(too_large()),
            Err(err) => {
                return Err(Refusal {
                    error: ApiError::bad_request("The request body could not be read"),
                    cause: err.to_string(),
                })
            }
        };
        let digest = hex(ring::digest::digest(&ring::digest::SHA256, &bytes).as_ref());
        if !equal_text(claimed.as_bytes(), digest.as_bytes()) {
            return Err(invalid(format!(
                "the body's SHA-256 is not the token's {}",
                self.claim
            )));
        }
        Ok(Request::from_parts(parts, Body::from(bytes)))
    }
}

fn too_large() -> Refusal {
    Refusal {
        error: ApiError::payload_too_large("Request body exceeds the accepted size"),
        cause: "the body is over the verifier's limit".to_string(),
    }
}

fn is_length_limit(err: &axum::Error) -> bool {
    let mut source: Option<&(dyn std::error::Error + 'static)> = Some(err);
    while let Some(current) = source {
        if current.is::<http_body_util::LengthLimitError>() {
            return true;
        }
        source = current.source();
    }
    false
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write;
    bytes.iter().fold(String::new(), |mut text, byte| {
        let _ = write!(text, "{byte:02x}");
        text
    })
}

/// Whether two byte strings are equal, in time that depends on their
/// length alone.
fn equal_text(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0u8, |diff, (x, y)| diff | (x ^ y)) == 0
}

/// The key a JWK holds, when the verifier can use it: public, EC P-256, a
/// `use` of `sig` and an `alg` of `alg` when they are set.
fn usable(jwk: &Map<String, Value>, alg: &str) -> Option<PublicKey> {
    let member = |name: &str| jwk.get(name).and_then(Value::as_str).unwrap_or_default();
    if member("kty") != "EC" || member("crv") != "P-256" {
        return None;
    }
    if !matches!(member("use"), "" | "sig") || !(member("alg").is_empty() || member("alg") == alg) {
        return None;
    }
    match PublicKey::from_jwk(jwk) {
        Ok(key @ PublicKey::P256(_)) => Some(key),
        _ => None,
    }
}

/// The usable keys of a JWKS document by `kid`; of two keys with one
/// `kid`, the first is kept.
fn key_set(document: &[u8], alg: &str) -> Result<HashMap<String, Arc<PublicKey>>, String> {
    let document: Value =
        serde_json::from_slice(document).map_err(|err| format!("the key set: {err}"))?;
    let Some(entries) = document.get("keys").and_then(Value::as_array) else {
        return Err("the key set has no keys list".to_string());
    };
    let mut keys = HashMap::new();
    for entry in entries {
        let Some(jwk) = entry.as_object() else {
            continue;
        };
        let Some(kid) = jwk
            .get("kid")
            .and_then(Value::as_str)
            .filter(|kid| !kid.is_empty())
        else {
            continue;
        };
        if keys.contains_key(kid) {
            continue;
        }
        if let Some(key) = usable(jwk, alg) {
            keys.insert(kid.to_string(), Arc::new(key));
        }
    }
    Ok(keys)
}

#[derive(Clone)]
struct Held {
    key: Arc<PublicKey>,
    fetched_at: u64,
}

#[derive(Default)]
struct CacheState {
    held: HashMap<String, Held>,
    attempted_at: Option<u64>,
}

/// The provider's keys by `kid`. A lookup answers the keys it found: for a
/// [`WebhookKeyLookup`] the kid's, or none, which drops a held one; for a
/// key set every key, which replaces those held. `lookups` is held while a
/// lookup runs.
struct KeyCache {
    source: WebhookKeys,
    state: std::sync::Mutex<CacheState>,
    lookups: tokio::sync::Mutex<()>,
}

impl KeyCache {
    fn new(source: WebhookKeys) -> Self {
        Self {
            source,
            state: std::sync::Mutex::default(),
            lookups: tokio::sync::Mutex::new(()),
        }
    }

    fn held(&self, kid: &str) -> (Option<Held>, Option<u64>) {
        let state = self
            .state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        (state.held.get(kid).cloned(), state.attempted_at)
    }

    async fn key(
        &self,
        kid: &str,
        now: impl Fn() -> Duration,
        alg: &str,
    ) -> Result<Arc<PublicKey>, Refusal> {
        let millis = || u64::try_from(now().as_millis()).unwrap_or(u64::MAX);
        loop {
            let (held, _) = self.held(kid);
            if let Some(held) = &held {
                if millis().saturating_sub(held.fetched_at) < KEY_MAX_AGE_MS {
                    return Ok(Arc::clone(&held.key));
                }
            }
            let Ok(running) = self.lookups.try_lock() else {
                if let Some(held) = held {
                    return Ok(held.key);
                }
                // Wait for the running lookup, then look again.
                drop(self.lookups.lock().await);
                continue;
            };
            let at = millis();
            let (held, attempted_at) = self.held(kid);
            if let Some(held) = &held {
                if at.saturating_sub(held.fetched_at) < KEY_MAX_AGE_MS {
                    return Ok(Arc::clone(&held.key));
                }
            }
            if attempted_at
                .is_some_and(|attempted| at.saturating_sub(attempted) < LOOKUP_MIN_INTERVAL_MS)
            {
                return held.map(|held| held.key).ok_or_else(|| {
                    invalid(format!(
                        "no key {kid:?}, and the provider was asked less than a minute ago"
                    ))
                });
            }
            self.state
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner())
                .attempted_at = Some(at);
            let found = match tokio::time::timeout(LOOKUP_TIMEOUT, self.lookup(kid, alg)).await {
                Ok(found) => found,
                Err(_) => Err("the lookup timed out".to_string()),
            };
            let holds = {
                let mut state = self
                    .state
                    .lock()
                    .unwrap_or_else(|poisoned| poisoned.into_inner());
                match &found {
                    Err(_) => {}
                    Ok(keys) if matches!(self.source, WebhookKeys::Jwks { .. }) => {
                        state.held = keys
                            .iter()
                            .map(|(id, key)| {
                                (
                                    id.clone(),
                                    Held {
                                        key: Arc::clone(key),
                                        fetched_at: at,
                                    },
                                )
                            })
                            .collect();
                    }
                    Ok(keys) => match keys.get(kid) {
                        Some(key) => {
                            state.held.insert(
                                kid.to_string(),
                                Held {
                                    key: Arc::clone(key),
                                    fetched_at: at,
                                },
                            );
                        }
                        None => {
                            state.held.remove(kid);
                        }
                    },
                }
                state.held.contains_key(kid)
            };
            drop(running);
            if let Err(err) = found {
                if !holds {
                    return Err(unavailable(err));
                }
            }
            // Look again: the lookup may have brought the kid, or not.
        }
    }

    async fn lookup(
        &self,
        kid: &str,
        alg: &str,
    ) -> Result<HashMap<String, Arc<PublicKey>>, String> {
        match &self.source {
            WebhookKeys::Lookup(lookup) => {
                let jwk = lookup.key(kid).await?;
                Ok(jwk
                    .as_ref()
                    .and_then(Value::as_object)
                    .and_then(|jwk| usable(jwk, alg))
                    .map(|key| HashMap::from([(kid.to_string(), Arc::new(key))]))
                    .unwrap_or_default())
            }
            WebhookKeys::Jwks { url, fetcher } => key_set(&fetcher.fetch(url, None).await?, alg),
        }
    }
}
