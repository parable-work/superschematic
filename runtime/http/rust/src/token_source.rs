//! Service credential sources (D37): how a calling service gets the token it
//! sends in `Service-Authorization`, one per row of section 9.2 of
//! `docs/stack-model.md`. Each builds a [`TokenSource`], a function from a
//! `fresh` flag to a token: `fresh` asks for a new one, as an SDK does once
//! after a 401 `service_unauthorized`. Each caches its token, and a failure
//! is an `Err` with a message for logs.
//!
//! - [`GoogleIdTokenSource`]: a Google ID token from the metadata server,
//!   for Cloud Run.
//! - [`TokenFileSource`]: a token the platform keeps current in a file, for
//!   a Kubernetes projected service account token.
//! - [`SignedKeySource`]: a token signed with the edge's Ed25519 key, for
//!   the generic connector and the `local` target.

use crate::jws::{self, decode, encode};
use crate::jwt::{unix_seconds, Clock};
use async_trait::async_trait;
use ring::rand::{SecureRandom, SystemRandom};
use ring::signature::Ed25519KeyPair;
use serde::Serialize;
use serde_json::Value;
use std::future::Future;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::Arc;
use std::time::SystemTime;
use tokio::sync::Mutex;

/// What a [`TokenSource`] returns.
pub type TokenFuture = Pin<Box<dyn Future<Output = Result<String, String>> + Send>>;

/// A service credential source: `fresh` asks for a new token instead of the
/// cached one. Clone it freely; the clones share one cache.
pub type TokenSource = Arc<dyn Fn(bool) -> TokenFuture + Send + Sync>;

/// A plain HTTP GET, for the Google metadata server.
#[async_trait]
pub trait HttpGetter: Send + Sync + 'static {
    /// The body of a GET of `url` with `headers`; an `Err` for a failed
    /// request or an answer that is not 2xx.
    async fn get(&self, url: &str, headers: &[(&str, &str)]) -> Result<Vec<u8>, String>;
}

/// The metadata server's host when `GCE_METADATA_HOST` is not set.
const METADATA_HOST: &str = "metadata.google.internal";
/// A Google ID token is fetched again this long before it expires.
const GOOGLE_REFRESH_MARGIN_SECONDS: u64 = 5 * 60;
/// A token file is read again when the token read is this old.
const TOKEN_FILE_MAX_AGE_SECONDS: u64 = 60;
/// A signed token lives this long.
const SIGNED_LIFETIME_SECONDS: u64 = 5 * 60;
/// A signed token is signed again when less than this remains.
const SIGNED_REFRESH_MARGIN_SECONDS: u64 = 60;

fn source<S, F>(state: Arc<S>, token: F) -> TokenSource
where
    S: Send + Sync + 'static,
    F: Fn(Arc<S>, bool) -> TokenFuture + Send + Sync + 'static,
{
    Arc::new(move |fresh| token(Arc::clone(&state), fresh))
}

/// A Google ID token whose audience is the callee's URL, from the metadata
/// server of the Cloud Run service (or GCE instance) the caller runs on:
/// `GET http://<host>/computeMetadata/v1/instance/service-accounts/default/identity?audience=<audience>`
/// with `Metadata-Flavor: Google`, the host from `GCE_METADATA_HOST` when it
/// is set. The token is kept until 5 minutes before its `exp`.
///
/// The request goes through an [`HttpGetter`]: the `http-client` feature's
/// `HttpFetcher` by default with that feature on; without it, pass one with
/// [`GoogleIdTokenSource::with_getter`], or each fetch fails.
pub struct GoogleIdTokenSource {
    audience: String,
    host: String,
    getter: Arc<dyn HttpGetter>,
    clock: Clock,
}

impl GoogleIdTokenSource {
    pub fn new(audience: impl Into<String>) -> Self {
        Self {
            audience: audience.into(),
            host: std::env::var("GCE_METADATA_HOST")
                .ok()
                .filter(|host| !host.is_empty())
                .unwrap_or_else(|| METADATA_HOST.to_string()),
            getter: default_getter(),
            clock: Arc::new(SystemTime::now),
        }
    }

    /// The metadata server's `host[:port]`, in place of `GCE_METADATA_HOST`.
    #[must_use]
    pub fn with_metadata_host(mut self, host: impl Into<String>) -> Self {
        self.host = host.into();
        self
    }

