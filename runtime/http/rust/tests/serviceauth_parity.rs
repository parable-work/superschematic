//! The shared service auth vectors (D37, section 9.8 of
//! `docs/stack-model.md`): `runtime/http/testdata/serviceauth_parity.json`,
//! which the Go runtime writes and the Go, TypeScript and Rust runtimes each
//! run through their route gate. This runs each through `RouteControls` with
//! a `JwtServiceAuthenticator` and an end-user stub, as the file's comment
//! describes, and compares the status, the code, the caller and end user the
//! handler saw, and whether the end-user stub ran.

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::Extension;
use axum::routing::post;
use axum::{Json, Router};
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use http::request::Parts;
use http::{HeaderName, HeaderValue, StatusCode};
use serde::Deserialize;
use serde_json::{json, Value};
use std::collections::{BTreeMap, HashMap};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use superschematic_http_runtime::{
    bearer_token, ed25519_public_jwk_from_pem, is_loopback_url, ApiError, Authenticator,
    JwtServiceAuthenticator, KeyFetcher, Loopback, Principal, RouteControls, ServiceAuthConfig,
    ServiceAuthenticator, ServiceCaller, SignedKeySource,
};
use tower::ServiceExt;

const VECTORS: &str = "../testdata/serviceauth_parity.json";

#[derive(Deserialize)]
struct ParityFile {
    users: HashMap<String, User>,
    jwks: HashMap<String, Value>,
    configs: HashMap<String, ServiceAuthConfig>,
    vectors: Vec<Vector>,
}

#[derive(Clone, Deserialize)]
struct User {
    subject: String,
    permissions: Vec<String>,
}

#[derive(Deserialize)]
struct Vector {
    name: String,
    now: u64,
    config: Option<String>,
    route: Route,
    headers: BTreeMap<String, String>,
    want: Want,
}

#[derive(Deserialize)]
struct Route {
    service: Option<ServiceRule>,
    user: UserClause,
}

#[derive(Deserialize)]
struct ServiceRule {
    mode: String,
    from: Vec<String>,
}

#[derive(Deserialize)]
struct UserClause {
    required: bool,
    permissions: Vec<String>,
}

#[derive(Debug, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Want {
    status: u16,
    code: Option<String>,
    caller: Option<ServiceCaller>,
    user: Option<String>,
    user_authenticated: bool,
}

/// Answers `jwks[url]`, and fails for any other URL.
struct Keys(HashMap<String, Value>);

#[async_trait]
impl KeyFetcher for Keys {
    async fn fetch(&self, url: &str, _bearer: Option<&str>) -> Result<Vec<u8>, String> {
        let set = self
            .0
            .get(url)
            .ok_or_else(|| format!("{url} is unreachable"))?;
        serde_json::to_vec(set).map_err(|err| err.to_string())
    }
}

/// The end-user stub: the `Authorization` bearer token looked up in
/// `users`, recording that it ran.
struct Users {
    users: HashMap<String, User>,
    ran: Arc<AtomicBool>,
}

#[async_trait]
impl Authenticator for Users {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.ran.store(true, Ordering::SeqCst);
        Ok(bearer_token(&request.headers)
            .and_then(|token| self.users.get(token))
            .map(|user| Principal::new(user.subject.clone(), user.permissions.clone())))
    }
}

/// A server without a service authenticator does not compile in Rust when
/// an operation has a service rule (`Implementations.service_authenticator`
/// is then a field). The vectors expect such a route to answer 401
/// `service_unauthorized`, as the Go and TypeScript routers do; this stands
/// in for that.
struct NoServiceAuthenticator;

#[async_trait]
impl ServiceAuthenticator for NoServiceAuthenticator {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        Err(ApiError::service_unauthorized(
            "Service credential required",
        ))
    }
}

