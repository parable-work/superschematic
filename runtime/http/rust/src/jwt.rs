//! [`JwtServiceAuthenticator`]: the runtime's service authenticator, over a
//! [`ServiceAuthConfig`]. It runs the checks of the Go and TypeScript
//! verifiers, in their order, so a token gets the same answer from each:
//!
//! 1. No `Service-Authorization` header: no caller.
//! 2. Not `Bearer <token>`, or a token that is not three base64url segments
//!    whose first two are JSON objects: 401.
//! 3. `iss` names no issuer, the header's `alg` is not one of the issuer's
//!    `algorithms` (`Ed25519` reads as `EdDSA`), or there is no `kid`: 401.
//! 4. The key: the issuer's static key with that `kid`, or one from its
//!    JWKS (below). No keys at all, because the JWKS cannot be fetched: 503.
//!    An unknown `kid`, a key of another type than `alg`'s, or a signature
//!    that does not verify: 401.
//! 5. The claims, with the leeway: `exp` required and not past, `nbf` and
//!    `iat` not in the future, `aud` naming the issuer's audience, and with
//!    `maxLifetimeSeconds` an `iat` no further than that before `exp`: 401.
//! 6. The subject claim names one of the issuer's callers, or 403.
//!
//! A JWKS is cached per URL and kept until a fetch replaces it. It is
//! fetched again when a token names a `kid` it lacks, at most once a
//! minute, and when it is an hour old; a fetch that fails keeps the keys
//! held.

use crate::jws::{Jws, PublicKey};
use crate::service_auth::SERVICE_AUTHORIZATION;
use crate::{
    ApiError, JwsAlgorithm, ServiceAuthConfig, ServiceAuthenticator, ServiceCaller,
    ServiceCallerConfig, ServiceIssuer,
};
use async_trait::async_trait;
use http::request::Parts;
use http::HeaderMap;
use serde_json::{Map, Value};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::sync::Mutex;

/// A JWKS older than this is fetched again.
const JWKS_MAX_AGE_SECONDS: u64 = 60 * 60;
/// A JWKS is fetched at most this often per URL.
const JWKS_MIN_INTERVAL_SECONDS: u64 = 60;

/// Fetches a JWKS: the body of a GET of `url`, with `Authorization: Bearer
/// <bearer>` when there is one. An `Err` is a failed fetch (a non-2xx answer
/// included); its message is for logs.
#[async_trait]
pub trait KeyFetcher: Send + Sync + 'static {
    async fn fetch(&self, url: &str, bearer: Option<&str>) -> Result<Vec<u8>, String>;
}

pub(crate) type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

pub(crate) fn unix_seconds(clock: &Clock) -> u64 {
    clock()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_secs())
}

/// Verifies `Service-Authorization` JWTs against a [`ServiceAuthConfig`].
///
/// ```no_run
/// # use superschematic_http_runtime::{JwtServiceAuthenticator, ServiceAuthConfig};
/// # fn build(json: &str) -> Result<(), String> {
/// let config: ServiceAuthConfig = serde_json::from_str(json).map_err(|e| e.to_string())?;
/// let authenticator = JwtServiceAuthenticator::new(config)?;
/// # Ok(()) }
/// ```
///
/// An issuer with a `jwksUrl` needs a [`KeyFetcher`]: the `http-client`
/// feature's `HttpFetcher` is the default with that feature on; without
/// it, pass one with [`JwtServiceAuthenticator::with_key_fetcher`], or each
/// fetch fails and a token from that issuer answers 503.
pub struct JwtServiceAuthenticator {
    issuers: Vec<Issuer>,
    leeway: u64,
    clock: Clock,
    fetcher: Arc<dyn KeyFetcher>,
    jwks: HashMap<String, Mutex<JwksCache>>,
}

struct Issuer {
    names: Vec<String>,
    audience: String,
    algorithms: Vec<JwsAlgorithm>,
    keys: Keys,
    subject_claim: String,
    max_lifetime: Option<u64>,
    callers: BTreeMap<String, ServiceCallerConfig>,
}

enum Keys {
    Static(HashMap<String, Arc<PublicKey>>),
    Jwks {
        url: String,
        bearer_token_file: Option<String>,
    },
}

#[derive(Default)]
struct JwksCache {
    keys: Option<HashMap<String, Arc<PublicKey>>>,
    fetched_at: u64,
    attempted_at: Option<u64>,
}

impl JwtServiceAuthenticator {
    /// Checks the config, as the Go `serviceauth.New` does: each issuer has
    /// a name, an audience and algorithms; a `jwksUrl` or static `keys`, not
    /// both, each key a public key with a `kid` of its own; callers with a
    /// deployable; and no `iss` names two issuers.
    pub fn new(config: ServiceAuthConfig) -> Result<Self, String> {
        let mut seen = HashSet::new();
        let mut jwks = HashMap::new();
        let mut issuers = Vec::with_capacity(config.issuers.len());
        for entry in config.issuers {
            let issuer = Issuer::new(entry)?;
            for name in &issuer.names {
                if name.is_empty() || !seen.insert(name.clone()) {
                    return Err(format!(
                        "service auth: issuer {name:?} is empty or another issuer's"
                    ));
                }
            }
            if let Keys::Jwks { url, .. } = &issuer.keys {
                jwks.entry(url.clone()).or_insert_with(Mutex::default);
            }
            issuers.push(issuer);
        }
        Ok(Self {
            issuers,
            leeway: config.leeway_seconds,
            clock: Arc::new(SystemTime::now),
            fetcher: default_fetcher(),
            jwks,
        })
    }