    #[must_use]
    pub fn with_getter(mut self, getter: Arc<dyn HttpGetter>) -> Self {
        self.getter = getter;
        self
    }

    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    pub fn build(self) -> TokenSource {
        let url = format!(
            "http://{}/computeMetadata/v1/instance/service-accounts/default/identity?audience={}",
            self.host,
            query_escape(&self.audience)
        );
        let state = Arc::new(GoogleState {
            url,
            getter: self.getter,
            clock: self.clock,
            cached: Mutex::new(None),
        });
        source(state, |state, fresh| {
            Box::pin(async move { state.token(fresh).await })
        })
    }
}

struct GoogleState {
    url: String,
    getter: Arc<dyn HttpGetter>,
    clock: Clock,
    /// The token and its `exp`.
    cached: Mutex<Option<(String, u64)>>,
}

impl GoogleState {
    async fn token(&self, fresh: bool) -> Result<String, String> {
        let mut cached = self.cached.lock().await;
        let now = unix_seconds(&self.clock);
        if let Some((token, exp)) = cached.as_ref() {
            if !fresh && now + GOOGLE_REFRESH_MARGIN_SECONDS < *exp {
                return Ok(token.clone());
            }
        }
        let body = self
            .getter
            .get(&self.url, &[("Metadata-Flavor", "Google")])
            .await
            .map_err(|err| format!("Google ID token: {err}"))?;
        let token = String::from_utf8(body)
            .map_err(|_| "Google ID token: the answer is not text".to_string())?
            .trim()
            .to_string();
        let exp = token_expiry(&token)
            .ok_or_else(|| "Google ID token: the answer is not a JWT with an exp".to_string())?;
        *cached = Some((token.clone(), exp));
        Ok(token)
    }
}

/// The `exp` of a JWT, read without verifying it.
fn token_expiry(token: &str) -> Option<u64> {
    let payload = token.split('.').nth(1)?;
    let exp = jws::object(payload)?.get("exp")?.as_f64()?;
    (exp >= 0.0).then_some(exp as u64)
}

/// Percent-encodes all but the unreserved characters of RFC 3986.
fn query_escape(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || b"-._~".contains(&byte) {
            out.push(char::from(byte));
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

/// A token read from a file that the platform keeps current, such as a
/// Kubernetes projected service account token, which the kubelet replaces
/// at 80% of its lifetime. The file is read again when the token read is a
/// minute old, or on `fresh`; its contents are trimmed.
pub struct TokenFileSource {
    path: PathBuf,
    clock: Clock,
}

impl TokenFileSource {
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self {
            path: path.into(),
            clock: Arc::new(SystemTime::now),
        }
    }

    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    pub fn build(self) -> TokenSource {
        let state = Arc::new(FileState {
            path: self.path,
            clock: self.clock,
            cached: std::sync::Mutex::new(None),
        });
        source(state, |state, fresh| {
            Box::pin(async move { state.token(fresh) })
        })
    }
}

struct FileState {
    path: PathBuf,
    clock: Clock,
    /// The token and when it was read.
    cached: std::sync::Mutex<Option<(String, u64)>>,
}

impl FileState {
    fn token(&self, fresh: bool) -> Result<String, String> {
        let now = unix_seconds(&self.clock);
        let mut cached = self
            .cached
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some((token, read_at)) = cached.as_ref() {
            if !fresh && now.saturating_sub(*read_at) < TOKEN_FILE_MAX_AGE_SECONDS {
                return Ok(token.clone());
            }
        }
        let path = self.path.display();
        let token = std::fs::read_to_string(&self.path)
            .map_err(|err| format!("token file {path}: {err}"))?
            .trim()
            .to_string();
        if token.is_empty() {
            return Err(format!("token file {path} is empty"));
        }
        *cached = Some((token.clone(), now));
        Ok(token)
    }
}

/// A compact JWS the caller signs with its edge's Ed25519 key, for the
/// generic connector and the `local` target. The header is
/// `{"alg":"EdDSA","kid":<kid>,"typ":"JWT"}` and the claims are `iss`,
/// `sub`, `aud`, `iat`, `exp` (5 minutes after `iat`) and `jti` (16 random
/// bytes, base64url), in that order. A new token is signed when the one held
/// has less than a minute left, or on `fresh`.
pub struct SignedKeySource {
    signer: Ed25519KeyPair,
    kid: String,
    issuer: String,
    subject: String,
    audience: String,
    clock: Clock,
}

impl SignedKeySource {
    /// `private_jwk` is the edge's private key, `{"kty": "OKP", "crv":
    /// "Ed25519", "d", "x", "kid"}`.
    pub fn new(
        private_jwk: &Value,
        issuer: impl Into<String>,
        subject: impl Into<String>,
        audience: impl Into<String>,
    ) -> Result<Self, String> {
        let member = |name: &str| {
            private_jwk
                .get(name)
                .and_then(Value::as_str)
                .ok_or_else(|| format!("the signing key has no {name}"))
        };
        if member("kty")? != "OKP" || member("crv")? != "Ed25519" {
            return Err("the signing key is not an Ed25519 key (kty OKP, crv Ed25519)".to_string());
        }
        let d = decode(member("d")?).ok_or("the signing key's d is not base64url")?;
        let x = decode(member("x")?).ok_or("the signing key's x is not base64url")?;
        let signer = Ed25519KeyPair::from_seed_and_public_key(&d, &x)
            .map_err(|err| format!("the signing key: {err}"))?;
        Ok(Self {
            signer,
            kid: member("kid")?.to_string(),
            issuer: issuer.into(),
            subject: subject.into(),
            audience: audience.into(),
            clock: Arc::new(SystemTime::now),
        })
    }

    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    pub fn build(self) -> TokenSource {
        let state = Arc::new(SignedState {
            key: self,
            random: SystemRandom::new(),
            cached: std::sync::Mutex::new(None),
        });
        source(state, |state, fresh| {
            Box::pin(async move { state.token(fresh) })
        })
    }
}

struct SignedState {
    key: SignedKeySource,
    random: SystemRandom,
    /// The token and its `exp`.
    cached: std::sync::Mutex<Option<(String, u64)>>,
}

#[derive(Serialize)]
struct SignedHeader<'a> {
    alg: &'static str,
    kid: &'a str,
    typ: &'static str,
}

