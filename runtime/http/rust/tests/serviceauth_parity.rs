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
    bearer_token, ApiError, Authenticator, JwtServiceAuthenticator, KeyFetcher, Principal,
    RouteControls, ServiceAuthConfig, ServiceAuthenticator, ServiceCaller,
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