    /// The clock `exp`, `nbf`, `iat` and the JWKS cache are checked against.
    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    /// How a `jwksUrl` is fetched.
    #[must_use]
    pub fn with_key_fetcher(mut self, fetcher: Arc<dyn KeyFetcher>) -> Self {
        self.fetcher = fetcher;
        self
    }

    /// The caller a request's `Service-Authorization` names; `Ok(None)`
    /// without the header. Two of the header are refused.
    pub async fn verify(&self, headers: &HeaderMap) -> Result<Option<ServiceCaller>, ApiError> {
        let mut values = headers.get_all(SERVICE_AUTHORIZATION).iter();
        let Some(value) = values.next() else {
            return Ok(None);
        };
        if values.next().is_some() {
            return Err(invalid("more than one Service-Authorization header"));
        }
        let token = value
            .to_str()
            .ok()
            .and_then(service_bearer)
            .ok_or_else(|| invalid("not a Bearer credential"))?;
        self.verify_token(token).await.map(Some)
    }

    async fn verify_token(&self, token: &str) -> Result<ServiceCaller, ApiError> {
        let jws = Jws::parse(token).ok_or_else(|| invalid("not a compact JWS"))?;
        let issuer = jws
            .payload
            .get("iss")
            .and_then(Value::as_str)
            .and_then(|iss| {
                self.issuers
                    .iter()
                    .find(|i| i.names.iter().any(|n| n == iss))
            })
            .ok_or_else(|| invalid("iss names no configured issuer"))?;
        let algorithm = jws
            .header
            .get("alg")
            .and_then(Value::as_str)
            .and_then(JwsAlgorithm::from_header)
            .filter(|alg| issuer.algorithms.contains(alg))
            .ok_or_else(|| invalid("alg is not one of the issuer's"))?;
        let kid = jws
            .header
            .get("kid")
            .and_then(Value::as_str)
            .filter(|kid| !kid.is_empty())
            .ok_or_else(|| invalid("no kid"))?;
        let now = unix_seconds(&self.clock);
        let key = self.key(issuer, kid, now).await?;
        if !key.verify(algorithm, jws.signing_input.as_bytes(), &jws.signature) {
            return Err(invalid(
                "the signature does not verify with that key and alg",
            ));
        }
        issuer
            .check_claims(&jws.payload, self.leeway, now)
            .map_err(invalid)?;
        issuer.caller(&jws.payload).ok_or_else(|| {
            tracing::debug!(issuer = %issuer.names[0], "service credential names no caller");
            ApiError::service_forbidden("Service not permitted")
        })
    }

    async fn key(&self, issuer: &Issuer, kid: &str, now: u64) -> Result<Arc<PublicKey>, ApiError> {
        let (url, bearer_token_file) = match &issuer.keys {
            Keys::Static(keys) => {
                return keys.get(kid).cloned().ok_or_else(|| invalid("unknown kid"))
            }
            Keys::Jwks {
                url,
                bearer_token_file,
            } => (url, bearer_token_file),
        };
        let Some(cache) = self.jwks.get(url) else {
            return Err(unavailable());
        };
        let mut cache = cache.lock().await;
        let held = cache
            .keys
            .as_ref()
            .is_some_and(|keys| keys.contains_key(kid));
        let stale = now.saturating_sub(cache.fetched_at) >= JWKS_MAX_AGE_SECONDS;
        let may_fetch = cache
            .attempted_at
            .is_none_or(|at| now.saturating_sub(at) >= JWKS_MIN_INTERVAL_SECONDS);
        if (!held || stale) && may_fetch {
            cache.attempted_at = Some(now);
            match self.fetch_jwks(url, bearer_token_file.as_deref()).await {
                Ok(keys) => {
                    cache.keys = Some(keys);
                    cache.fetched_at = now;
                }
                Err(err) => tracing::warn!(url, error = %err, "service auth: JWKS fetch failed"),
            }
        }
        match &cache.keys {
            None => Err(unavailable()),
            Some(keys) => keys.get(kid).cloned().ok_or_else(|| invalid("unknown kid")),
        }
    }

    async fn fetch_jwks(
        &self,
        url: &str,
        bearer_token_file: Option<&str>,
    ) -> Result<HashMap<String, Arc<PublicKey>>, String> {
        let bearer = match bearer_token_file {
            Some(path) => Some(
                std::fs::read_to_string(path)
                    .map_err(|err| format!("jwksBearerTokenFile {path}: {err}"))?
                    .trim()
                    .to_string(),
            ),
            None => None,
        };
        let body = self.fetcher.fetch(url, bearer.as_deref()).await?;
        let jwks: Value = serde_json::from_slice(&body).map_err(|err| err.to_string())?;
        let keys = jwks
            .get("keys")
            .and_then(Value::as_array)
            .ok_or_else(|| format!("{url} is not a JWKS"))?;
        // A key this runtime cannot use (another type, a use other than sig,
        // no kid, a private member) is left out, not the whole set; of two
        // keys with one kid, the first is kept.
        let mut parsed = HashMap::new();
        for jwk in keys.iter().filter_map(Value::as_object) {
            let Some(kid) = jwk.get("kid").and_then(Value::as_str) else {
                continue;
            };
            let signing = jwk.get("use").is_none_or(|u| u == "sig");
            if kid.is_empty() || !signing || parsed.contains_key(kid) {
                continue;
            }
            if let Ok(key) = PublicKey::from_jwk(jwk) {
                parsed.insert(kid.to_string(), Arc::new(key));
            }
        }
        Ok(parsed)
    }
}