#[derive(Serialize)]
struct SignedClaims<'a> {
    iss: &'a str,
    sub: &'a str,
    aud: &'a str,
    iat: u64,
    exp: u64,
    jti: String,
}

impl SignedState {
    fn token(&self, fresh: bool) -> Result<String, String> {
        let now = unix_seconds(&self.key.clock);
        let mut cached = self
            .cached
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some((token, exp)) = cached.as_ref() {
            if !fresh && now + SIGNED_REFRESH_MARGIN_SECONDS <= *exp {
                return Ok(token.clone());
            }
        }
        let exp = now + SIGNED_LIFETIME_SECONDS;
        let token = self.sign(now, exp)?;
        *cached = Some((token.clone(), exp));
        Ok(token)
    }

    fn sign(&self, iat: u64, exp: u64) -> Result<String, String> {
        let mut jti = [0u8; 16];
        self.random
            .fill(&mut jti)
            .map_err(|_| "no random bytes for the jti".to_string())?;
        let key = &self.key;
        let header = SignedHeader {
            alg: "EdDSA",
            kid: &key.kid,
            typ: "JWT",
        };
        let claims = SignedClaims {
            iss: &key.issuer,
            sub: &key.subject,
            aud: &key.audience,
            iat,
            exp,
            jti: encode(&jti),
        };
        let header = serde_json::to_string(&header).map_err(|err| err.to_string())?;
        let claims = serde_json::to_string(&claims).map_err(|err| err.to_string())?;
        let input = format!(
            "{}.{}",
            encode(header.as_bytes()),
            encode(claims.as_bytes())
        );
        let signature = key.signer.sign(input.as_bytes());
        Ok(format!("{input}.{}", encode(signature.as_ref())))
    }
}

#[cfg(feature = "http-client")]
fn default_getter() -> Arc<dyn HttpGetter> {
    Arc::new(crate::HttpFetcher::new())
}

