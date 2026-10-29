//! The JWT body-hash webhook verifier (D81). The parity vectors are
//! `runtime/http/testdata/webhook_jwt_parity.json`, which the Go runtime
//! writes (`go test ./webhook -run TestWriteParityVectors -update` in
//! `runtime/http/go`) and the Go, TypeScript and Rust runtimes each run;
//! its comment states the harness. Every key is a P-256 key generated for
//! the run: none is committed.

use async_trait::async_trait;
use axum::body::{Body, Bytes};
use axum::extract::Request;
use axum::middleware::Next;
use axum::routing::post;
use axum::Router;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ring::rand::SystemRandom;
use ring::signature::{EcdsaKeyPair, KeyPair, ECDSA_P256_SHA256_FIXED_SIGNING};
use serde::Deserialize;
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::path::Path;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, UNIX_EPOCH};
use superschematic_http_runtime::{
    JwsAlgorithm, JwtBodyHashOptions, JwtBodyHashVerifier, KeyFetcher, WebhookKeys,
    INVALID_SIGNATURE_CODE,
};
use tower::ServiceExt;

const VECTORS: &str = "../testdata/webhook_jwt_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    verifier: ParityVerifier,
    vectors: Vec<Vector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct ParityVerifier {
    header: String,
    alg: JwsAlgorithm,
    max_age_seconds: u64,
    clock_skew_seconds: u64,
    body_hash_claim: String,
    max_body_bytes: usize,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Vector {
    name: String,
    now: u64,
    keys: Vec<String>,
    lookup_fails: bool,
    token: Option<Token>,
    body: String,
    want: Want,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Token {
    #[serde(default)]
    raw: Option<String>,
    #[serde(default)]
    #[allow(dead_code)]
    header: Option<Value>,
    #[serde(default)]
    #[allow(dead_code)]
    claims: Option<Value>,
    #[serde(default)]
    signing_input: String,
    #[serde(default)]
    signed: String,
    #[serde(default)]
    signer: String,
    #[serde(default)]
    tamper: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Want {
    status: u16,
    code: Option<String>,
}

fn corpus() -> ParityFile {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data =
        std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
    serde_json::from_str(&data).expect("decode the webhook vectors")
}

/// A P-256 key generated for this run.
struct TestKey {
    pair: EcdsaKeyPair,
    rng: SystemRandom,
}

impl TestKey {
    fn new() -> Self {
        let rng = SystemRandom::new();
        let pkcs8 = EcdsaKeyPair::generate_pkcs8(&ECDSA_P256_SHA256_FIXED_SIGNING, &rng).unwrap();
        let pair = EcdsaKeyPair::from_pkcs8(&ECDSA_P256_SHA256_FIXED_SIGNING, pkcs8.as_ref(), &rng)
            .unwrap();
        Self { pair, rng }
    }

    /// The public JWK under `kid`, as Plaid's key endpoint answers it.
    fn jwk(&self, kid: &str) -> Value {
        let point = self.pair.public_key().as_ref();
        json!({
            "kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256",
            "x": URL_SAFE_NO_PAD.encode(&point[1..33]),
            "y": URL_SAFE_NO_PAD.encode(&point[33..]),
        })
    }

    /// The ES256 signature of `input`: r||s, ring's fixed form.
    fn sign(&self, input: &str) -> Vec<u8> {
        self.pair
            .sign(&self.rng, input.as_bytes())
            .unwrap()
            .as_ref()
            .to_vec()
    }
}

fn token_of(token: &Token, trusted: &TestKey, stranger: &TestKey) -> String {
    if let Some(raw) = &token.raw {
        return raw.clone();
    }
    let signed = if token.signed.is_empty() {
        &token.signing_input
    } else {
        &token.signed
    };
    let mut signature = match token.signer.as_str() {
        "trusted" => trusted.sign(signed),
        "stranger" => stranger.sign(signed),
        "none" => Vec::new(),
        other => panic!("unknown signer {other}"),
    };
    match token.tamper.as_str() {
        "" => {}
        "flip" => *signature.last_mut().unwrap() ^= 1,
        "truncate" => {
            signature.pop();
        }
        other => panic!("unknown tamper {other}"),
    }
    format!(
        "{}.{}",
        token.signing_input,
        URL_SAFE_NO_PAD.encode(signature)
    )
}

/// A route behind `verifier` that answers the body it reads.
fn echo_app(verifier: JwtBodyHashVerifier) -> Router {
    let verifier = Arc::new(verifier);
    Router::new()
        .route("/webhook", post(|body: Bytes| async move { body }))
        .route_layer(axum::middleware::from_fn(
            move |request: Request, next: Next| {
                let verifier = Arc::clone(&verifier);
                async move { verifier.verify(request, next).await }
            },
        ))
}

async fn send(app: &Router, header: &str, token: Option<String>, body: &str) -> (u16, String) {
    let mut request = http::Request::post("/webhook")
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap();
    if let Some(token) = token {
        request.headers_mut().insert(
            header.parse::<http::HeaderName>().unwrap(),
            token.parse().unwrap(),
        );
    }
    let response = app.clone().oneshot(request).await.unwrap();
    let status = response.status().as_u16();
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

/// A lookup that answers `kids` with `key`'s JWK, fails when `fails`, and
/// counts the kids it was asked about.
fn lookup(
    key: Arc<TestKey>,
    kids: Vec<String>,
    fails: bool,
    asked: Arc<Mutex<Vec<String>>>,
) -> WebhookKeys {
    WebhookKeys::Lookup(Arc::new(move |kid: String| {
        asked.lock().unwrap().push(kid.clone());
        let answer = if fails {
            Err("the key endpoint is down".to_string())
        } else if kids.contains(&kid) {
            Ok(Some(key.jwk(&kid)))
        } else {
            Ok(None)
        };
        async move { answer }
    }))
}

#[tokio::test]
async fn parity_vectors() {
    let file = corpus();
    let names: BTreeSet<_> = file.vectors.iter().map(|v| &v.name).collect();
    assert!(!file.vectors.is_empty() && names.len() == file.vectors.len());
    let trusted = Arc::new(TestKey::new());
    let stranger = TestKey::new();
    let config = &file.verifier;
    for vector in &file.vectors {
        let now = vector.now;
        let verifier = JwtBodyHashVerifier::new(JwtBodyHashOptions {
            header: config.header.clone(),
            keys: lookup(
                Arc::clone(&trusted),
                vector.keys.clone(),
                vector.lookup_fails,
                Arc::default(),
            ),
            alg: config.alg,
            max_age: Duration::from_secs(config.max_age_seconds),
            body_hash_claim: config.body_hash_claim.clone(),
            clock_skew: Some(Duration::from_secs(config.clock_skew_seconds)),
            max_body_bytes: Some(config.max_body_bytes),
        })
        .unwrap()
        .with_clock(move || UNIX_EPOCH + Duration::from_secs(now));
        let app = echo_app(verifier);
        let token = vector
            .token
            .as_ref()
            .map(|token| token_of(token, &trusted, &stranger));
        let (status, body) = send(&app, &config.header, token, &vector.body).await;
        assert_eq!(status, vector.want.status, "{}: {body}", vector.name);
        match &vector.want.code {
            None => assert_eq!(body, vector.body, "{}", vector.name),
            Some(code) => {
                let problem: Value = serde_json::from_str(&body).unwrap();
                assert_eq!(problem["code"], json!(code), "{}", vector.name);
            }
        }
    }
}

const NOW: u64 = 1_767_225_600;
const PLAID_BODY: &str = "{\n  \"webhook_type\": \"TRANSACTIONS\",\n  \"webhook_code\": \"SYNC_UPDATES_AVAILABLE\",\n  \"item_id\": \"item-sandbox-1\"\n}";

fn hex_sha256(body: &str) -> String {
    ring::digest::digest(&ring::digest::SHA256, body.as_bytes())
        .as_ref()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// A Plaid-style token for `PLAID_BODY` under `kid`, signed by `key` at `now`.
fn plaid_token(key: &TestKey, kid: &str, now: u64) -> String {
    let segment = |value: Value| URL_SAFE_NO_PAD.encode(value.to_string());
    let input = format!(
        "{}.{}",
        segment(json!({"alg": "ES256", "kid": kid, "typ": "JWT"})),
        segment(json!({"iat": now, "request_body_sha256": hex_sha256(PLAID_BODY)}))
    );
    format!("{input}.{}", URL_SAFE_NO_PAD.encode(key.sign(&input)))
}

fn plaid_options(keys: WebhookKeys) -> JwtBodyHashOptions {
    JwtBodyHashOptions {
        header: "Plaid-Verification".to_string(),
        keys,
        alg: JwsAlgorithm::Es256,
        max_age: Duration::from_secs(300),
        body_hash_claim: "request_body_sha256".to_string(),
        clock_skew: None,
        max_body_bytes: None,
    }
}

/// A verifier over `keys` whose clock reads `clock`'s seconds.
fn plaid_app(keys: WebhookKeys, clock: &Arc<AtomicU64>) -> Router {
    let clock = Arc::clone(clock);
    echo_app(
        JwtBodyHashVerifier::new(plaid_options(keys))
            .unwrap()
            .with_clock(move || UNIX_EPOCH + Duration::from_secs(clock.load(Ordering::SeqCst))),
    )
}

async fn status(app: &Router, token: String) -> u16 {
    send(app, "plaid-verification", Some(token), PLAID_BODY)
        .await
        .0
}

/// A change to a verifier's options.
type Change = Box<dyn Fn(&mut JwtBodyHashOptions)>;

#[test]
fn a_config_it_cannot_run_is_refused() {
    let keys = || WebhookKeys::Lookup(Arc::new(|_kid: String| async { Ok(None) }));
    let cases: Vec<(Change, &str)> = vec![
        (Box::new(|o| o.header = String::new()), "header is required"),
        (
            Box::new(|o| o.alg = JwsAlgorithm::Rs256),
            "alg RS256 is not supported; use ES256",
        ),
        (
            Box::new(|o| o.max_age = Duration::ZERO),
            "max_age must be positive",
        ),
        (
            Box::new(|o| o.body_hash_claim = String::new()),
            "body_hash_claim is required",
        ),
        (
            Box::new(|o| o.max_body_bytes = Some(0)),
            "max_body_bytes must be positive",
        ),
    ];
    for (change, want) in cases {
        let mut options = plaid_options(keys());
        change(&mut options);
        let err = JwtBodyHashVerifier::new(options).err().unwrap();
        assert!(err.contains(want), "{err} lacks {want}");
    }
}

#[tokio::test]
async fn a_held_key_serves_and_lookups_are_bounded() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    let asked = Arc::new(Mutex::new(Vec::new()));
    let app = plaid_app(
        lookup(
            Arc::clone(&key),
            vec!["k1".into()],
            false,
            Arc::clone(&asked),
        ),
        &clock,
    );
    for _ in 0..3 {
        assert_eq!(status(&app, plaid_token(&key, "k1", NOW)).await, 200);
    }
    clock.fetch_add(60, Ordering::SeqCst);
    for kid in ["r1", "r2", "r3", "r4"] {
        assert_eq!(status(&app, plaid_token(&key, kid, NOW + 60)).await, 401);
    }
    // The held kid verifies through the flood.
    assert_eq!(status(&app, plaid_token(&key, "k1", NOW + 60)).await, 200);
    assert_eq!(*asked.lock().unwrap(), ["k1", "r1"]);
    clock.fetch_add(60, Ordering::SeqCst);
    status(&app, plaid_token(&key, "r5", NOW + 120)).await;
    assert_eq!(*asked.lock().unwrap(), ["k1", "r1", "r5"]);
}

#[tokio::test]
async fn a_held_key_an_hour_old_is_looked_up_again() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    // 0: the key, 1: the endpoint is down, 2: no such key.
    let answer = Arc::new(AtomicUsize::new(0));
    let lookups = Arc::new(AtomicUsize::new(0));
    let keys = {
        let (key, answer, lookups) = (Arc::clone(&key), Arc::clone(&answer), Arc::clone(&lookups));
        WebhookKeys::Lookup(Arc::new(move |kid: String| {
            lookups.fetch_add(1, Ordering::SeqCst);
            let result = match answer.load(Ordering::SeqCst) {
                0 => Ok(Some(key.jwk(&kid))),
                1 => Err("down".to_string()),
                _ => Ok(None),
            };
            async move { result }
        }))
    };
    let app = plaid_app(keys, &clock);
    assert_eq!(status(&app, plaid_token(&key, "k1", NOW)).await, 200);
    clock.fetch_add(3600, Ordering::SeqCst);
    answer.store(1, Ordering::SeqCst);
    let now = NOW + 3600;
    assert_eq!(status(&app, plaid_token(&key, "k1", now)).await, 200);
    assert_eq!(lookups.load(Ordering::SeqCst), 2);
    clock.fetch_add(60, Ordering::SeqCst);
    answer.store(2, Ordering::SeqCst);
    assert_eq!(status(&app, plaid_token(&key, "k1", now + 60)).await, 401);
    assert_eq!(lookups.load(Ordering::SeqCst), 3);
}

#[tokio::test]
async fn requests_for_a_kid_not_held_share_the_running_lookup() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    let gate = Arc::new(tokio::sync::Semaphore::new(0));
    let lookups = Arc::new(AtomicUsize::new(0));
    let keys = {
        let (key, gate, lookups) = (Arc::clone(&key), Arc::clone(&gate), Arc::clone(&lookups));
        WebhookKeys::Lookup(Arc::new(move |kid: String| {
            lookups.fetch_add(1, Ordering::SeqCst);
            let (key, gate) = (Arc::clone(&key), Arc::clone(&gate));
            async move {
                let _permit = gate.acquire().await.unwrap();
                Ok(Some(key.jwk(&kid)))
            }
        }))
    };
    let app = plaid_app(keys, &clock);
    let token = plaid_token(&key, "k1", NOW);
    let release = async {
        tokio::time::sleep(Duration::from_millis(10)).await;
        gate.add_permits(1);
    };
    let (a, b, c, d, ()) = tokio::join!(
        status(&app, token.clone()),
        status(&app, token.clone()),
        status(&app, token.clone()),
        status(&app, token.clone()),
        release
    );
    assert_eq!([a, b, c, d], [200, 200, 200, 200]);
    assert_eq!(lookups.load(Ordering::SeqCst), 1);
}

struct Jwks {
    set: Value,
    fetches: AtomicUsize,
}

#[async_trait]
impl KeyFetcher for Jwks {
    async fn fetch(&self, _url: &str, _bearer: Option<&str>) -> Result<Vec<u8>, String> {
        self.fetches.fetch_add(1, Ordering::SeqCst);
        Ok(self.set.to_string().into_bytes())
    }
}

#[tokio::test]
async fn a_jwks_url_is_fetched_whole_and_again_at_most_once_a_minute() {
    let (k1, k2) = (TestKey::new(), TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    let jwks = Arc::new(Jwks {
        set: json!({"keys": [k1.jwk("k1"), k2.jwk("k2"), {"kty": "RSA", "kid": "rsa", "n": "AQAB", "e": "AQAB"}]}),
        fetches: AtomicUsize::new(0),
    });
    let app = plaid_app(
        WebhookKeys::Jwks {
            url: "https://keys.example/jwks.json".to_string(),
            fetcher: Arc::clone(&jwks) as Arc<dyn KeyFetcher>,
        },
        &clock,
    );
    assert_eq!(status(&app, plaid_token(&k1, "k1", NOW)).await, 200);
    assert_eq!(status(&app, plaid_token(&k2, "k2", NOW)).await, 200);
    assert_eq!(status(&app, plaid_token(&k1, "k3", NOW)).await, 401);
    assert_eq!(status(&app, plaid_token(&k1, "rsa", NOW)).await, 401);
    assert_eq!(jwks.fetches.load(Ordering::SeqCst), 1);
    clock.fetch_add(60, Ordering::SeqCst);
    status(&app, plaid_token(&k1, "k3", NOW + 60)).await;
    assert_eq!(jwks.fetches.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn a_key_it_cannot_use_is_no_key() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    for (member, value) in [
        ("crv", "P-384"),
        ("d", "AAAA"),
        ("use", "enc"),
        ("alg", "RS256"),
        ("x", "AAAA"),
    ] {
        let key2 = Arc::clone(&key);
        let keys = WebhookKeys::Lookup(Arc::new(move |kid: String| {
            let mut jwk = key2.jwk(&kid);
            jwk[member] = json!(value);
            async move { Ok(Some(jwk)) }
        }));
        let app = plaid_app(keys, &clock);
        let (code, body) = send(
            &app,
            "plaid-verification",
            Some(plaid_token(&key, "k1", NOW)),
            PLAID_BODY,
        )
        .await;
        assert_eq!(code, 401, "{member}: {body}");
        assert_eq!(
            serde_json::from_str::<Value>(&body).unwrap()["code"],
            json!(INVALID_SIGNATURE_CODE)
        );
    }
}

#[tokio::test]
async fn two_headers_are_refused() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    let app = plaid_app(
        lookup(Arc::clone(&key), vec!["k1".into()], false, Arc::default()),
        &clock,
    );
    let token = plaid_token(&key, "k1", NOW);
    let request = http::Request::post("/webhook")
        .header("plaid-verification", &token)
        .header("plaid-verification", &token)
        .body(Body::from(PLAID_BODY))
        .unwrap();
    assert_eq!(app.oneshot(request).await.unwrap().status().as_u16(), 401);
}

/// Tokens for `PLAID_BODY` under `kid` whose header names an alg other
/// than ES256, or none: each signed by `key` with ES256 but `none`, which
/// carries no signature.
fn other_alg_tokens(key: &TestKey, kid: &str, now: u64) -> Vec<(&'static str, String)> {
    let segment = |value: Value| URL_SAFE_NO_PAD.encode(value.to_string());
    let claims = segment(json!({"iat": now, "request_body_sha256": hex_sha256(PLAID_BODY)}));
    ["none", "HS256", "RS256", "es256", ""]
        .into_iter()
        .map(|alg| {
            let header = if alg.is_empty() {
                json!({"kid": kid, "typ": "JWT"})
            } else {
                json!({"alg": alg, "kid": kid, "typ": "JWT"})
            };
            let input = format!("{}.{claims}", segment(header));
            let signature = if alg == "none" {
                String::new()
            } else {
                URL_SAFE_NO_PAD.encode(key.sign(&input))
            };
            (alg, format!("{input}.{signature}"))
        })
        .collect()
}

/// A token whose header names another alg than the configured one, none
/// among them, is refused before any key is looked up or fetched (D81).
#[tokio::test]
async fn a_token_naming_another_alg_is_refused_before_any_lookup() {
    let key = Arc::new(TestKey::new());
    let clock = Arc::new(AtomicU64::new(NOW));
    let asked = Arc::new(Mutex::new(Vec::new()));
    let by_kid = plaid_app(
        lookup(
            Arc::clone(&key),
            vec!["k1".into()],
            false,
            Arc::clone(&asked),
        ),
        &clock,
    );
    let jwks = Arc::new(Jwks {
        set: json!({"keys": [key.jwk("k1")]}),
        fetches: AtomicUsize::new(0),
    });
    let by_set = plaid_app(
        WebhookKeys::Jwks {
            url: "https://keys.example/jwks.json".to_string(),
            fetcher: Arc::clone(&jwks) as Arc<dyn KeyFetcher>,
        },
        &clock,
    );
    for (alg, token) in other_alg_tokens(&key, "k1", NOW) {
        for app in [&by_kid, &by_set] {
            assert_eq!(status(app, token.clone()).await, 401, "alg {alg:?}");
        }
    }
    assert_eq!(*asked.lock().unwrap(), Vec::<String>::new());
    assert_eq!(jwks.fetches.load(Ordering::SeqCst), 0);
}