#[async_trait]
impl ServiceAuthenticator for JwtServiceAuthenticator {
    async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        self.verify(&request.headers).await
    }
}

impl Issuer {
    fn new(entry: ServiceIssuer) -> Result<Self, String> {
        let name = &entry.issuer;
        if entry.audience.is_empty() || entry.algorithms.is_empty() {
            return Err(format!(
                "service auth: issuer {name:?} needs an audience and algorithms"
            ));
        }
        let keys = match (entry.jwks_url, entry.keys) {
            (Some(url), None) if !url.is_empty() => Keys::Jwks {
                url,
                bearer_token_file: entry.jwks_bearer_token_file.filter(|f| !f.is_empty()),
            },
            (None, Some(keys)) if !keys.is_empty() && entry.jwks_bearer_token_file.is_none() => {
                Keys::Static(static_keys(name, &keys)?)
            }
            _ => {
                return Err(format!(
                    "service auth: issuer {name:?} needs a jwksUrl or keys, not both, and \
                     jwksBearerTokenFile only with jwksUrl"
                ))
            }
        };
        if let Some((subject, _)) = entry.callers.iter().find(|(_, c)| c.deployable.is_empty()) {
            return Err(format!(
                "service auth: caller {subject:?} of issuer {name:?} has no deployable"
            ));
        }
        let mut names = vec![entry.issuer];
        names.extend(entry.issuer_aliases);
        Ok(Self {
            names,
            audience: entry.audience,
            algorithms: entry.algorithms,
            keys,
            subject_claim: entry.subject_claim,
            // Zero is no limit, as in Go.
            max_lifetime: entry.max_lifetime_seconds.filter(|max| *max > 0),
            callers: entry.callers,
        })
    }

    /// `exp`, `nbf`, `iat`, `aud` and the lifetime, with `leeway` seconds of
    /// clock skew.
    fn check_claims(
        &self,
        claims: &Map<String, Value>,
        leeway: u64,
        now: u64,
    ) -> Result<(), &'static str> {
        // Absent is None, present but not a number Some(None).
        let number = |name: &str| claims.get(name).map(Value::as_f64);
        let (now, leeway) = (now as f64, leeway as f64);
        let Some(Some(exp)) = number("exp") else {
            return Err("exp is missing or not a number");
        };
        if now >= exp + leeway {
            return Err("the token has expired");
        }
        let iat = match number("iat") {
            Some(None) => return Err("iat is not a number"),
            iat => iat.flatten(),
        };
        match number("nbf") {
            Some(None) => return Err("nbf is not a number"),
            Some(Some(nbf)) if now + leeway < nbf => {
                return Err("the token is not valid yet (nbf)")
            }
            _ => {}
        }
        if iat.is_some_and(|iat| now + leeway < iat) {
            return Err("the token is not valid yet (iat)");
        }
        if !self.audience_holds(claims.get("aud")) {
            return Err("aud does not hold the audience");
        }
        if let Some(max) = self.max_lifetime {
            match iat {
                None => return Err("iat is required with a maximum lifetime"),
                Some(iat) if exp - iat > max as f64 => {
                    return Err("the lifetime is over the maximum")
                }
                Some(_) => {}
            }
        }
        Ok(())
    }

    fn audience_holds(&self, aud: Option<&Value>) -> bool {
        match aud {
            Some(Value::String(aud)) => *aud == self.audience,
            Some(Value::Array(auds)) => {
                auds.iter().all(Value::is_string)
                    && auds.iter().any(|aud| aud.as_str() == Some(&self.audience))
            }
            _ => false,
        }
    }

    fn caller(&self, claims: &Map<String, Value>) -> Option<ServiceCaller> {
        let subject = claims.get(&self.subject_claim).and_then(Value::as_str)?;
        let caller = self.callers.get(subject)?;
        Some(ServiceCaller {
            deployable: caller.deployable.clone(),
            serves: caller.serves.clone(),
            subject: subject.to_string(),
        })
    }
}

fn static_keys(
    issuer: &str,
    keys: &[Map<String, Value>],
) -> Result<HashMap<String, Arc<PublicKey>>, String> {
    let mut parsed = HashMap::with_capacity(keys.len());
    for jwk in keys {
        let kid = jwk
            .get("kid")
            .and_then(Value::as_str)
            .filter(|kid| !kid.is_empty())
            .ok_or_else(|| format!("service auth: a key of issuer {issuer:?} has no kid"))?;
        let key = PublicKey::from_jwk(jwk)
            .map_err(|err| format!("service auth: key {kid:?} of issuer {issuer:?}: {err}"))?;
        if parsed.insert(kid.to_string(), Arc::new(key)).is_some() {
            return Err(format!(
                "service auth: kid {kid:?} of issuer {issuer:?} repeats"
            ));
        }
    }
    Ok(parsed)
}