fn controls(file: &ParityFile, vector: &Vector, ran: &Arc<AtomicBool>) -> RouteControls {
    let service: Option<Arc<dyn ServiceAuthenticator>> = vector.config.as_ref().map(|name| {
        let config = file
            .configs
            .get(name)
            .cloned()
            .unwrap_or_else(|| panic!("{}: config {name:?} is not in configs", vector.name));
        let now = UNIX_EPOCH + Duration::from_secs(vector.now);
        let authenticator = JwtServiceAuthenticator::new(config)
            .unwrap_or_else(|err| panic!("{}: {err}", vector.name))
            .with_clock(move || -> SystemTime { now })
            .with_key_fetcher(Arc::new(Keys(file.jwks.clone())));
        Arc::new(authenticator) as Arc<dyn ServiceAuthenticator>
    });
    let mut controls = RouteControls::new();
    controls = match (&vector.route.service, service) {
        (None, Some(service)) => controls.identify_service(service),
        (None, None) => controls,
        (Some(rule), service) => {
            let service = service.unwrap_or_else(|| Arc::new(NoServiceAuthenticator));
            let from: Vec<&str> = rule.from.iter().map(String::as_str).collect();
            match rule.mode.as_str() {
                "require" => controls.require_service(service, &from),
                "allow" => controls.allow_service(service, &from),
                mode => panic!("{}: service mode {mode:?}", vector.name),
            }
        }
    };
    if vector.route.user.required {
        let users = Arc::new(Users {
            users: file.users.clone(),
            ran: Arc::clone(ran),
        });
        let permissions: Vec<&str> = vector
            .route
            .user
            .permissions
            .iter()
            .map(String::as_str)
            .collect();
        controls = controls.authorize(users, &permissions);
    }
    controls
}

async fn run(file: &ParityFile, vector: &Vector) -> Want {
    let ran = Arc::new(AtomicBool::new(false));
    let handler = post(
        |caller: Option<Extension<ServiceCaller>>, user: Option<Extension<Principal>>| async move {
            Json(json!({
                "caller": caller.map(|Extension(caller)| caller),
                "user": user.map(|Extension(user)| user.subject),
            }))
        },
    );
    let app = Router::new().route(
        "/api/stock/reservations",
        controls(file, vector, &ran).apply(handler),
    );

    let mut request = http::Request::post("/api/stock/reservations")
        .body(Body::empty())
        .unwrap();
    for (name, value) in &vector.headers {
        request.headers_mut().insert(
            HeaderName::from_bytes(name.as_bytes()).unwrap(),
            HeaderValue::from_str(value).unwrap(),
        );
    }
    let response = app.oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let body: Value = serde_json::from_slice(&bytes).unwrap_or_else(|err| {
        panic!(
            "{}: body {:?}: {err}",
            vector.name,
            String::from_utf8_lossy(&bytes)
        )
    });
    let user_authenticated = ran.load(Ordering::SeqCst);
    if status == StatusCode::OK {
        Want {
            status: status.as_u16(),
            code: None,
            caller: serde_json::from_value(body["caller"].clone()).unwrap(),
            user: body["user"].as_str().map(ToString::to_string),
            user_authenticated,
        }
    } else {
        Want {
            status: status.as_u16(),
            code: body["code"].as_str().map(ToString::to_string),
            caller: None,
            user: None,
            user_authenticated,
        }
    }
}