#[cfg(not(feature = "http-client"))]
fn default_getter() -> Arc<dyn HttpGetter> {
    struct NoGetter;

    #[async_trait]
    impl HttpGetter for NoGetter {
        async fn get(&self, url: &str, _headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
            Err(format!(
                "no HttpGetter to fetch {url}: pass one with with_getter, or build with the \
                 http-client feature"
            ))
        }
    }

    Arc::new(NoGetter)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::jws::testing::Signer;
    use crate::{JwtServiceAuthenticator, ServiceAuthConfig, ServiceCaller, SERVICE_AUTHORIZATION};
    use http::HeaderMap;
    use ring::signature::KeyPair;
    use serde_json::json;
    use std::sync::Mutex as StdMutex;
    use std::time::{Duration, UNIX_EPOCH};

    const NOW: u64 = 1_767_225_600;

    type Requests = Arc<StdMutex<Vec<(String, Vec<(String, String)>)>>>;

    /// Answers each GET with the next of `answers`, recording the request.
    struct StubGetter {
        answers: StdMutex<Vec<Result<String, String>>>,
        requests: Requests,
    }

    #[async_trait]
    impl HttpGetter for StubGetter {
        async fn get(&self, url: &str, headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
            self.requests.lock().unwrap().push((
                url.to_string(),
                headers
                    .iter()
                    .map(|(name, value)| (name.to_string(), value.to_string()))
                    .collect(),
            ));
            self.answers
                .lock()
                .unwrap()
                .remove(0)
                .map(String::into_bytes)
        }
    }

    fn clock(now: &Arc<StdMutex<u64>>) -> impl Fn() -> SystemTime + Send + Sync + 'static {
        let now = Arc::clone(now);
        move || UNIX_EPOCH + Duration::from_secs(*now.lock().unwrap())
    }

    /// An unsigned JWT with this `exp`; the source does not verify it.
    fn id_token(exp: u64) -> String {
        format!(
            "{}.{}.sig",
            encode(br#"{"alg":"RS256"}"#),
            encode(json!({ "exp": exp }).to_string().as_bytes())
        )
    }

    #[tokio::test]
    async fn a_google_id_token_is_asked_of_the_metadata_server_and_kept() {
        let now = Arc::new(StdMutex::new(NOW));
        let requests = Requests::default();
        let getter = StubGetter {
            answers: StdMutex::new(vec![
                Ok(format!("{}\n", id_token(NOW + 3600))),
                Ok(id_token(NOW + 7200)),
                Ok(id_token(NOW + 9000)),
                Err("down".to_string()),
                Ok("not-a-jwt".to_string()),
            ]),
            requests: requests.clone(),
        };
        let source = GoogleIdTokenSource::new("https://shop-api-abc.a.run.app/x y")
            .with_metadata_host("169.254.169.254:8080")
            .with_getter(Arc::new(getter))
            .with_clock(clock(&now))
            .build();

        assert_eq!(source(false).await.unwrap(), id_token(NOW + 3600));
        assert_eq!(
            requests.lock().unwrap()[0],
            (
                "http://169.254.169.254:8080/computeMetadata/v1/instance/service-accounts/default/identity?audience=https%3A%2F%2Fshop-api-abc.a.run.app%2Fx%20y".to_string(),
                vec![("Metadata-Flavor".to_string(), "Google".to_string())]
            )
        );
        // Kept until five minutes before exp.
        *now.lock().unwrap() = NOW + 3600 - 301;
        assert_eq!(source(false).await.unwrap(), id_token(NOW + 3600));
        assert_eq!(requests.lock().unwrap().len(), 1);
        *now.lock().unwrap() = NOW + 3600 - 300;
        assert_eq!(source(false).await.unwrap(), id_token(NOW + 7200));
        // fresh asks again.
        assert_eq!(source(true).await.unwrap(), id_token(NOW + 9000));
        assert!(source(true).await.unwrap_err().contains("down"));
        assert!(source(true).await.unwrap_err().contains("not a JWT"));
        assert_eq!(requests.lock().unwrap().len(), 5);
        // A failure leaves the token held.
        assert_eq!(source(false).await.unwrap(), id_token(NOW + 9000));
    }

    #[test]
    fn the_metadata_host_defaults_to_google_internal() {
        if std::env::var_os("GCE_METADATA_HOST").is_none() {
            assert_eq!(
                GoogleIdTokenSource::new("a").host,
                "metadata.google.internal"
            );
        }
    }

    #[tokio::test]
    async fn a_token_file_is_read_again_after_a_minute_or_when_fresh() {
        let dir = std::env::temp_dir().join(format!("sa-token-file-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("token");
        std::fs::write(&file, "one\n").unwrap();
        let now = Arc::new(StdMutex::new(NOW));
        let source = TokenFileSource::new(&file).with_clock(clock(&now)).build();

        assert_eq!(source(false).await.unwrap(), "one");
        std::fs::write(&file, "two").unwrap();
        *now.lock().unwrap() = NOW + 59;
        assert_eq!(source(false).await.unwrap(), "one");
        *now.lock().unwrap() = NOW + 60;
        assert_eq!(source(false).await.unwrap(), "two");
        std::fs::write(&file, "three").unwrap();
        assert_eq!(source(true).await.unwrap(), "three");

        std::fs::write(&file, " \n").unwrap();
        assert!(source(true).await.unwrap_err().contains("empty"));
        std::fs::remove_dir_all(&dir).unwrap();
        assert!(source(true).await.is_err());
    }

    fn private_jwk(seed: u8) -> Value {
        let pair = Ed25519KeyPair::from_seed_unchecked(&[seed; 32]).unwrap();
        json!({
            "kty": "OKP", "crv": "Ed25519", "kid": "edge-1",
            "d": encode(&[seed; 32]), "x": encode(pair.public_key().as_ref()),
        })
    }

    #[tokio::test]
    async fn a_signed_key_token_verifies_against_the_edges_public_key() {
        let now = Arc::new(StdMutex::new(NOW));
        let source = SignedKeySource::new(&private_jwk(3), "orders", "orders", "shop-api")
            .unwrap()
            .with_clock(clock(&now))
            .build();
        let token = source(false).await.unwrap();

        let segments: Vec<&str> = token.split('.').collect();
        assert_eq!(
            decode(segments[0]).unwrap(),
            br#"{"alg":"EdDSA","kid":"edge-1","typ":"JWT"}"#
        );
        let claims = String::from_utf8(decode(segments[1]).unwrap()).unwrap();
        let jti = jws::object(segments[1]).unwrap()["jti"]
            .as_str()
            .unwrap()
            .to_string();
        assert_eq!(decode(&jti).unwrap().len(), 16);
        assert_eq!(
            claims,
            format!(
                r#"{{"iss":"orders","sub":"orders","aud":"shop-api","iat":{NOW},"exp":{},"jti":"{jti}"}}"#,
                NOW + 300
            )
        );

        let public = Signer::ed25519(3).jwk("edge-1");
        let config: ServiceAuthConfig = serde_json::from_value(json!({
            "issuers": [{
                "issuer": "orders", "audience": "shop-api", "algorithms": ["EdDSA"],
                "keys": [public], "subjectClaim": "iss", "maxLifetimeSeconds": 300,
                "callers": { "orders": { "deployable": "orders", "serves": ["shop-orders"] } }
            }]
        }))
        .unwrap();
        let authenticator = JwtServiceAuthenticator::new(config)
            .unwrap()
            .with_clock(clock(&now));
        let mut headers = HeaderMap::new();
        headers.insert(
            SERVICE_AUTHORIZATION,
            format!("Bearer {token}").parse().unwrap(),
        );
        assert_eq!(
            authenticator.verify(&headers).await.unwrap(),
            Some(ServiceCaller::new("orders", ["shop-orders"], "orders"))
        );
    }

    #[tokio::test]
    async fn a_signed_key_token_is_signed_again_with_under_a_minute_left_or_when_fresh() {
        let now = Arc::new(StdMutex::new(NOW));
        let source = SignedKeySource::new(&private_jwk(4), "orders", "orders", "shop-api")
            .unwrap()
            .with_clock(clock(&now))
            .build();
        let first = source(false).await.unwrap();
        *now.lock().unwrap() = NOW + 240;
        assert_eq!(source(false).await.unwrap(), first);
        *now.lock().unwrap() = NOW + 241;
        let second = source(false).await.unwrap();
        assert_ne!(second, first);
        let third = source(true).await.unwrap();
        assert_ne!(third, second, "fresh signs again, with a new jti");
    }

    #[test]
    fn a_signing_key_that_is_not_an_ed25519_private_jwk_is_refused() {
        let jwk = private_jwk(5);
        let refused = |edit: &dyn Fn(&mut Value)| {
            let mut jwk = jwk.clone();
            edit(&mut jwk);
            SignedKeySource::new(&jwk, "a", "a", "b").err()
        };
        assert!(refused(&|_| {}).is_none());
        assert!(refused(&|j| j["kty"] = json!("EC")).is_some());
        assert!(refused(&|j| j["crv"] = json!("X25519")).is_some());
        assert!(refused(&|j| {
            j.as_object_mut().unwrap().remove("kid");
        })
        .is_some());
        assert!(refused(&|j| {
            j.as_object_mut().unwrap().remove("d");
        })
        .is_some());
        assert!(refused(&|j| j["x"] = private_jwk(6)["x"].clone()).is_some());
        assert!(refused(&|j| j["d"] = json!("short")).is_some());
    }
}