/// The token of `Bearer <token>`: the scheme in any case, one space, and a
/// token with no space or tab in it.
fn service_bearer(value: &str) -> Option<&str> {
    let (scheme, token) = value.split_once(' ')?;
    (scheme.eq_ignore_ascii_case("bearer") && !token.is_empty() && !token.contains([' ', '\t']))
        .then_some(token)
}

/// A credential that does not verify. Why goes to the log, at debug, and
/// never to the caller.
fn invalid(cause: &str) -> ApiError {
    tracing::debug!(cause, "service credential refused");
    ApiError::service_unauthorized("Invalid service credential")
}

fn unavailable() -> ApiError {
    ApiError::service_unavailable("Service credential could not be checked")
}

#[cfg(feature = "http-client")]
fn default_fetcher() -> Arc<dyn KeyFetcher> {
    Arc::new(crate::HttpFetcher::new())
}

#[cfg(not(feature = "http-client"))]
fn default_fetcher() -> Arc<dyn KeyFetcher> {
    struct NoFetcher;

    #[async_trait]
    impl KeyFetcher for NoFetcher {
        async fn fetch(&self, url: &str, _bearer: Option<&str>) -> Result<Vec<u8>, String> {
            Err(format!(
                "no KeyFetcher to fetch {url}: pass one with with_key_fetcher, or build with \
                 the http-client feature"
            ))
        }
    }

    Arc::new(NoFetcher)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::jws::encode;
    use crate::jws::testing::Signer;
    use http::StatusCode;
    use serde_json::json;
    use std::sync::Mutex as StdMutex;
    use std::time::Duration;

    const NOW: u64 = 1_767_225_600;
    const JWKS_URL: &str = "https://keys.test/jwks";

    /// Answers each URL with what `answers` holds, and records each fetch.
    #[derive(Default)]
    struct StubFetcher {
        answers: StdMutex<HashMap<String, Result<Vec<u8>, String>>>,
        fetches: StdMutex<Vec<(String, Option<String>)>>,
    }

    impl StubFetcher {
        fn answer(&self, url: &str, answer: Result<Value, &str>) {
            self.answers.lock().unwrap().insert(
                url.to_string(),
                answer
                    .map(|jwks| jwks.to_string().into_bytes())
                    .map_err(ToString::to_string),
            );
        }

        fn fetch_count(&self) -> usize {
            self.fetches.lock().unwrap().len()
        }
    }

    #[async_trait]
    impl KeyFetcher for StubFetcher {
        async fn fetch(&self, url: &str, bearer: Option<&str>) -> Result<Vec<u8>, String> {
            self.fetches
                .lock()
                .unwrap()
                .push((url.to_string(), bearer.map(ToString::to_string)));
            self.answers
                .lock()
                .unwrap()
                .get(url)
                .cloned()
                .unwrap_or_else(|| Err(format!("{url} is unreachable")))
        }
    }

    struct Fixture {
        rsa: Signer,
        p256: Signer,
        ed25519: Signer,
        fetcher: Arc<StubFetcher>,
        now: Arc<StdMutex<u64>>,
    }

    impl Fixture {
        fn new() -> Self {
            let fixture = Self {
                rsa: Signer::rsa(),
                p256: Signer::p256(),
                ed25519: Signer::ed25519(9),
                fetcher: Arc::default(),
                now: Arc::new(StdMutex::new(NOW)),
            };
            fixture.fetcher.answer(
                JWKS_URL,
                Ok(json!({ "keys": [fixture.rsa.jwk("rsa-1"), fixture.p256.jwk("ec-1")] })),
            );
            fixture
        }

        /// Google-like: RS256 and ES256 from a JWKS, an alias, a lifetime
        /// cap; and a key-pair issuer with a static Ed25519 key.
        fn config(&self) -> ServiceAuthConfig {
            serde_json::from_value(json!({
                "issuers": [{
                    "issuer": "https://issuer.test",
                    "issuerAliases": ["issuer.test"],
                    "audience": "https://shop-api.test",
                    "algorithms": ["RS256", "ES256"],
                    "jwksUrl": JWKS_URL,
                    "maxLifetimeSeconds": 3600,
                    "callers": {
                        "sa-orders": { "deployable": "orders", "serves": ["shop-orders"] }
                    }
                }, {
                    "issuer": "orders",
                    "audience": "shop-api",
                    "algorithms": ["EdDSA"],
                    "keys": [self.ed25519.jwk("ed-1")],
                    "subjectClaim": "iss",
                    "callers": { "orders": { "deployable": "orders", "serves": ["shop-orders"] } }
                }]
            }))
            .unwrap()
        }

        fn authenticator(&self, config: ServiceAuthConfig) -> JwtServiceAuthenticator {
            let now = Arc::clone(&self.now);
            let fetcher: Arc<dyn KeyFetcher> = self.fetcher.clone();
            JwtServiceAuthenticator::new(config)
                .unwrap()
                .with_clock(move || UNIX_EPOCH + Duration::from_secs(*now.lock().unwrap()))
                .with_key_fetcher(fetcher)
        }

        fn set_now(&self, now: u64) {
            *self.now.lock().unwrap() = now;
        }

        fn claims(&self) -> Value {
            json!({
                "iss": "https://issuer.test",
                "aud": "https://shop-api.test",
                "sub": "sa-orders",
                "iat": NOW - 10,
                "exp": NOW + 600,
            })
        }

        fn rsa_token(&self, claims: &Value) -> String {
            self.rsa.token(
                &json!({ "alg": "RS256", "kid": "rsa-1", "typ": "JWT" }),
                claims,
            )
        }
    }

    fn headers(value: &str) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(SERVICE_AUTHORIZATION, value.parse().unwrap());
        headers
    }

    async fn verify(
        authenticator: &JwtServiceAuthenticator,
        token: &str,
    ) -> Result<ServiceCaller, (StatusCode, String)> {
        match authenticator
            .verify(&headers(&format!("Bearer {token}")))
            .await
        {
            Ok(Some(caller)) => Ok(caller),
            Ok(None) => Err((StatusCode::OK, "no caller".to_string())),
            Err(err) => Err((err.status, err.code)),
        }
    }

    fn refused(status: StatusCode, code: &str) -> Result<ServiceCaller, (StatusCode, String)> {
        Err((status, code.to_string()))
    }

    fn unauthorized() -> Result<ServiceCaller, (StatusCode, String)> {
        refused(StatusCode::UNAUTHORIZED, "service_unauthorized")
    }

    fn orders(subject: &str) -> Result<ServiceCaller, (StatusCode, String)> {
        Ok(ServiceCaller::new("orders", ["shop-orders"], subject))
    }

    #[tokio::test]
    async fn without_the_header_there_is_no_caller() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let mut headers = HeaderMap::new();
        headers.insert(
            http::header::AUTHORIZATION,
            format!("Bearer {}", fixture.rsa_token(&fixture.claims()))
                .parse()
                .unwrap(),
        );
        assert_eq!(authenticator.verify(&headers).await.unwrap(), None);
        assert_eq!(fixture.fetcher.fetch_count(), 0);
    }

    #[tokio::test]
    async fn a_good_token_names_its_caller() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let token = fixture.rsa_token(&fixture.claims());
        assert_eq!(verify(&authenticator, &token).await, orders("sa-orders"));

        let es256 = fixture
            .p256
            .token(&json!({ "alg": "ES256", "kid": "ec-1" }), &fixture.claims());
        assert_eq!(verify(&authenticator, &es256).await, orders("sa-orders"));

        // The scheme in any case.
        let value = format!("bEaReR {token}");
        assert!(authenticator
            .verify(&headers(&value))
            .await
            .unwrap()
            .is_some());
    }

    #[tokio::test]
    async fn a_header_that_is_not_one_bearer_token_is_refused() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let token = fixture.rsa_token(&fixture.claims());
        for value in [
            String::new(),
            "Bearer".to_string(),
            "Bearer ".to_string(),
            format!("Basic {token}"),
            format!("Bearer {token} extra"),
            format!("Bearer  {token}"),
            token.clone(),
        ] {
            let err = authenticator.verify(&headers(&value)).await.unwrap_err();
            assert_eq!(
                (err.status, err.code.as_str(), err.message.as_str()),
                (
                    StatusCode::UNAUTHORIZED,
                    "service_unauthorized",
                    "Invalid service credential"
                ),
                "{value:?}"
            );
        }
    }

    #[tokio::test]
    async fn two_headers_a_tab_or_an_empty_kid_are_refused() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let token = fixture.rsa_token(&fixture.claims());
        let mut two = headers(&format!("Bearer {token}"));
        two.append(
            SERVICE_AUTHORIZATION,
            format!("Bearer {token}").parse().unwrap(),
        );
        let err = authenticator.verify(&two).await.unwrap_err();
        assert_eq!(err.code, "service_unauthorized");
        let tab = headers(&format!("Bearer {token}\tx"));
        assert!(authenticator.verify(&tab).await.is_err());
        let empty_kid = fixture
            .rsa
            .token(&json!({ "alg": "RS256", "kid": "" }), &fixture.claims());
        assert_eq!(verify(&authenticator, &empty_kid).await, unauthorized());
    }

    #[tokio::test]
    async fn a_jwks_key_that_is_not_for_signing_or_repeats_is_left_out() {
        let fixture = Fixture::new();
        let other = Signer::rsa();
        let mut encryption = other.jwk("enc-1");
        encryption["use"] = json!("enc");
        let mut private = fixture.ed25519.jwk("priv-1");
        private["d"] = json!(encode(&[9; 32]));
        let rotated = Signer::p256();
        fixture.fetcher.answer(
            JWKS_URL,
            Ok(json!({ "keys": [
                encryption,
                private,
                { "kty": "oct", "kid": "hmac-1", "k": "c2VjcmV0" },
                fixture.p256.jwk("ec-1"),
                rotated.jwk("ec-1"),
            ] })),
        );
        let authenticator = fixture.authenticator(fixture.config());
        let claims = fixture.claims();
        let first = fixture
            .p256
            .token(&json!({ "alg": "ES256", "kid": "ec-1" }), &claims);
        assert_eq!(verify(&authenticator, &first).await, orders("sa-orders"));
        let second = rotated.token(&json!({ "alg": "ES256", "kid": "ec-1" }), &claims);
        assert_eq!(verify(&authenticator, &second).await, unauthorized());
        let enc = other.token(&json!({ "alg": "RS256", "kid": "enc-1" }), &claims);
        assert_eq!(verify(&authenticator, &enc).await, unauthorized());
    }

    #[tokio::test]
    async fn a_token_that_is_not_a_jws_is_refused() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let token = fixture.rsa_token(&fixture.claims());
        let (input, _) = token.rsplit_once('.').unwrap();
        for bad in [
            "abc".to_string(),
            input.to_string(),
            format!("{token}.extra"),
            format!("{}.{}.", encode(b"[]"), encode(b"{}")),
            format!("{input}.!!!"),
        ] {
            assert_eq!(verify(&authenticator, &bad).await, unauthorized(), "{bad}");
        }
    }

    #[tokio::test]
    async fn the_issuer_is_found_by_its_name_or_an_alias() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let mut claims = fixture.claims();
        claims["iss"] = json!("issuer.test");
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            orders("sa-orders")
        );
        claims["iss"] = json!("https://other.test");
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            unauthorized()
        );
        claims["iss"] = json!(7);
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            unauthorized()
        );
    }

    #[tokio::test]
    async fn the_algorithm_must_be_the_issuers_and_fit_the_key() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let claims = fixture.claims();
        let input = |header: Value| {
            format!(
                "{}.{}",
                encode(header.to_string().as_bytes()),
                encode(claims.to_string().as_bytes())
            )
        };

        // alg none, with and without a signature.
        let none = input(json!({ "alg": "none", "kid": "rsa-1" }));
        assert_eq!(
            verify(&authenticator, &format!("{none}.")).await,
            unauthorized()
        );

        // HS256 keyed with the RSA public key's bytes.
        let hs256 = input(json!({ "alg": "HS256", "kid": "rsa-1" }));
        let jwk = fixture.rsa.jwk("rsa-1");
        let secret = ring::hmac::Key::new(
            ring::hmac::HMAC_SHA256,
            jwk["n"].as_str().unwrap().as_bytes(),
        );
        let mac = ring::hmac::sign(&secret, hs256.as_bytes());
        let forged = format!("{hs256}.{}", encode(mac.as_ref()));
        assert_eq!(verify(&authenticator, &forged).await, unauthorized());

        // EdDSA is not this issuer's.
        let eddsa = fixture
            .ed25519
            .token(&json!({ "alg": "EdDSA", "kid": "rsa-1" }), &claims);
        assert_eq!(verify(&authenticator, &eddsa).await, unauthorized());

        // ES256 is the issuer's, but rsa-1 is an RSA key.
        let mismatched = fixture
            .p256
            .token(&json!({ "alg": "ES256", "kid": "rsa-1" }), &claims);
        assert_eq!(verify(&authenticator, &mismatched).await, unauthorized());
    }

    #[tokio::test]
    async fn the_kid_must_name_a_key_and_the_signature_verify() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let claims = fixture.claims();
        let no_kid = fixture.rsa.token(&json!({ "alg": "RS256" }), &claims);
        assert_eq!(verify(&authenticator, &no_kid).await, unauthorized());
        let unknown = fixture
            .rsa
            .token(&json!({ "alg": "RS256", "kid": "rsa-9" }), &claims);
        assert_eq!(verify(&authenticator, &unknown).await, unauthorized());

        let token = fixture.rsa_token(&claims);
        let (input, _) = token.rsplit_once('.').unwrap();
        let other = Signer::p256();
        let resigned = format!("{input}.{}", encode(&other.sign(input.as_bytes())));
        assert_eq!(verify(&authenticator, &resigned).await, unauthorized());

        let mut altered = fixture.claims();
        altered["sub"] = json!("sa-other");
        let (_, payload) = input.split_once('.').unwrap();
        let swapped = token.replace(payload, &encode(altered.to_string().as_bytes()));
        assert_eq!(verify(&authenticator, &swapped).await, unauthorized());
    }

    #[tokio::test]
    async fn an_ed25519_alg_reads_as_eddsa() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let claims = json!({ "iss": "orders", "aud": "shop-api", "iat": NOW, "exp": NOW + 300 });
        for alg in ["EdDSA", "Ed25519"] {
            let token = fixture
                .ed25519
                .token(&json!({ "alg": alg, "kid": "ed-1" }), &claims);
            assert_eq!(
                verify(&authenticator, &token).await,
                orders("orders"),
                "{alg}"
            );
        }
    }

    #[tokio::test]
    async fn the_claims_hold_within_the_leeway() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let check = |edit: &dyn Fn(&mut Value)| {
            let mut claims = fixture.claims();
            edit(&mut claims);
            fixture.rsa_token(&claims)
        };
        let cases: Vec<(&str, String, bool)> = vec![
            (
                "exp missing",
                check(&|c| {
                    c.as_object_mut().unwrap().remove("exp");
                }),
                false,
            ),
            ("exp a string", check(&|c| c["exp"] = json!("soon")), false),
            (
                "exp past the leeway",
                check(&|c| c["exp"] = json!(NOW - 60)),
                false,
            ),
            (
                "exp within the leeway",
                check(&|c| c["exp"] = json!(NOW - 59)),
                true,
            ),
            (
                "nbf past the leeway",
                check(&|c| c["nbf"] = json!(NOW + 61)),
                false,
            ),
            (
                "nbf within the leeway",
                check(&|c| c["nbf"] = json!(NOW + 60)),
                true,
            ),
            (
                "iat in the future",
                check(&|c| c["iat"] = json!(NOW + 61)),
                false,
            ),
            (
                "aud another",
                check(&|c| c["aud"] = json!("https://other.test")),
                false,
            ),
            (
                "aud missing",
                check(&|c| {
                    c.as_object_mut().unwrap().remove("aud");
                }),
                false,
            ),
            (
                "aud an array with ours",
                check(&|c| c["aud"] = json!(["x", "https://shop-api.test"])),
                true,
            ),
            (
                "aud an array without ours",
                check(&|c| c["aud"] = json!(["x"])),
                false,
            ),
            (
                "lifetime over the max",
                check(&|c| {
                    c["iat"] = json!(NOW - 3000);
                    c["exp"] = json!(NOW + 601);
                }),
                false,
            ),
            (
                "lifetime at the max",
                check(&|c| {
                    c["iat"] = json!(NOW - 3000);
                    c["exp"] = json!(NOW + 600);
                }),
                true,
            ),
            (
                "no iat with a max",
                check(&|c| {
                    c.as_object_mut().unwrap().remove("iat");
                }),
                false,
            ),
        ];
        for (name, token, good) in cases {
            let want = if good {
                orders("sa-orders")
            } else {
                unauthorized()
            };
            assert_eq!(verify(&authenticator, &token).await, want, "{name}");
        }
    }

    #[tokio::test]
    async fn the_leeway_and_lifetime_follow_the_config() {
        let fixture = Fixture::new();
        let mut config = fixture.config();
        config.leeway_seconds = 0;
        config.issuers[0].max_lifetime_seconds = None;
        let authenticator = fixture.authenticator(config);
        let mut claims = fixture.claims();
        claims["exp"] = json!(NOW);
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            unauthorized()
        );
        claims["exp"] = json!(NOW + 1);
        claims.as_object_mut().unwrap().remove("iat");
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            orders("sa-orders")
        );
    }

    #[tokio::test]
    async fn an_identity_that_is_no_caller_is_forbidden() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let mut claims = fixture.claims();
        claims["sub"] = json!("sa-stranger");
        let err = authenticator
            .verify(&headers(&format!("Bearer {}", fixture.rsa_token(&claims))))
            .await
            .unwrap_err();
        assert_eq!(
            (err.status, err.code.as_str(), err.message.as_str()),
            (
                StatusCode::FORBIDDEN,
                "service_forbidden",
                "Service not permitted"
            )
        );
        claims.as_object_mut().unwrap().remove("sub");
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            refused(StatusCode::FORBIDDEN, "service_forbidden")
        );
    }

    #[tokio::test]
    async fn the_jwks_is_fetched_once_and_kept() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let token = fixture.rsa_token(&fixture.claims());
        for _ in 0..3 {
            assert_eq!(verify(&authenticator, &token).await, orders("sa-orders"));
        }
        assert_eq!(fixture.fetcher.fetch_count(), 1);
        assert_eq!(
            fixture.fetcher.fetches.lock().unwrap()[0],
            (JWKS_URL.to_string(), None)
        );
    }

    #[tokio::test]
    async fn an_unknown_kid_fetches_again_at_most_once_a_minute() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let claims = fixture.claims();
        assert!(verify(&authenticator, &fixture.rsa_token(&claims))
            .await
            .is_ok());

        // The issuer rotates in a key the cache does not hold yet.
        let rotated = Signer::p256();
        fixture.fetcher.answer(
            JWKS_URL,
            Ok(json!({ "keys": [fixture.rsa.jwk("rsa-1"), rotated.jwk("ec-2")] })),
        );
        let token = rotated.token(&json!({ "alg": "ES256", "kid": "ec-2" }), &claims);
        fixture.set_now(NOW + 30);
        assert_eq!(verify(&authenticator, &token).await, unauthorized());
        assert_eq!(fixture.fetcher.fetch_count(), 1);
        fixture.set_now(NOW + 60);
        assert_eq!(verify(&authenticator, &token).await, orders("sa-orders"));
        assert_eq!(fixture.fetcher.fetch_count(), 2);
        // ec-1 left the set with that fetch.
        let old = fixture
            .p256
            .token(&json!({ "alg": "ES256", "kid": "ec-1" }), &claims);
        assert_eq!(verify(&authenticator, &old).await, unauthorized());
    }

    #[tokio::test]
    async fn an_hour_old_jwks_is_fetched_again_and_kept_when_that_fails() {
        let fixture = Fixture::new();
        let authenticator = fixture.authenticator(fixture.config());
        let mut claims = fixture.claims();
        assert!(verify(&authenticator, &fixture.rsa_token(&claims))
            .await
            .is_ok());

        fixture.fetcher.answer(JWKS_URL, Err("down"));
        let later = NOW + 3600;
        fixture.set_now(later);
        claims["iat"] = json!(later);
        claims["exp"] = json!(later + 600);
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&claims)).await,
            orders("sa-orders")
        );
        assert_eq!(fixture.fetcher.fetch_count(), 2);
        // The failed attempt counts: no fetch again within the minute.
        assert!(verify(&authenticator, &fixture.rsa_token(&claims))
            .await
            .is_ok());
        assert_eq!(fixture.fetcher.fetch_count(), 2);
    }

    #[tokio::test]
    async fn without_keys_a_jwks_that_cannot_be_fetched_is_503() {
        let fixture = Fixture::new();
        fixture.fetcher.answer(JWKS_URL, Err("down"));
        let authenticator = fixture.authenticator(fixture.config());
        let err = authenticator
            .verify(&headers(&format!(
                "Bearer {}",
                fixture.rsa_token(&fixture.claims())
            )))
            .await
            .unwrap_err();
        assert_eq!(
            (err.status, err.code.as_str(), err.message.as_str()),
            (
                StatusCode::SERVICE_UNAVAILABLE,
                "service_unavailable",
                "Service credential could not be checked"
            )
        );

        // A body that is not a JWKS fails the same way.
        let fixture = Fixture::new();
        fixture
            .fetcher
            .answer(JWKS_URL, Ok(json!({ "nokeys": [] })));
        let authenticator = fixture.authenticator(fixture.config());
        assert_eq!(
            verify(&authenticator, &fixture.rsa_token(&fixture.claims())).await,
            refused(StatusCode::SERVICE_UNAVAILABLE, "service_unavailable")
        );
    }

    #[tokio::test]
    async fn a_jwks_fetch_sends_the_bearer_token_file() {
        let fixture = Fixture::new();
        let dir = std::env::temp_dir().join(format!("sa-jwks-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("token");
        std::fs::write(&file, "k8s-token\n").unwrap();
        let mut config = fixture.config();
        config.issuers[0].jwks_bearer_token_file = Some(file.display().to_string());
        let authenticator = fixture.authenticator(config);
        assert!(
            verify(&authenticator, &fixture.rsa_token(&fixture.claims()))
                .await
                .is_ok()
        );
        assert_eq!(
            fixture.fetcher.fetches.lock().unwrap()[0],
            (JWKS_URL.to_string(), Some("k8s-token".to_string()))
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn a_config_that_cannot_be_checked_is_refused() {
        let fixture = Fixture::new();
        let refused = |edit: &dyn Fn(&mut ServiceAuthConfig)| {
            let mut config = fixture.config();
            edit(&mut config);
            JwtServiceAuthenticator::new(config).err()
        };
        assert!(refused(&|_| {}).is_none());
        let both = |c: &mut ServiceAuthConfig| c.issuers[1].jwks_url = Some(JWKS_URL.to_string());
        assert!(refused(&both).is_some());
        let neither = |c: &mut ServiceAuthConfig| c.issuers[0].jwks_url = None;
        assert!(refused(&neither).is_some());
        let file_with_keys = |c: &mut ServiceAuthConfig| {
            c.issuers[1].jwks_bearer_token_file = Some("/token".to_string());
        };
        assert!(refused(&file_with_keys).is_some());
        let no_kid = |c: &mut ServiceAuthConfig| {
            c.issuers[1].keys.as_mut().unwrap()[0].remove("kid");
        };
        assert!(refused(&no_kid).is_some());
        let private = |c: &mut ServiceAuthConfig| {
            c.issuers[1].keys.as_mut().unwrap()[0].insert("d".to_string(), json!("AA"));
        };
        assert!(refused(&private).is_some());
        let unreadable = |c: &mut ServiceAuthConfig| {
            c.issuers[1].keys.as_mut().unwrap()[0].insert("x".to_string(), json!("AA"));
        };
        assert!(refused(&unreadable).is_some());
        let twice = |c: &mut ServiceAuthConfig| {
            c.issuers[1].issuer_aliases = vec!["issuer.test".to_string()];
        };
        assert!(refused(&twice).is_some());
        let no_audience = |c: &mut ServiceAuthConfig| c.issuers[0].audience.clear();
        assert!(refused(&no_audience).is_some());
        let empty_alias =
            |c: &mut ServiceAuthConfig| c.issuers[0].issuer_aliases.push(String::new());
        assert!(refused(&empty_alias).is_some());
        let no_deployable = |c: &mut ServiceAuthConfig| {
            c.issuers[0]
                .callers
                .get_mut("sa-orders")
                .unwrap()
                .deployable
                .clear();
        };
        assert!(refused(&no_deployable).is_some());
        let repeated_kid = |c: &mut ServiceAuthConfig| {
            let keys = c.issuers[1].keys.as_mut().unwrap();
            keys.push(keys[0].clone());
        };
        assert!(refused(&repeated_kid).is_some());
    }

    #[cfg(not(feature = "http-client"))]
    #[tokio::test]
    async fn without_a_fetcher_a_jwks_issuer_answers_503() {
        let fixture = Fixture::new();
        let authenticator = JwtServiceAuthenticator::new(fixture.config()).unwrap();
        let token = fixture.rsa_token(&fixture.claims());
        let err = authenticator
            .verify(&headers(&format!("Bearer {token}")))
            .await
            .unwrap_err();
        assert_eq!(err.status, StatusCode::SERVICE_UNAVAILABLE);
    }
}