#[tokio::test]
async fn the_shared_service_auth_vectors() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let Ok(data) = std::fs::read(&path) else {
        eprintln!(
            "skipped: {} is absent; the Go runtime writes it \
             (cd runtime/http/go && go test ./serviceauth -run TestWriteParityVectors -update)",
            path.display()
        );
        return;
    };
    let file: ParityFile = serde_json::from_slice(&data).expect("serviceauth_parity.json");
    assert!(!file.vectors.is_empty(), "no vectors");
    let mut failures = Vec::new();
    for vector in &file.vectors {
        let got = run(&file, vector).await;
        if got != vector.want {
            failures.push(format!(
                "{}:\n  got  {got:?}\n  want {:?}",
                vector.name, vector.want
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} vectors differ:\n{}",
        failures.len(),
        file.vectors.len(),
        failures.join("\n")
    );
}

/// The signed-token source's vectors (D63): the file's comment states the
/// harness.
#[derive(Deserialize)]
struct SigningFile {
    signing: Vec<Signing>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Signing {
    name: String,
    key: String,
    public_key: String,
    issuer: String,
    audience: String,
    now: u64,
    want: Option<Signed>,
}

#[derive(Deserialize)]
struct Signed {
    header: String,
    kid: String,
    claims: serde_json::Map<String, Value>,
}

/// The token the signed-token source signs for a vector, or why it refuses
/// the key.
async fn sign(vector: &Signing) -> Result<String, String> {
    let now = UNIX_EPOCH + Duration::from_secs(vector.now);
    let source = SignedKeySource::from_key(
        &vector.key,
        &vector.issuer,
        &vector.issuer,
        &vector.audience,
    )?
    .with_clock(move || -> SystemTime { now })
    .build();
    source(false).await
}

/// Checks one signing vector, or says how it differs.
async fn check_signing(vector: &Signing) -> Result<(), String> {
    let signed = sign(vector).await;
    let Some(want) = &vector.want else {
        return match signed {
            Ok(token) => Err(format!("signed {token} with a key it should refuse")),
            Err(_) => Ok(()),
        };
    };
    let token = signed?;
    let segments: Vec<&str> = token.split('.').collect();
    if segments.first() != Some(&want.header.as_str()) {
        return Err(format!(
            "header {:?}, want {}",
            segments.first(),
            want.header
        ));
    }
    let payload = URL_SAFE_NO_PAD
        .decode(segments.get(1).copied().unwrap_or_default())
        .map_err(|err| err.to_string())?;
    let mut claims: serde_json::Map<String, Value> =
        serde_json::from_slice(&payload).map_err(|err| err.to_string())?;
    let jti = claims
        .remove("jti")
        .and_then(|jti| jti.as_str().map(str::to_string))
        .unwrap_or_default();
    if URL_SAFE_NO_PAD.decode(&jti).map(|raw| raw.len()) != Ok(16) {
        return Err(format!("jti {jti:?} is not 16 bytes in base64url"));
    }
    if claims != want.claims {
        return Err(format!("claims {claims:?}, want {:?}", want.claims));
    }
    let key = ed25519_public_jwk_from_pem(&vector.public_key)?;
    if key.get("kid").and_then(Value::as_str) != Some(want.kid.as_str()) {
        return Err(format!(
            "the public key's kid {:?}, want {}",
            key.get("kid"),
            want.kid
        ));
    }
    let mut callers = serde_json::Map::new();
    callers.insert(
        vector.issuer.clone(),
        json!({ "deployable": vector.issuer, "serves": ["shop-orders"] }),
    );
    let config: ServiceAuthConfig = serde_json::from_value(json!({
        "issuers": [{
            "issuer": vector.issuer,
            "audience": vector.audience,
            "algorithms": ["EdDSA"],
            "keys": [key],
            "maxLifetimeSeconds": 300,
            "callers": callers,
        }],
    }))
    .map_err(|err| err.to_string())?;
    let now = UNIX_EPOCH + Duration::from_secs(vector.now);
    let authenticator =
        JwtServiceAuthenticator::new(config)?.with_clock(move || -> SystemTime { now });
    let (parts, ()) = http::Request::builder()
        .header("service-authorization", format!("Bearer {token}"))
        .body(())
        .map_err(|err| err.to_string())?
        .into_parts();
    match authenticator.authenticate(&parts).await {
        Ok(Some(caller)) if caller.deployable == vector.issuer => Ok(()),
        other => Err(format!(
            "authenticate = {:?}, want {}",
            other.map(|caller| caller.map(|c| c.deployable)),
            vector.issuer
        )),
    }
}

#[tokio::test]
async fn the_shared_signed_token_vectors() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let Ok(data) = std::fs::read(&path) else {
        eprintln!(
            "skipped: {} is absent; the Go runtime writes it",
            path.display()
        );
        return;
    };
    let file: SigningFile = serde_json::from_slice(&data).expect("serviceauth_parity.json");
    assert!(!file.signing.is_empty(), "no signing vectors");
    let mut failures = Vec::new();
    for vector in &file.signing {
        if let Err(why) = check_signing(vector).await {
            failures.push(format!("{}: {why}", vector.name));
        }
    }
    assert!(
        failures.is_empty(),
        "{} signing vectors differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

/// The loopback key's section (section 9.9 of `docs/stack-model.md`): the
/// file's comment states the harness.
#[derive(Deserialize)]
struct LoopbackFile {
    configs: HashMap<String, ServiceAuthConfig>,
    loopback: LoopbackVectors,
}

#[derive(Deserialize)]
struct LoopbackVectors {
    seed: String,
    deployable: String,
    serves: Vec<String>,
    now: u64,
    issuer: Value,
    header: String,
    claims: serde_json::Map<String, Value>,
    config: String,
    checks: Vec<LoopbackCheck>,
    refused: Vec<LoopbackRefused>,
    urls: BTreeMap<String, bool>,
}

#[derive(Deserialize)]
struct LoopbackCheck {
    name: String,
    token: String,
    at: u64,
    status: u16,
    code: Option<String>,
    caller: Option<ServiceCaller>,
}

#[derive(Deserialize)]
struct LoopbackRefused {
    name: String,
    config: ServiceAuthConfig,
}

#[tokio::test]
async fn the_shared_loopback_vectors() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let Ok(data) = std::fs::read(&path) else {
        eprintln!(
            "skipped: {} is absent; the Go runtime writes it",
            path.display()
        );
        return;
    };
    let file: LoopbackFile = serde_json::from_slice(&data).expect("serviceauth_parity.json");
    let v = &file.loopback;
    let seed: [u8; 32] = URL_SAFE_NO_PAD
        .decode(&v.seed)
        .expect("seed")
        .try_into()
        .expect("a 32-byte seed");
    let now = UNIX_EPOCH + Duration::from_secs(v.now);
    let loopback = Loopback::with_seed(&seed, &v.deployable, &v.serves, move || -> SystemTime {
        now
    })
    .expect("loopback key");
    assert_eq!(
        serde_json::to_value(loopback.issuer()).unwrap(),
        v.issuer,
        "issuer"
    );
    let token = loopback.token_source()(false).await.expect("token");
    let segments: Vec<&str> = token.split('.').collect();
    assert_eq!(segments[0], v.header, "header");
    let mut claims: serde_json::Map<String, Value> =
        serde_json::from_slice(&URL_SAFE_NO_PAD.decode(segments[1]).unwrap()).unwrap();
    let jti = claims.remove("jti").expect("jti");
    assert_eq!(
        URL_SAFE_NO_PAD
            .decode(jti.as_str().unwrap_or_default())
            .map(|raw| raw.len()),
        Ok(16),
        "jti {jti}"
    );
    assert_eq!(claims, v.claims, "claims");

    let config = loopback
        .accept(file.configs[&v.config].clone())
        .expect("accept");
    let mut failures = Vec::new();
    for check in &v.checks {
        let at = UNIX_EPOCH + Duration::from_secs(v.now + check.at);
        let authenticator = JwtServiceAuthenticator::new(config.clone())
            .unwrap()
            .with_clock(move || -> SystemTime { at });
        let sent = if check.token.is_empty() {
            &token
        } else {
            &check.token
        };
        let (parts, ()) = http::Request::builder()
            .header("service-authorization", format!("Bearer {sent}"))
            .body(())
            .unwrap()
            .into_parts();
        let got = match authenticator.authenticate(&parts).await {
            Ok(caller) => (200, None, caller),
            Err(err) => (err.status.as_u16(), Some(err.code), None),
        };
        if got != (check.status, check.code.clone(), check.caller.clone()) {
            failures.push(format!(
                "{}: got {got:?}, want {} {:?} {:?}",
                check.name, check.status, check.code, check.caller
            ));
        }
    }
    for refused in &v.refused {
        if loopback.accept(refused.config.clone()).is_ok() {
            failures.push(format!("accept took {}", refused.name));
        }
    }
    for (url, want) in &v.urls {
        if is_loopback_url(url) != *want {
            failures.push(format!("is_loopback_url({url:?}) = {}, want {want}", !want));
        }
    }
    assert!(
        failures.is_empty(),
        "{} loopback vectors differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
