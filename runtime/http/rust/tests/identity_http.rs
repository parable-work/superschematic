//! The identity routes end to end, through `tower::ServiceExt::oneshot`, as
//! a generated server mounts them: the session and administration handlers,
//! an app route the router guards with `RouteControls::authorize` and the
//! `IdentityAuthenticator`, the request-id middleware, and the CORS layer
//! around it all. Each test runs on SQLite, and on Postgres with the
//! `identity-postgres` feature and `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL`.
//! The cases are the Go runtime's handler tests, its API key tests (D114)
//! after the others.

mod support;

use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use axum::body::Body;
use axum::extract::Extension;
use axum::routing::{get, post};
use axum::Router;
use http::{HeaderMap, Method, Request, StatusCode};
use serde_json::{json, Value};
use superschematic_http_runtime::identity::{
    hash_password, hash_token, new_api_key, Argon2Params, Config, CreateApiKeyInput,
    IdentityAuthenticator, IdentityPrincipal, NewApiKey, NewUser, NewUserGrants, Route, Service,
    SqlStore, Store, Transport, UserAdministration, UserSessions, API_KEY_LENGTH, API_KEY_PREFIX,
    CODE_CONFLICT, CODE_CROSS_ORIGIN, CODE_FORBIDDEN, CODE_INVALID_CREDENTIALS,
    CODE_INVALID_PERMISSION, CODE_NOT_FOUND, CODE_UNAUTHORIZED, HOST_COOKIE_NAME, OPERATIONS,
    OP_CREATE_API_KEY, OP_LIST_API_KEYS, OP_LIST_USER_API_KEYS, OP_LOGIN, OP_REVOKE_API_KEY,
    OP_REVOKE_USER_API_KEY, TOKEN_LENGTH,
};
use superschematic_http_runtime::{
    request_ids, Authenticator, OperationInfo, Principal, RouteControls,
};
use support::{at, databases, sqlite, TestDb};
use tower::ServiceExt;

/// A config at a low argon2 cost, so a test hashes quickly.
const TEST_CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://app.example.com"]}"#;

const ADMIN_PASSWORD: &str = "admin password";
const USER_PASSWORD: &str = "user password";
const MISSING: &str = "00000000-0000-4000-8000-000000000000";

/// The route requirements capabilities answers for.
fn route_table() -> Vec<Route> {
    let route = |id: &str, auth: bool, permissions: &[&str], service_only: bool| Route {
        operation_id: id.to_owned(),
        requires_auth: auth,
        permissions: permissions.iter().map(ToString::to_string).collect(),
        require_ownership: false,
        service_only,
    };
    vec![
        route("OrdersList", false, &[], false),
        route("AuthMe", true, &[], false),
        route("OrdersCreate", true, &["orders.write"], false),
        route("IdentityListUsers", true, &["identity.users.read"], false),
        route("StockSync", false, &[], true),
    ]
}

/// A clock a test moves by hand.
#[derive(Clone)]
struct TestClock(Arc<Mutex<SystemTime>>);

impl TestClock {
    fn now(&self) -> SystemTime {
        *self.0.lock().unwrap()
    }

    fn advance(&self, d: Duration) {
        *self.0.lock().unwrap() += d;
    }
}

/// What a route answered.
struct Reply {
    status: StatusCode,
    headers: HeaderMap,
    body: Value,
    cookies: Vec<String>,
}

impl Reply {
    fn data(&self) -> &Value {
        &self.body["data"]
    }

    fn code(&self) -> &str {
        self.body["code"].as_str().unwrap_or("")
    }
}

/// A service over a store on one database, mounted as a generated server
/// mounts it, with an app route the router guards: `POST /orders` for a
/// caller holding orders.write, and `GET /orders` for one holding
/// orders.read or orders.write.
struct Harness {
    db: TestDb,
    store: Arc<SqlStore>,
    service: Arc<Service>,
    clock: TestClock,
    router: Router,
    config: Config,
    /// admin holds the role "admin" (identity, orders); member holds none.
    admin_id: String,
    member_id: String,
}

async fn harness(db: TestDb, config: &str) -> Harness {
    harness_over(db, &support::descriptor(|_| {}), config, |service| service).await
}

/// Both sets' API key operations turned on (D114).
fn with_api_keys(service: Service) -> Service {
    service.with_api_keys().with_api_key_administration()
}

/// [`harness`] over a store built from `descriptor`, with the service's
/// options `options`.
async fn harness_over(
    db: TestDb,
    descriptor: &str,
    config: &str,
    options: fn(Service) -> Service,
) -> Harness {
    let store = Arc::new(db.store(descriptor));
    let config = Config::parse(config.as_bytes()).unwrap();
    let clock = TestClock(Arc::new(Mutex::new(at(Duration::ZERO))));
    let now = clock.clone();
    let service = Arc::new(options(
        Service::new(store.clone(), config.clone())
            .unwrap()
            .with_clock(Arc::new(move || now.now()))
            .with_routes(route_table()),
    ));

    let authenticator: Arc<dyn Authenticator> =
        Arc::new(IdentityAuthenticator::new(Arc::clone(&service)));
    let name = |Extension(principal): Extension<Principal>| async move {
        principal.claims["name"].as_str().unwrap_or("").to_owned()
    };
    let orders = RouteControls::new()
        .authorize(Arc::clone(&authenticator), &["orders.write"])
        .apply(post(name))
        .merge(
            RouteControls::new()
                .authorize(authenticator, &["orders.read", "orders.write"])
                .apply(get(name)),
        );
    let router = service
        .router(
            Some(&UserSessions {
                register: true,
                ..UserSessions::default()
            }),
            Some(&UserAdministration::default()),
        )
        .route("/orders", orders)
        .layer(axum::middleware::from_fn(request_ids))
        .layer(service.cors());

    let hash = |password: &str| hash_password(password, config.argon2_params()).unwrap();
    let create = |login: &str, name: &str, password: &str| NewUser {
        login: login.to_owned(),
        name: name.to_owned(),
        password_hash: hash(password),
        at: clock.now(),
    };
    let admin = store
        .create_user(create("admin@example.com", "Admin", ADMIN_PASSWORD))
        .await
        .unwrap();
    let member = store
        .create_user(create("member@example.com", "Member", USER_PASSWORD))
        .await
        .unwrap();
    let role = store
        .create_role("admin", &["identity".to_owned(), "orders".to_owned()])
        .await
        .unwrap();
    store
        .grant_role(&admin.id, &role.id, clock.now())
        .await
        .unwrap();
    Harness {
        db,
        store,
        service,
        clock,
        router,
        config,
        admin_id: admin.id,
        member_id: member.id,
    }
}

impl Harness {
    async fn send(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
        headers: &[(&str, &str)],
    ) -> Reply {
        let mut request = Request::builder()
            .method(Method::from_bytes(method.as_bytes()).unwrap())
            .uri(path)
            .header("host", "api.example.com");
        for (name, value) in headers {
            request = request.header(*name, *value);
        }
        let body = body.map_or_else(Body::empty, |b| Body::from(b.to_string()));
        let response = self
            .router
            .clone()
            .oneshot(request.body(body).unwrap())
            .await
            .unwrap();
        let (parts, body) = response.into_parts();
        let bytes = axum::body::to_bytes(body, usize::MAX).await.unwrap();
        let cookies = parts
            .headers
            .get_all("set-cookie")
            .iter()
            .map(|v| v.to_str().unwrap().to_owned())
            .collect();
        Reply {
            status: parts.status,
            body: serde_json::from_slice(&bytes)
                .unwrap_or_else(|_| Value::from(String::from_utf8_lossy(&bytes).into_owned())),
            headers: parts.headers,
            cookies,
        }
    }

    /// Signs in with a bearer session and answers its token.
    async fn login(&self, login: &str, password: &str) -> String {
        let r = self
            .send(
                "POST",
                "/auth/login",
                Some(json!({"login": login, "password": password})),
                &[],
            )
            .await;
        expect(&r, 200, "");
        r.data()["token"].as_str().unwrap().to_owned()
    }
}

#[track_caller]
fn expect(r: &Reply, status: u16, code: &str) {
    assert_eq!(r.status.as_u16(), status, "{}", r.body);
    if !code.is_empty() {
        assert_eq!(r.code(), code, "{}", r.body);
    }
}

fn bearer(token: &str) -> String {
    format!("Bearer {token}")
}

fn session_cookie(token: &str) -> String {
    format!("{HOST_COOKIE_NAME}={token}")
}

/// A `Set-Cookie`'s value.
fn cookie_value(set_cookie: &str) -> String {
    set_cookie
        .split("; ")
        .next()
        .and_then(|pair| pair.split_once('='))
        .map(|(_, v)| v.to_owned())
        .unwrap_or_default()
}

/// Runs `test` on a harness over each database.
async fn each_harness<F, Fut>(config: &str, test: F)
where
    F: Fn(Harness) -> Fut,
    Fut: Future<Output = Harness>,
{
    for db in databases().await {
        eprintln!("on {}", db.name);
        let h = test(harness(db, config).await).await;
        h.db.close().await;
    }
}

/// A bearer login answers the user, the expiry and the token; the token
/// signs `me`, `capabilities` and a route the router guards in; every
/// failed login is 401 `invalid_credentials`, and a refused input 400.
#[tokio::test]
async fn login_bearer() {
    each_harness(TEST_CONFIG, |h| async move {
        let r = h
            .send(
                "POST",
                "/auth/login",
                Some(json!({"login": "ADMIN@example.com", "password": ADMIN_PASSWORD})),
                &[],
            )
            .await;
        expect(&r, 200, "");
        assert_eq!(
            r.data()["user"],
            json!({"id": h.admin_id, "login": "admin@example.com", "name": "Admin"})
        );
        assert_eq!(r.data()["expiresAt"], "2026-10-22T12:00:00Z", "14 days on");
        assert!(r.cookies.is_empty(), "a bearer login set {:?}", r.cookies);
        assert!(r.body["meta"]["requestId"].is_string(), "{}", r.body);
        let token = bearer(r.data()["token"].as_str().unwrap());
        let t = [("authorization", token.as_str())];

        let me = h.send("GET", "/auth/me", None, &t).await;
        expect(&me, 200, "");
        assert_eq!(me.data()["permissions"], json!(["identity", "orders"]));
        assert_eq!(me.data()["roles"].as_array().unwrap().len(), 1);
        assert_eq!(me.data()["roles"][0]["name"], "admin");

        let caps = h.send("GET", "/auth/capabilities", None, &t).await;
        expect(&caps, 200, "");
        assert_eq!(
            caps.data()["operations"],
            json!({"OrdersList": true, "AuthMe": true, "OrdersCreate": true, "IdentityListUsers": true})
        );
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let m = [("authorization", member.as_str())];
        let caps = h.send("GET", "/auth/capabilities", None, &m).await;
        assert_eq!(
            caps.data()["operations"],
            json!({"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": false})
        );

        // The router's authenticator feeds its permission check unchanged.
        let order = h.send("POST", "/orders", None, &t).await;
        expect(&order, 200, "");
        assert_eq!(order.body, "Admin");
        expect(&h.send("POST", "/orders", None, &m).await, 403, CODE_FORBIDDEN);
        expect(
            &h.send("POST", "/orders", None, &[]).await,
            401,
            CODE_UNAUTHORIZED,
        );

        for (name, body) in [
            (
                "a wrong password",
                json!({"login": "admin@example.com", "password": "wrong password"}),
            ),
            (
                "an unknown login",
                json!({"login": "nobody@example.com", "password": ADMIN_PASSWORD}),
            ),
            (
                "a login the scalar refuses",
                json!({"login": "admin", "password": ADMIN_PASSWORD}),
            ),
            (
                "the right password for another",
                json!({"login": "member@example.com", "password": ADMIN_PASSWORD}),
            ),
            (
                "the password in another case",
                json!({"login": "admin@example.com", "password": ADMIN_PASSWORD.to_uppercase()}),
            ),
            (
                "a bearer session asked explicitly",
                json!({"login": "admin@example.com", "password": "wrong password", "session": "bearer"}),
            ),
        ] {
            let r = h.send("POST", "/auth/login", Some(body), &[]).await;
            assert_eq!(
                (r.status.as_u16(), r.code()),
                (401, CODE_INVALID_CREDENTIALS),
                "{name}"
            );
        }
        for (name, body) in [
            (
                "a short password",
                json!({"login": "admin@example.com", "password": "short"}),
            ),
            ("no login", json!({"password": ADMIN_PASSWORD})),
            (
                "an unknown session",
                json!({"login": "admin@example.com", "password": ADMIN_PASSWORD, "session": "jwt"}),
            ),
            (
                "an unknown member",
                json!({"login": "admin@example.com", "password": ADMIN_PASSWORD, "remember": true}),
            ),
            (
                "a member named in another case",
                json!({"Login": "admin@example.com", "password": ADMIN_PASSWORD}),
            ),
            ("a body that is a list", json!([])),
        ] {
            let r = h.send("POST", "/auth/login", Some(body), &[]).await;
            assert_eq!(
                (r.status.as_u16(), r.code()),
                (400, "bad_request"),
                "{name}: {}",
                r.body
            );
        }
        let short = h
            .send(
                "POST",
                "/auth/login",
                Some(json!({"login": "admin@example.com", "password": "short"})),
                &[],
            )
            .await;
        assert_eq!(
            short.body["errors"]["password"][0]["validator"],
            "length",
            "{}",
            short.body
        );
        h
    })
    .await;
}

/// A cookie login sets the session cookie and answers no token; the cookie
/// authenticates, logout revokes the session and clears it, and a refused
/// cookie is cleared with the 401, by the handlers and by the router's
/// guard alike.
#[tokio::test]
async fn login_cookie() {
    each_harness(TEST_CONFIG, |h| async move {
        let r = h
            .send(
                "POST",
                "/auth/login",
                Some(
                    json!({"login": "admin@example.com", "password": ADMIN_PASSWORD, "session": "cookie"}),
                ),
                &[("sec-fetch-site", "same-origin")],
            )
            .await;
        expect(&r, 200, "");
        assert!(r.data().get("token").is_none(), "{}", r.body);
        assert_eq!(r.cookies.len(), 1, "{:?}", r.cookies);
        let set = &r.cookies[0];
        assert!(
            set.starts_with(&format!("{HOST_COOKIE_NAME}="))
                && set.ends_with("; Path=/; Max-Age=1209600; HttpOnly; Secure; SameSite=Lax"),
            "{set}"
        );
        let token = cookie_value(set);
        assert_eq!(token.len(), TOKEN_LENGTH);
        let cookie = session_cookie(&token);
        let same = [
            ("cookie", cookie.as_str()),
            ("sec-fetch-site", "same-origin"),
        ];

        expect(
            &h.send("GET", "/auth/me", None, &[("cookie", &cookie)])
                .await,
            200,
            "",
        );
        // A safe method skips the cross-origin check.
        let cross_get = [
            ("cookie", cookie.as_str()),
            ("sec-fetch-site", "cross-site"),
        ];
        expect(&h.send("GET", "/auth/me", None, &cross_get).await, 200, "");
        expect(&h.send("POST", "/orders", None, &same).await, 200, "");

        let out = h.send("POST", "/auth/logout", None, &same).await;
        expect(&out, 200, "");
        assert_eq!(out.data(), &json!(true));
        assert_eq!(
            out.cookies,
            [h.config.clear_cookie()],
            "logout clears the cookie"
        );

        let clear = [h.config.clear_cookie()];
        let after = h
            .send("GET", "/auth/me", None, &[("cookie", &cookie)])
            .await;
        expect(&after, 401, CODE_UNAUTHORIZED);
        assert_eq!(after.cookies, clear, "a refused cookie is cleared");
        let bad = h
            .send(
                "GET",
                "/auth/me",
                None,
                &[("cookie", &session_cookie("not-a-token"))],
            )
            .await;
        expect(&bad, 401, CODE_UNAUTHORIZED);
        assert_eq!(bad.cookies, clear);
        let guarded = h.send("POST", "/orders", None, &same).await;
        expect(&guarded, 401, CODE_UNAUTHORIZED);
        assert_eq!(
            guarded.cookies, clear,
            "a route the router guards clears a refused cookie too"
        );
        assert!(guarded.body["requestId"].is_string(), "{}", guarded.body);
        let unguarded = h.send("POST", "/orders", None, &[]).await;
        assert!(unguarded.cookies.is_empty(), "{:?}", unguarded.cookies);
        h
    })
    .await;
}

/// A cookie login and a cookie request with an unsafe method from another
/// origin are 403 `cross_origin`, unless the origin is trusted; a bearer
/// request is not checked.
#[tokio::test]
async fn cross_origin() {
    each_harness(TEST_CONFIG, |h| async move {
        let cookie_login =
            json!({"login": "admin@example.com", "password": ADMIN_PASSWORD, "session": "cookie"});
        let evil = [
            ("sec-fetch-site", "cross-site"),
            ("origin", "https://evil.example"),
        ];
        expect(
            &h.send("POST", "/auth/login", Some(cookie_login.clone()), &evil)
                .await,
            403,
            CODE_CROSS_ORIGIN,
        );
        expect(
            &h.send(
                "POST",
                "/auth/login",
                Some(cookie_login.clone()),
                &[("origin", "https://evil.example")],
            )
            .await,
            403,
            CODE_CROSS_ORIGIN,
        );
        expect(
            &h.send(
                "POST",
                "/auth/register",
                Some(
                    json!({"login": "new@example.com", "password": USER_PASSWORD, "session": "cookie"}),
                ),
                &[("sec-fetch-site", "cross-site")],
            )
            .await,
            403,
            CODE_CROSS_ORIGIN,
        );
        // A bearer login from another origin is not checked: it sets no
        // cookie.
        expect(
            &h.send(
                "POST",
                "/auth/login",
                Some(json!({"login": "admin@example.com", "password": ADMIN_PASSWORD})),
                &[("sec-fetch-site", "cross-site")],
            )
            .await,
            200,
            "",
        );

        let app = [
            ("sec-fetch-site", "cross-site"),
            ("origin", "https://app.example.com"),
        ];
        let trusted = h
            .send("POST", "/auth/login", Some(cookie_login), &app)
            .await;
        expect(&trusted, 200, "");
        assert_eq!(
            trusted.headers["access-control-allow-origin"],
            "https://app.example.com"
        );
        assert_eq!(trusted.headers["access-control-allow-credentials"], "true");
        let cookie = session_cookie(&cookie_value(&trusted.cookies[0]));

        let cross = [("cookie", cookie.as_str()), evil[0], evil[1]];
        let refused = h.send("POST", "/auth/logout", None, &cross).await;
        expect(&refused, 403, CODE_CROSS_ORIGIN);
        assert!(
            refused.cookies.is_empty(),
            "a cross-origin refusal cleared the cookie"
        );
        expect(
            &h.send("POST", "/orders", None, &cross).await,
            403,
            CODE_CROSS_ORIGIN,
        );
        let from_app = [("cookie", cookie.as_str()), app[0], app[1]];
        expect(&h.send("POST", "/orders", None, &from_app).await, 200, "");

        let token = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let bearer_cross = [("authorization", token.as_str()), evil[0], evil[1]];
        expect(
            &h.send("POST", "/orders", None, &bearer_cross).await,
            200,
            "",
        );
        h
    })
    .await;
}

/// An `Authorization` header that is not a usable bearer token is 401, and
/// the cookie is not read.
#[tokio::test]
async fn credential_precedence() {
    each_harness(TEST_CONFIG, |h| async move {
        let r = h
            .send(
                "POST",
                "/auth/login",
                Some(
                    json!({"login": "admin@example.com", "password": ADMIN_PASSWORD, "session": "cookie"}),
                ),
                &[],
            )
            .await;
        let token = cookie_value(&r.cookies[0]);
        let cookie = session_cookie(&token);
        let basic = h
            .send(
                "GET",
                "/auth/me",
                None,
                &[
                    ("authorization", "Basic YWRtaW46cGFzcw=="),
                    ("cookie", &cookie),
                ],
            )
            .await;
        expect(&basic, 401, CODE_UNAUTHORIZED);
        assert!(basic.cookies.is_empty(), "the cookie was not the credential");
        let unknown = bearer(&"A".repeat(43));
        expect(
            &h.send(
                "GET",
                "/auth/me",
                None,
                &[("authorization", &unknown), ("cookie", &cookie)],
            )
            .await,
            401,
            CODE_UNAUTHORIZED,
        );
        expect(
            &h.send(
                "GET",
                "/auth/me",
                None,
                &[("authorization", &bearer(&token)), ("cookie", "x")],
            )
            .await,
            200,
            "",
        );
        h
    })
    .await;
}

/// A session ends when it expires, is revoked, goes idle, or its user is
/// disabled; requests within the idle timeout keep it alive.
#[tokio::test]
async fn session_ends() {
    let idle = r#"{"sessionTtlSeconds": 3600, "idleTimeoutSeconds": 600, "touchIntervalSeconds": 60, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
    each_harness(idle, |h| async move {
        let me = |token: String| {
            let h = &h;
            async move {
                h.send(
                    "GET",
                    "/auth/me",
                    None,
                    &[("authorization", &bearer(&token))],
                )
                .await
            }
        };

        let active = h.login("member@example.com", USER_PASSWORD).await;
        for _ in 0..10 {
            h.clock.advance(Duration::from_secs(500));
            if me(active.clone()).await.status == StatusCode::UNAUTHORIZED {
                // The session expires an hour after login, the eighth step.
                assert!(
                    h.clock.now() >= at(Duration::from_secs(3600)),
                    "an active session ended early"
                );
                break;
            }
        }
        expect(&me(active).await, 401, CODE_UNAUTHORIZED);

        let idle_token = h.login("member@example.com", USER_PASSWORD).await;
        h.clock.advance(Duration::from_secs(599));
        expect(&me(idle_token.clone()).await, 200, "");
        h.clock.advance(Duration::from_secs(599));
        expect(&me(idle_token.clone()).await, 200, "");
        h.clock.advance(Duration::from_secs(600));
        expect(&me(idle_token).await, 401, CODE_UNAUTHORIZED);

        let revoked = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let r = [("authorization", revoked.as_str())];
        expect(&h.send("POST", "/auth/logout", None, &r).await, 200, "");
        expect(
            &h.send("GET", "/auth/me", None, &r).await,
            401,
            CODE_UNAUTHORIZED,
        );
        expect(
            &h.send("POST", "/auth/logout", None, &r).await,
            401,
            CODE_UNAUTHORIZED,
        );

        let disabled = h.login("member@example.com", USER_PASSWORD).await;
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let a = [("authorization", admin.as_str())];
        let path = format!("/auth/admin/users/{}/disable", h.member_id);
        expect(&h.send("POST", &path, None, &a).await, 200, "");
        expect(&me(disabled.clone()).await, 401, CODE_UNAUTHORIZED);
        let login = json!({"login": "member@example.com", "password": USER_PASSWORD});
        expect(
            &h.send("POST", "/auth/login", Some(login), &[]).await,
            401,
            CODE_INVALID_CREDENTIALS,
        );
        let path = format!("/auth/admin/users/{}/enable", h.member_id);
        expect(&h.send("POST", &path, None, &a).await, 200, "");
        // Enabling does not bring a revoked session back.
        expect(&me(disabled).await, 401, CODE_UNAUTHORIZED);
        h.login("member@example.com", USER_PASSWORD).await;
        h
    })
    .await;
}

/// The current password must verify; the change keeps the caller's session
/// and revokes the others.
#[tokio::test]
async fn change_password() {
    each_harness(TEST_CONFIG, |h| async move {
        let mine = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let other = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let me = [("authorization", mine.as_str())];
        let new_password = "a new password";
        let change =
            |current: &str, password: &str| Some(json!({"current": current, "password": password}));

        expect(
            &h.send(
                "POST",
                "/auth/password",
                change("not my password", new_password),
                &me,
            )
            .await,
            401,
            CODE_INVALID_CREDENTIALS,
        );
        expect(
            &h.send(
                "POST",
                "/auth/password",
                change(USER_PASSWORD, "short"),
                &me,
            )
            .await,
            400,
            "bad_request",
        );
        expect(
            &h.send(
                "POST",
                "/auth/password",
                change(USER_PASSWORD, new_password),
                &[],
            )
            .await,
            401,
            CODE_UNAUTHORIZED,
        );
        let changed = h
            .send(
                "POST",
                "/auth/password",
                change(USER_PASSWORD, new_password),
                &me,
            )
            .await;
        expect(&changed, 200, "");
        assert_eq!(changed.data(), &json!(true));

        expect(&h.send("GET", "/auth/me", None, &me).await, 200, "");
        expect(
            &h.send("GET", "/auth/me", None, &[("authorization", &other)])
                .await,
            401,
            CODE_UNAUTHORIZED,
        );
        let old = json!({"login": "member@example.com", "password": USER_PASSWORD});
        expect(
            &h.send("POST", "/auth/login", Some(old), &[]).await,
            401,
            CODE_INVALID_CREDENTIALS,
        );
        h.login("member@example.com", new_password).await;
        h
    })
    .await;
}

/// `register` creates a user and signs them in as `login` does; a taken
/// login is 409 `conflict`, and a refused login or name a field error.
#[tokio::test]
async fn register() {
    each_harness(TEST_CONFIG, |h| async move {
        let r = h
            .send(
                "POST",
                "/auth/register",
                Some(
                    json!({"login": "New@Example.com", "name": "Newcomer", "password": USER_PASSWORD}),
                ),
                &[],
            )
            .await;
        expect(&r, 200, "");
        assert_eq!(r.data()["user"]["login"], "new@example.com");
        assert_eq!(r.data()["user"]["name"], "Newcomer");
        let token = bearer(r.data()["token"].as_str().unwrap());
        let me = h
            .send("GET", "/auth/me", None, &[("authorization", &token)])
            .await;
        expect(&me, 200, "");
        assert_eq!(me.data()["roles"], json!([]));
        assert_eq!(me.data()["permissions"], json!([]));

        let unnamed = h
            .send(
                "POST",
                "/auth/register",
                Some(
                    json!({"login": "plain@example.com", "password": USER_PASSWORD, "session": "cookie"}),
                ),
                &[],
            )
            .await;
        expect(&unnamed, 200, "");
        assert_eq!(unnamed.data()["user"]["name"], "plain@example.com");
        assert_eq!(unnamed.cookies.len(), 1);

        let taken = json!({"login": "NEW@example.com", "password": USER_PASSWORD});
        expect(
            &h.send("POST", "/auth/register", Some(taken), &[]).await,
            409,
            CODE_CONFLICT,
        );
        let refused = json!({"login": "not an email", "password": USER_PASSWORD});
        let bad = h.send("POST", "/auth/register", Some(refused), &[]).await;
        expect(&bad, 400, "bad_request");
        assert!(bad.body["errors"]["login"].is_array(), "{}", bad.body);
        let long = json!({"login": "long@example.com", "name": "n".repeat(81), "password": USER_PASSWORD});
        let long = h.send("POST", "/auth/register", Some(long), &[]).await;
        expect(&long, 400, "bad_request");
        assert_eq!(
            long.body["errors"]["name"][0]["validator"],
            "parse",
            "{}",
            long.body
        );
        h
    })
    .await;
}

/// The administration routes need their permissions, and no one grants what
/// they do not hold.
#[tokio::test]
async fn administration() {
    each_harness(TEST_CONFIG, administer).await;
}

async fn administer(h: Harness) -> Harness {
    let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
    let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let clerk_id = administer_users(&h, &admin, &member).await;
    let (clerk, reader_id) = administer_roles(&h, &admin, &clerk_id).await;
    administer_grants(&h, &admin, &member, &clerk, &clerk_id, &reader_id).await;
    administer_passwords(&h, &admin, &member, &clerk, &clerk_id).await;
    h
}

/// Users are created, listed and found with users.read and users.write.
async fn administer_users(h: &Harness, admin: &str, member: &str) -> String {
    let a = [("authorization", admin)];
    let m = [("authorization", member)];

    expect(
        &h.send("GET", "/auth/admin/users", None, &m).await,
        403,
        CODE_FORBIDDEN,
    );
    expect(
        &h.send("GET", "/auth/admin/users", None, &[]).await,
        401,
        CODE_UNAUTHORIZED,
    );

    let clerk_input =
        json!({"login": "clerk@example.com", "name": "Clerk", "password": USER_PASSWORD});
    let created = h
        .send("POST", "/auth/admin/users", Some(clerk_input), &a)
        .await;
    expect(&created, 200, "");
    let clerk_id = created.data()["id"].as_str().unwrap().to_owned();
    assert_eq!(created.data()["disabled"], false);
    assert_eq!(created.data()["roles"], json!([]));
    let again = json!({"login": "CLERK@example.com", "password": USER_PASSWORD});
    expect(
        &h.send("POST", "/auth/admin/users", Some(again), &a).await,
        409,
        CODE_CONFLICT,
    );
    let other = json!({"login": "x@example.com", "password": USER_PASSWORD});
    expect(
        &h.send("POST", "/auth/admin/users", Some(other), &m).await,
        403,
        CODE_FORBIDDEN,
    );

    let users = h.send("GET", "/auth/admin/users", None, &a).await;
    expect(&users, 200, "");
    let logins: Vec<&str> = users
        .data()
        .as_array()
        .unwrap()
        .iter()
        .map(|u| u["login"].as_str().unwrap())
        .collect();
    assert_eq!(
        logins,
        [
            "admin@example.com",
            "clerk@example.com",
            "member@example.com"
        ]
    );
    let user = |id: &str| format!("/auth/admin/users/{id}");
    expect(&h.send("GET", &user(&clerk_id), None, &a).await, 200, "");
    expect(
        &h.send("GET", &user(MISSING), None, &a).await,
        404,
        CODE_NOT_FOUND,
    );
    expect(
        &h.send("GET", &user("not-a-key"), None, &a).await,
        404,
        CODE_NOT_FOUND,
    );

    clerk_id
}

/// A manager may write roles but holds only orders.read, so writes only
/// roles of what it holds.
async fn administer_roles(h: &Harness, admin: &str, clerk_id: &str) -> (String, String) {
    let a = [("authorization", admin)];
    let manager = json!({"name": "manager", "permissions": ["identity.roles", "orders.read"]});
    let manager = h.send("POST", "/auth/admin/roles", Some(manager), &a).await;
    expect(&manager, 200, "");
    let manager_id = manager.data()["id"].as_str().unwrap().to_owned();
    let grant = |user: &str, role: &str| format!("/auth/admin/users/{user}/roles/{role}");
    expect(
        &h.send("PUT", &grant(clerk_id, &manager_id), None, &a).await,
        200,
        "",
    );
    let clerk = bearer(&h.login("clerk@example.com", USER_PASSWORD).await);
    let c = [("authorization", clerk.as_str())];

    let writer = json!({"name": "writer", "permissions": ["orders.read", "orders.write"]});
    let denied = h.send("POST", "/auth/admin/roles", Some(writer), &c).await;
    expect(&denied, 403, CODE_FORBIDDEN);
    assert_eq!(
        denied.body["details"],
        json!({"permissions": ["orders.write"]})
    );
    let reader = json!({"name": "reader", "permissions": ["orders.read", "orders.read.archive", "orders.read"]});
    let reader = h.send("POST", "/auth/admin/roles", Some(reader), &c).await;
    expect(&reader, 200, "");
    assert_eq!(
        reader.data()["permissions"],
        json!(["orders.read", "orders.read.archive"])
    );
    let reader_id = reader.data()["id"].as_str().unwrap().to_owned();
    let role = |id: &str| format!("/auth/admin/roles/{id}");
    let broader = json!({"name": "reader", "permissions": ["orders"]});
    expect(
        &h.send("PUT", &role(&reader_id), Some(broader), &c).await,
        403,
        CODE_FORBIDDEN,
    );
    let bad = json!({"name": "bad", "permissions": ["orders read", "a..b"]});
    let invalid = h.send("POST", "/auth/admin/roles", Some(bad), &a).await;
    expect(&invalid, 422, CODE_INVALID_PERMISSION);
    assert_eq!(
        invalid.body["details"],
        json!({"permissions": ["orders read", "a..b"]})
    );
    let taken = json!({"name": "reader", "permissions": []});
    expect(
        &h.send("POST", "/auth/admin/roles", Some(taken), &a).await,
        409,
        CODE_CONFLICT,
    );
    let unnamed = json!({"name": "", "permissions": []});
    expect(
        &h.send("POST", "/auth/admin/roles", Some(unnamed), &a).await,
        400,
        "bad_request",
    );

    (clerk, reader_id)
}

/// The admin's role carries orders, which the clerk does not hold; a role
/// it does hold it grants, revokes, renames and deletes.
async fn administer_grants(
    h: &Harness,
    admin: &str,
    member: &str,
    clerk: &str,
    clerk_id: &str,
    reader_id: &str,
) {
    let a = [("authorization", admin)];
    let m = [("authorization", member)];
    let c = [("authorization", clerk)];
    let grant = |user: &str, role: &str| format!("/auth/admin/users/{user}/roles/{role}");
    let role = |id: &str| format!("/auth/admin/roles/{id}");
    let roles = h.send("GET", "/auth/admin/roles", None, &a).await;
    expect(&roles, 200, "");
    let admin_role = roles
        .data()
        .as_array()
        .unwrap()
        .iter()
        .find(|r| r["name"] == "admin")
        .map(|r| r["id"].as_str().unwrap().to_owned())
        .unwrap();
    expect(
        &h.send("PUT", &grant(clerk_id, &admin_role), None, &c).await,
        403,
        CODE_FORBIDDEN,
    );
    let granted = h
        .send("PUT", &grant(&h.member_id, reader_id), None, &c)
        .await;
    expect(&granted, 200, "");
    assert_eq!(
        granted.data()["roles"],
        json!([{"id": reader_id, "name": "reader"}])
    );
    expect(
        &h.send("PUT", &grant(&h.member_id, MISSING), None, &a).await,
        404,
        CODE_NOT_FOUND,
    );
    let revoked = h
        .send("DELETE", &grant(&h.member_id, reader_id), None, &c)
        .await;
    expect(&revoked, 200, "");
    assert_eq!(revoked.data()["roles"], json!([]));

    let renamed = json!({"name": "order reader", "permissions": ["orders.read"]});
    let updated = h.send("PUT", &role(reader_id), Some(renamed), &c).await;
    expect(&updated, 200, "");
    assert_eq!(updated.data()["name"], "order reader");
    let deleted = h.send("DELETE", &role(reader_id), None, &c).await;
    expect(&deleted, 200, "");
    assert_eq!(deleted.data(), &json!(true));
    expect(
        &h.send("DELETE", &role(reader_id), None, &c).await,
        404,
        CODE_NOT_FOUND,
    );
    expect(
        &h.send("GET", "/auth/admin/roles", None, &m).await,
        403,
        CODE_FORBIDDEN,
    );
}

/// Setting a user's password revokes their sessions, and disabling a user
/// ends theirs.
async fn administer_passwords(h: &Harness, admin: &str, member: &str, clerk: &str, clerk_id: &str) {
    let a = [("authorization", admin)];
    let m = [("authorization", member)];
    let c = [("authorization", clerk)];
    let password = |id: &str| format!("/auth/admin/users/{id}/password");
    let reset = || Some(json!({"password": "reset password"}));
    let set = h.send("PUT", &password(&h.member_id), reset(), &a).await;
    expect(&set, 200, "");
    assert_eq!(set.data(), &json!(true));
    expect(
        &h.send("GET", "/auth/me", None, &m).await,
        401,
        CODE_UNAUTHORIZED,
    );
    h.login("member@example.com", "reset password").await;
    expect(
        &h.send(
            "PUT",
            &password(&h.member_id),
            Some(json!({"password": "short"})),
            &a,
        )
        .await,
        400,
        "bad_request",
    );
    expect(
        &h.send("PUT", &password(MISSING), reset(), &a).await,
        404,
        CODE_NOT_FOUND,
    );

    let disable = format!("/auth/admin/users/{clerk_id}/disable");
    let disabled = h.send("POST", &disable, None, &a).await;
    expect(&disabled, 200, "");
    assert_eq!(disabled.data()["disabled"], true);
    expect(
        &h.send("GET", "/auth/me", None, &c).await,
        401,
        CODE_UNAUTHORIZED,
    );
}

/// Setting a user's password, disabling them and enabling them need the
/// caller's permissions to cover every permission the user holds through
/// their roles, as a grant does. A helpdesk holding identity.users may
/// write a member, who holds nothing, and not the admin, whose role
/// carries identity and orders: 403 listing them, the admin's password
/// and session untouched.
#[tokio::test]
async fn a_user_write_needs_their_permissions() {
    each_harness(TEST_CONFIG, |h| async move {
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let a = [("authorization", admin.as_str())];
        let role = json!({"name": "helpdesk", "permissions": ["identity.users"]});
        let role = h.send("POST", "/auth/admin/roles", Some(role), &a).await;
        expect(&role, 200, "");
        let user =
            json!({"login": "helpdesk@example.com", "name": "Helpdesk", "password": USER_PASSWORD});
        let user = h.send("POST", "/auth/admin/users", Some(user), &a).await;
        expect(&user, 200, "");
        let grant = format!(
            "/auth/admin/users/{}/roles/{}",
            user.data()["id"].as_str().unwrap(),
            role.data()["id"].as_str().unwrap()
        );
        expect(&h.send("PUT", &grant, None, &a).await, 200, "");
        let helpdesk = bearer(&h.login("helpdesk@example.com", USER_PASSWORD).await);
        let d = [("authorization", helpdesk.as_str())];

        let target = format!("/auth/admin/users/{}", h.admin_id);
        for (method, path, body) in [
            (
                "PUT",
                format!("{target}/password"),
                Some(json!({"password": "taken over"})),
            ),
            ("POST", format!("{target}/disable"), None),
            ("POST", format!("{target}/enable"), None),
        ] {
            let refused = h.send(method, &path, body, &d).await;
            expect(&refused, 403, CODE_FORBIDDEN);
            assert_eq!(
                refused.body["details"],
                json!({"permissions": ["identity", "orders"]}),
                "{method} {path}"
            );
        }
        expect(&h.send("GET", "/auth/me", None, &a).await, 200, "");
        h.login("admin@example.com", ADMIN_PASSWORD).await;
        let missing = format!("/auth/admin/users/{MISSING}/password");
        let reset = || Some(json!({"password": "reset password"}));
        expect(
            &h.send("PUT", &missing, reset(), &d).await,
            404,
            CODE_NOT_FOUND,
        );

        let member = format!("/auth/admin/users/{}", h.member_id);
        let set = h
            .send("PUT", &format!("{member}/password"), reset(), &d)
            .await;
        expect(&set, 200, "");
        expect(
            &h.send("POST", &format!("{member}/disable"), None, &d).await,
            200,
            "",
        );
        expect(
            &h.send("POST", &format!("{member}/enable"), None, &d).await,
            200,
            "",
        );
        h.login("member@example.com", "reset password").await;
        h
    })
    .await;
}

/// A login whose hash has another cost writes it again at the config's.
#[tokio::test]
async fn login_rehashes() {
    each_harness(TEST_CONFIG, |h| async move {
        let older = Argon2Params {
            memory_kib: 32,
            iterations: 1,
            parallelism: 1,
        };
        let old = hash_password(USER_PASSWORD, older).unwrap();
        h.store
            .set_password(&h.member_id, &old, h.clock.now(), None)
            .await
            .unwrap();
        h.login("member@example.com", USER_PASSWORD).await;
        let rec = h.store.find_login("member@example.com").await.unwrap();
        assert!(
            rec.password_hash
                .starts_with("$argon2id$v=19$m=64,t=1,p=1$"),
            "after login the hash is {}",
            rec.password_hash
        );
        h.login("member@example.com", USER_PASSWORD).await;
        h
    })
    .await;
}

/// The trusted origin's preflight is answered with credentials; another
/// origin's passes through without CORS headers.
#[tokio::test]
async fn cors() {
    let h = harness(sqlite(), TEST_CONFIG).await;
    let r = h
        .send(
            "OPTIONS",
            "/auth/login",
            None,
            &[
                ("origin", "https://app.example.com"),
                ("access-control-request-method", "POST"),
                ("access-control-request-headers", "content-type"),
            ],
        )
        .await;
    assert_eq!(r.status, StatusCode::NO_CONTENT);
    assert_eq!(
        r.headers["access-control-allow-origin"],
        "https://app.example.com"
    );
    assert_eq!(r.headers["access-control-allow-credentials"], "true");
    assert_eq!(r.headers["access-control-allow-methods"], "POST");
    assert_eq!(r.headers["access-control-allow-headers"], "content-type");
    assert_eq!(r.headers["access-control-max-age"], "600");
    let other = h
        .send(
            "OPTIONS",
            "/auth/login",
            None,
            &[
                ("origin", "https://evil.example"),
                ("access-control-request-method", "POST"),
            ],
        )
        .await;
    assert_ne!(other.status, StatusCode::NO_CONTENT);
    assert!(other.headers.get("access-control-allow-origin").is_none());
    let vary: Vec<&str> = other
        .headers
        .get_all("vary")
        .iter()
        .map(|v| v.to_str().unwrap())
        .collect();
    assert_eq!(vary, ["Origin"]);
    let plain = h.send("GET", "/auth/me", None, &[]).await;
    assert!(plain.headers.get("vary").is_none(), "no Origin, no Vary");
}

/// Every operation the contract names has a handler, and the routes follow
/// the sets' options.
#[tokio::test]
async fn handlers() {
    let h = harness(sqlite(), TEST_CONFIG).await;
    for op in OPERATIONS {
        assert!(
            h.service.handler::<()>(op.name).is_some(),
            "no handler for {}",
            op.name
        );
    }
    assert!(h.service.handler::<()>("refresh").is_none());
    let paths =
        |sessions: Option<&UserSessions>, admin: Option<&UserAdministration>| -> Vec<String> {
            h.service
                .routes::<()>(sessions, admin)
                .into_iter()
                .map(|(method, path, _)| format!("{method} {path}"))
                .collect()
        };
    let no_login = UserSessions {
        path: "account".to_owned(),
        no_login: true,
        register: false,
    };
    assert_eq!(
        paths(Some(&no_login), None),
        ["GET /account/me", "GET /account/capabilities"]
    );
    let plain = paths(Some(&UserSessions::default()), None);
    assert!(plain.contains(&"POST /auth/login".to_owned()), "{plain:?}");
    assert!(!plain.iter().any(|p| p.contains("register")), "{plain:?}");
    let every = paths(
        Some(&UserSessions {
            register: true,
            ..UserSessions::default()
        }),
        Some(&UserAdministration::default()),
    );
    // The API key operations are routed only when turned on (D114).
    let api_key_ops = OPERATIONS.iter().filter(|op| op.api_keys).count();
    assert_eq!(every.len(), OPERATIONS.len() - api_key_ops);
    assert!(
        every.contains(&"PUT /auth/admin/users/{id}/roles/{roleId}".to_owned()),
        "{every:?}"
    );
}

/// A handler mounted alone, as a manual operation at the route the
/// generated crate names, behind the router's controls: the caller the
/// router's authenticator put on the request reaches it.
#[tokio::test]
async fn a_handler_mounts_alone_behind_the_routers_controls() {
    let h = harness(sqlite(), TEST_CONFIG).await;
    let authenticator: Arc<dyn Authenticator> =
        Arc::new(IdentityAuthenticator::new(Arc::clone(&h.service)));
    let me = RouteControls::new()
        .authorize(authenticator, &[])
        .apply(h.service.handler("me").unwrap());
    let users = RouteControls::new().apply(h.service.handler("getUser").unwrap());
    let router: Router = Router::new()
        .route("/api/auth/me", me)
        .route("/api/auth/admin/users/{id}", users)
        .layer(axum::middleware::from_fn(request_ids));
    let token = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
    let send = |path: String, token: Option<String>| {
        let router = router.clone();
        async move {
            let mut request = Request::get(path);
            if let Some(token) = token {
                request = request.header("authorization", token);
            }
            let response = router
                .oneshot(request.body(Body::empty()).unwrap())
                .await
                .unwrap();
            let status = response.status();
            let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
                .await
                .unwrap();
            (status, serde_json::from_slice::<Value>(&bytes).unwrap())
        }
    };
    let (status, body) = send("/api/auth/me".to_owned(), Some(token.clone())).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"]["user"]["id"], h.admin_id);
    let (status, body) = send("/api/auth/me".to_owned(), None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
    let (status, body) = send(
        format!("/api/auth/admin/users/{}", h.member_id),
        Some(token),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"]["login"], "member@example.com");
}

/// The router's authenticator hands an in-process caller the session's
/// principal, which `OperationInfo::admit` admits by the route's rule, and
/// which the identity runtime reads back.
#[tokio::test]
async fn an_in_process_caller_is_admitted_by_the_routes_rule() {
    let h = harness(sqlite(), TEST_CONFIG).await;
    let token = h.login("admin@example.com", ADMIN_PASSWORD).await;
    let authenticator = IdentityAuthenticator::new(Arc::clone(&h.service));
    let (parts, ()) = Request::get("/api/orders")
        .header("authorization", bearer(&token))
        .body(())
        .unwrap()
        .into_parts();
    let principal = authenticator.authenticate(&parts).await.unwrap().unwrap();
    assert_eq!(principal.subject, h.admin_id);
    assert_eq!(principal.permissions, ["identity", "orders"]);
    assert_eq!(principal.claims["name"], "Admin");
    assert_eq!(principal.claims["transport"], "bearer");
    assert!(principal.claims["sessionId"].is_string());
    let caller = IdentityPrincipal::from_principal(&principal).unwrap();
    assert_eq!(caller.login, "admin@example.com");
    assert_eq!(caller.roles[0].name, "admin");

    const AUDIT: OperationInfo = OperationInfo {
        namespace: "orders",
        name: "audit",
        method: "GET",
        path: "/api/orders/audit",
        requires_auth: true,
        permissions: &["orders.audit"],
        require_ownership: false,
        manual: false,
        rate_limit: None,
        service: None,
    };
    assert!(AUDIT
        .admit(&authenticator, Some(principal))
        .unwrap()
        .is_some());
    let (parts, ()) = Request::get("/api/orders").body(()).unwrap().into_parts();
    assert!(
        authenticator.authenticate(&parts).await.unwrap().is_none(),
        "no credential, no caller"
    );
    let err = AUDIT.admit(&authenticator, None).unwrap_err();
    assert_eq!(err.status, StatusCode::UNAUTHORIZED);
}

/// A login for an account that does not exist verifies a hash at the
/// config's cost, so it takes as long as a wrong password.
#[tokio::test]
async fn an_unknown_login_takes_a_verify() {
    let costly =
        r#"{"password": {"argon2": {"memoryKiB": 4096, "iterations": 2, "parallelism": 1}}}"#;
    let h = harness(sqlite(), costly).await;
    let fastest = |body: Value| {
        let h = &h;
        async move {
            let mut best = Duration::MAX;
            for _ in 0..3 {
                let start = Instant::now();
                expect(
                    &h.send("POST", "/auth/login", Some(body.clone()), &[]).await,
                    401,
                    CODE_INVALID_CREDENTIALS,
                );
                best = best.min(start.elapsed());
            }
            best
        }
    };
    let wrong = fastest(json!({"login": "member@example.com", "password": "wrong password"})).await;
    let unknown =
        fastest(json!({"login": "nobody@example.com", "password": "wrong password"})).await;
    let refused = fastest(json!({"login": "nobody", "password": "wrong password"})).await;
    assert!(
        unknown >= wrong / 3 && refused >= wrong / 3,
        "a wrong password takes {wrong:?}, an unknown login {unknown:?} and a refused one {refused:?}"
    );
}

// API keys (D114): authentication by a key, and the five operations.

/// The refusals' details, the same text in every runtime.
const SESSION_ONLY_DETAIL: &str =
    "An API key does not act on its own account: sign in with a session";
const NOT_HELD_DETAIL: &str = "An API key names only permissions its user holds";
const NO_API_KEYS_DETAIL: &str =
    "API keys are not available: this database's types hold no API key table";

impl Harness {
    /// Sends a request as the caller whose `Authorization` header is
    /// `authorization`, or as no one when it is empty.
    async fn call(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
        authorization: &str,
    ) -> Reply {
        if authorization.is_empty() {
            return self.send(method, path, body, &[]).await;
        }
        self.send(method, path, body, &[("authorization", authorization)])
            .await
    }

    /// Makes the caller an API key and answers createApiKey's data.
    async fn create_key(&self, caller: &str, body: Value) -> Value {
        let r = self.call("POST", "/auth/keys", Some(body), caller).await;
        expect(&r, 200, "");
        let token = r.data()["token"].as_str().unwrap_or("");
        assert!(
            token.len() == API_KEY_LENGTH && token.starts_with(API_KEY_PREFIX),
            "createApiKey answered the token {token:?}"
        );
        r.data().clone()
    }

    async fn me(&self, caller: &str) -> Reply {
        self.call("GET", "/auth/me", None, caller).await
    }

    /// The keys `path` lists for `caller`.
    async fn keys(&self, path: &str, caller: &str) -> Vec<Value> {
        let r = self.call("GET", path, None, caller).await;
        expect(&r, 200, "");
        r.data().as_array().cloned().expect("a list of keys")
    }

    /// The names of the keys `path` lists for `caller`, in its order.
    async fn key_names(&self, path: &str, caller: &str) -> String {
        let names: Vec<String> = self
            .keys(path, caller)
            .await
            .iter()
            .map(|key| key["name"].as_str().unwrap_or("").to_owned())
            .collect();
        names.join(",")
    }
}

/// The bearer header of a createApiKey answer's token.
fn key_of(created: &Value) -> String {
    bearer(text(created, "token"))
}

/// A string member of a JSON object.
fn text<'a>(value: &'a Value, member: &str) -> &'a str {
    value[member].as_str().unwrap_or("")
}

/// Fails unless `r` is the problem with `status`, `code` and `detail`.
#[track_caller]
fn expect_detail(r: &Reply, status: u16, code: &str, detail: &str) {
    expect(r, status, code);
    assert_eq!(r.body["detail"], detail, "{}", r.body);
}

/// Fails unless `r` is 400 `bad_request` refusing `field` by `validator`.
#[track_caller]
fn expect_field(r: &Reply, field: &str, validator: &str) {
    expect(r, 400, "bad_request");
    let refusals = r.body["errors"][field].as_array();
    assert!(
        refusals.is_some_and(|l| l.len() == 1 && l[0]["validator"] == validator),
        "got the errors {}, want {field} refused by {validator}",
        r.body["errors"]
    );
}

/// Fails unless `r` is the answer of an operation the contract types as
/// true: 200 with the envelope of `true`, as every generated server
/// answers one.
#[track_caller]
fn expect_true(r: &Reply) {
    expect(r, 200, "");
    assert_eq!(r.data(), &json!(true), "{}", r.body);
    assert!(r.body["meta"].is_object(), "{}", r.body);
}

/// Runs `test` on a harness over each database, with both sets' API key
/// operations turned on.
async fn each_key_harness<F, Fut>(config: &str, test: F)
where
    F: Fn(Harness) -> Fut,
    Fut: Future<Output = Harness>,
{
    for db in databases().await {
        eprintln!("on {}", db.name);
        let descriptor = support::descriptor(|_| {});
        let h = test(harness_over(db, &descriptor, config, with_api_keys).await).await;
        h.db.close().await;
    }
}

/// createApiKey answers the key's token once, the row keeps its hash, and
/// the list shows the key without either; a request carrying the key in
/// `Authorization` is its user's, and writes lastUsedAt once per touch
/// interval.
#[tokio::test]
async fn api_key_create_and_list() {
    each_key_harness(TEST_CONFIG, |h| async move {
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let ci = Some(json!({"name": "ci"}));
        let unsigned = h.call("POST", "/auth/keys", ci, "").await;
        expect(&unsigned, 401, CODE_UNAUTHORIZED);
        let unsigned = h.call("GET", "/auth/keys", None, "").await;
        expect(&unsigned, 401, CODE_UNAUTHORIZED);
        let empty = h.keys("/auth/keys", &member).await;
        assert!(empty.is_empty(), "a user without keys lists {empty:?}");

        let created = h
            .create_key(&member, json!({"name": "  ci runner \n"}))
            .await;
        let (token, id) = (text(&created, "token"), text(&created, "id"));
        // A key with every permission has no permissions member, and one
        // never used no lastUsedAt.
        let shown = json!({"id": id, "name": "ci runner", "createdAt": "2026-10-08T12:00:00Z", "expiresAt": "2027-10-08T12:00:00Z"});
        let mut want = shown.clone();
        want["token"] = json!(token);
        assert_eq!(created, want, "createApiKey");
        assert!(
            !id.is_empty() && !id.contains('-'),
            "the key's id {id:?} is not the base62 form of its Identity.UUID"
        );
        let rec = h.store.find_api_key(&hash_token(token)).await.unwrap();
        assert_eq!(
            (rec.api_key.id.as_str(), rec.user.id.as_str(), rec.api_key.name.as_str()),
            (id, h.member_id.as_str(), "ci runner"),
            "the row of the key"
        );
        let list = h.keys("/auth/keys", &member).await;
        assert_eq!(list, [shown], "listApiKeys");

        a_key_authenticates(&h, token, id).await;
        a_key_is_touched(&h, &member, token).await;

        // Newest first, and each user's own.
        h.clock.advance(Duration::from_secs(60));
        h.create_key(&member, json!({"name": "second"})).await;
        let names = h.key_names("/auth/keys", &member).await;
        assert_eq!(names, "second,ci runner", "the newest first");
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let admins = h.keys("/auth/keys", &admin).await;
        assert!(admins.is_empty(), "the admin lists the member's keys");
        h
    })
    .await;
}

/// The key authenticates as its user, in `Authorization` alone.
async fn a_key_authenticates(h: &Harness, token: &str, id: &str) {
    h.clock.advance(Duration::from_secs(60));
    let me = h.me(&bearer(token)).await;
    expect(&me, 200, "");
    assert_eq!(
        me.data()["user"],
        json!({"id": h.member_id, "login": "member@example.com", "name": "Member"}),
        "a key's me"
    );
    let p = h
        .service
        .authenticate_token(token, Transport::Bearer)
        .await
        .unwrap();
    assert_eq!(
        (
            p.id.as_str(),
            p.api_key_id.as_str(),
            p.session_id.as_str(),
            p.transport
        ),
        (h.member_id.as_str(), id, "", Transport::Bearer),
        "a key's principal"
    );
    let as_cookie = h.service.authenticate_token(token, Transport::Cookie).await;
    assert!(
        as_cookie.is_err(),
        "a key authenticated as a cookie's token"
    );
    let cookie = session_cookie(token);
    let in_cookie = h
        .send("GET", "/auth/me", None, &[("cookie", &cookie)])
        .await;
    expect(&in_cookie, 401, CODE_UNAUTHORIZED);
    assert_eq!(
        in_cookie.cookies,
        [h.config.clear_cookie()],
        "a cookie carrying a key is cleared"
    );
    let bent = format!("{}!", &token[..token.len() - 1]);
    expect(&h.me(&bearer(&bent)).await, 401, CODE_UNAUTHORIZED);
    let other = new_api_key().unwrap();
    expect(&h.me(&bearer(&other)).await, 401, CODE_UNAUTHORIZED);
}

/// lastUsedAt is the first request's time, and moves once the touch
/// interval has passed.
async fn a_key_is_touched(h: &Harness, member: &str, token: &str) {
    let last_used = || async {
        let keys = h.keys("/auth/keys", member).await;
        keys.last().map(|key| key["lastUsedAt"].clone())
    };
    let first = Some(json!("2026-10-08T12:01:00Z"));
    assert_eq!(last_used().await, first, "after its first request");
    h.clock.advance(Duration::from_secs(59));
    expect(&h.me(&bearer(token)).await, 200, "");
    assert_eq!(last_used().await, first, "within the touch interval");
    h.clock.advance(Duration::from_secs(2));
    expect(&h.me(&bearer(token)).await, 200, "");
    assert_eq!(
        last_used().await,
        Some(json!("2026-10-08T12:02:01Z")),
        "after the touch interval"
    );
}

/// The idle timeout and the session's lifetime end a session and no API
/// key, which ends at its own expiry.
#[tokio::test]
async fn an_api_key_outlives_the_idle_timeout() {
    let idle = r#"{"sessionTtlSeconds": 3600, "idleTimeoutSeconds": 600, "touchIntervalSeconds": 60, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
    each_key_harness(idle, |h| async move {
        let signed_in = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let key = key_of(&h.create_key(&signed_in, json!({"name": "ci"})).await);

        h.clock.advance(Duration::from_secs(601));
        expect(&h.me(&signed_in).await, 401, CODE_UNAUTHORIZED);
        expect(&h.me(&key).await, 200, "");
        // Unused for longer than the idle timeout, and past a session's
        // lifetime.
        h.clock.advance(Duration::from_secs(2 * 3600));
        expect(&h.me(&key).await, 200, "");
        h.clock.advance(Duration::from_secs(364 * 24 * 3600));
        expect(&h.me(&key).await, 200, "");
        h.clock.advance(Duration::from_secs(24 * 3600));
        expect(&h.me(&key).await, 401, CODE_UNAUTHORIZED);
        h
    })
    .await;
}

/// A key's times are the clock's to the millisecond, which every database
/// keeps, so createApiKey answers what the row holds; and a key ends no
/// later than the year 9999, as a session does, however long the config
/// lets it last.
#[tokio::test]
async fn an_api_keys_times() {
    let endless = r#"{"apiKeyMaxTtlSeconds": 9223372036854775807, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
    each_key_harness(endless, |h| async move {
        h.clock.advance(Duration::from_nanos(1_234_567));
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let short = json!({"name": "short", "expiresInSeconds": 60});
        let short = h.create_key(&member, short).await;
        let times = |key: &Value| (key["createdAt"].clone(), key["expiresAt"].clone());
        assert_eq!(
            times(&short),
            (
                json!("2026-10-08T12:00:00.001Z"),
                json!("2026-10-08T12:01:00.001Z")
            ),
            "a key made between two milliseconds"
        );
        let longest = h.create_key(&member, json!({"name": "longest"})).await;
        assert_eq!(longest["expiresAt"], "9999-12-31T23:59:59Z");
        expect(&h.me(&key_of(&longest)).await, 200, "");
        let listed = h.keys("/auth/keys", &member).await;
        for created in [&short, &longest] {
            let row = listed.iter().find(|key| key["id"] == created["id"]);
            assert_eq!(
                row.map(times),
                Some(times(created)),
                "the row of {}",
                created["name"]
            );
        }
        h
    })
    .await;
}

/// A key ends at its expiry, when it is revoked, and when its user is
/// disabled, for good; a password change, the user's own or an
/// administrator's, ends the sessions and not the key.
#[tokio::test]
async fn api_key_ends() {
    each_key_harness(TEST_CONFIG, |h| async move {
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        a_key_expires(&h, &member).await;
        let admins_own = a_key_is_revoked(&h, &member, &admin).await;
        a_key_outlives_a_password(&h, &member, &admin, &admins_own).await;
        a_pending_users_key(&h).await;
        h
    })
    .await;
}

/// Its expiry: the key lives while now is before expiresAt.
async fn a_key_expires(h: &Harness, member: &str) {
    let short = h
        .create_key(member, json!({"name": "short", "expiresInSeconds": 60}))
        .await;
    assert_eq!(short["expiresAt"], "2026-10-08T12:01:00Z", "60 seconds on");
    h.clock.advance(Duration::from_secs(59));
    expect(&h.me(&key_of(&short)).await, 200, "");
    h.clock.advance(Duration::from_secs(1));
    expect(&h.me(&key_of(&short)).await, 401, CODE_UNAUTHORIZED);
    let listed = h.key_names("/auth/keys", member).await;
    assert_eq!(listed, "short", "an expired key is listed");
}

/// A revocation, which its user makes once. It answers the admin's own
/// key, which lives on.
async fn a_key_is_revoked(h: &Harness, member: &str, admin: &str) -> Value {
    let revoked = h.create_key(member, json!({"name": "revoked"})).await;
    let admins_own = h.create_key(admin, json!({"name": "admin's"})).await;
    expect(&h.me(&key_of(&revoked)).await, 200, "");
    let path = format!("/auth/keys/{}", text(&revoked, "id"));
    let unsigned = h.call("DELETE", &path, None, "").await;
    expect(&unsigned, 401, CODE_UNAUTHORIZED);
    expect_true(&h.call("DELETE", &path, None, member).await);
    expect(&h.me(&key_of(&revoked)).await, 401, CODE_UNAUTHORIZED);
    for (name, id) in [
        ("a key revoked already", text(&revoked, "id")),
        ("another user's key", text(&admins_own, "id")),
        ("an unknown key", MISSING),
        ("an id that is no key", "not-a-key"),
    ] {
        let path = format!("/auth/keys/{id}");
        let r = h.call("DELETE", &path, None, member).await;
        assert_eq!(
            (r.status.as_u16(), r.code(), &r.body["detail"]),
            (404, CODE_NOT_FOUND, &json!("API key not found")),
            "revoking {name}"
        );
    }
    expect(&h.me(&key_of(&admins_own)).await, 200, "");
    let listed = h.key_names("/auth/keys", member).await;
    assert_eq!(listed, "short", "a revoked key is not listed");
    admins_own
}

/// A password change ends the user's other sessions, not their keys; their
/// user disabled, the keys are revoked with the sessions, and enabling
/// brings none back.
async fn a_key_outlives_a_password(h: &Harness, member: &str, admin: &str, admins_own: &Value) {
    let lasting = key_of(&h.create_key(member, json!({"name": "lasting"})).await);
    let other = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let change = json!({"current": USER_PASSWORD, "password": "a new password"});
    expect_true(&h.call("POST", "/auth/password", Some(change), member).await);
    expect(&h.me(&other).await, 401, CODE_UNAUTHORIZED);
    expect(&h.me(&lasting).await, 200, "");
    let user = format!("/auth/admin/users/{}", h.member_id);
    let reset = Some(json!({"password": "reset password"}));
    expect_true(
        &h.call("PUT", &format!("{user}/password"), reset, admin)
            .await,
    );
    expect(&h.me(member).await, 401, CODE_UNAUTHORIZED);
    expect(&h.me(&lasting).await, 200, "");

    let disabled = h
        .call("POST", &format!("{user}/disable"), None, admin)
        .await;
    expect(&disabled, 200, "");
    expect(&h.me(&lasting).await, 401, CODE_UNAUTHORIZED);
    let enabled = h.call("POST", &format!("{user}/enable"), None, admin).await;
    expect(&enabled, 200, "");
    expect(&h.me(&lasting).await, 401, CODE_UNAUTHORIZED);
    let member = bearer(&h.login("member@example.com", "reset password").await);
    let left = h.keys("/auth/keys", &member).await;
    assert!(
        left.is_empty(),
        "after the disable the member's keys are {left:?}"
    );
    expect(&h.me(&key_of(admins_own)).await, 200, "");
}

/// A user who waits for approval is no caller, by a key either.
async fn a_pending_users_key(h: &Harness) {
    let now = h.clock.now();
    let user = NewUser {
        login: "pending@example.com".to_owned(),
        name: String::new(),
        password_hash: "x".to_owned(),
        at: now,
    };
    let grants = NewUserGrants {
        pending: true,
        ..NewUserGrants::default()
    };
    let pending = h.store.create_user_with(user, grants).await.unwrap();
    let token = new_api_key().unwrap();
    let key = NewApiKey {
        user_id: pending.id.clone(),
        name: "early".to_owned(),
        token_hash: hash_token(&token),
        permissions: None,
        created_at: now,
        expires_at: now + Duration::from_secs(3600),
    };
    h.store.create_api_key(key).await.unwrap();
    expect(&h.me(&bearer(&token)).await, 401, CODE_UNAUTHORIZED);
    h.store.approve_user(&pending.id).await.unwrap();
    expect(&h.me(&bearer(&token)).await, 200, "");
}

/// createApiKey's name is 1 to 100 characters once trimmed, its
/// expiresInSeconds 1 to the config's apiKeyMaxTtlSeconds, which is the
/// lifetime without it, and its permissions 1 to 100 valid names, none
/// twice.
#[tokio::test]
async fn api_key_input() {
    let config = r#"{"apiKeyMaxTtlSeconds": 3600, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
    each_key_harness(config, |h| async move {
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let made = key_lifetimes(&h, &admin).await
            + key_names_are_bounded(&h, &admin).await
            + key_permissions_are_bounded(&h, &admin).await
            + key_bodies(&h, &admin).await;
        let listed = h.keys("/auth/keys", &admin).await.len();
        assert_eq!(
            listed, made,
            "the keys that were made are listed: a refused create wrote a row"
        );
        h
    })
    .await;
}

/// A refused createApiKey of the admin's.
async fn refused_key(h: &Harness, admin: &str, body: Value) -> Reply {
    h.call("POST", "/auth/keys", Some(body), admin).await
}

/// The lifetime is 1 to the config's hour, which is also the default. It
/// answers how many keys it made, as the other parts of the test do.
async fn key_lifetimes(h: &Harness, admin: &str) -> usize {
    for (name, body, want) in [
        (
            "without expiresInSeconds",
            json!({"name": "default"}),
            "2026-10-08T13:00:00Z",
        ),
        (
            "of the longest lifetime",
            json!({"name": "longest", "expiresInSeconds": 3600}),
            "2026-10-08T13:00:00Z",
        ),
        (
            "of one second",
            json!({"name": "shortest", "expiresInSeconds": 1}),
            "2026-10-08T12:00:01Z",
        ),
    ] {
        let created = h.create_key(admin, body).await;
        assert_eq!(created["expiresAt"], want, "a key {name}");
    }
    for (seconds, validator) in [(3601, "max"), (0, "min"), (-5, "min")] {
        let body = json!({"name": "x", "expiresInSeconds": seconds});
        let refused = refused_key(h, admin, body).await;
        expect_field(&refused, "expiresInSeconds", validator);
    }
    3
}

/// Characters are counted, not bytes, and after the trim.
async fn key_names_are_bounded(h: &Harness, admin: &str) -> usize {
    for (body, validator) in [
        (json!({}), "required"),
        (json!({"name": ""}), "required"),
        (json!({"name": " \t\n "}), "required"),
        (json!({"name": "n".repeat(101)}), "maxLength"),
        (json!({"name": "\u{e9}".repeat(101)}), "maxLength"),
    ] {
        expect_field(&refused_key(h, admin, body).await, "name", validator);
    }
    let plain = "n".repeat(100);
    let created = h.create_key(admin, json!({"name": plain})).await;
    assert_eq!(created["name"], plain, "a name of 100 characters");
    let accented = "\u{e9}".repeat(100);
    let padded = json!({"name": format!("  {accented}  ")});
    let created = h.create_key(admin, padded).await;
    assert_eq!(created["name"], accented, "a padded name of 100 characters");
    2
}

/// A key names 1 to 100 permissions, each of a permission's form and
/// once.
async fn key_permissions_are_bounded(h: &Harness, admin: &str) -> usize {
    let none = json!({"name": "x", "permissions": []});
    expect_field(&refused_key(h, admin, none).await, "permissions", "listMin");
    let many: Vec<String> = (0..101).map(|i| format!("orders.p{i}")).collect();
    let too_many = json!({"name": "x", "permissions": many});
    let refused = refused_key(h, admin, too_many).await;
    expect_field(&refused, "permissions", "listMax");
    let hundred = json!({"name": "hundred", "permissions": many[..100]});
    let created = h.create_key(admin, hundred).await;
    let named = created["permissions"].as_array().map(Vec::len);
    assert_eq!(named, Some(100), "a key naming 100 permissions");
    for (name, permissions, refused) in [
        (
            "a permission of the wrong form",
            json!(["orders.read", "orders read", "a..b"]),
            json!(["orders read", "a..b"]),
        ),
        (
            "a permission named twice",
            json!(["orders.read", "orders.write", "orders.read"]),
            json!(["orders.read"]),
        ),
        ("an empty permission", json!([""]), json!([""])),
    ] {
        let body = json!({"name": "x", "permissions": permissions});
        let r = refused_key(h, admin, body).await;
        assert_eq!(
            (r.status.as_u16(), r.code(), &r.body["details"]),
            (
                422,
                CODE_INVALID_PERMISSION,
                &json!({"permissions": refused})
            ),
            "{name}"
        );
    }
    1
}

/// Every refused field is named at once, before any permission is read; a
/// null optional member is an absent one; and a body that is not the
/// operation's input is 400.
async fn key_bodies(h: &Harness, admin: &str) -> usize {
    let several = json!({"name": " ", "expiresInSeconds": 0, "permissions": []});
    let several = refused_key(h, admin, several).await;
    expect(&several, 400, "bad_request");
    let refused = several.body["errors"].as_object().map(serde_json::Map::len);
    assert_eq!(refused, Some(3), "{}", several.body);
    let unnamed = json!({"name": "", "permissions": ["orders read"]});
    expect_field(&refused_key(h, admin, unnamed).await, "name", "required");

    let nulls = json!({"name": "nulls", "expiresInSeconds": null, "permissions": null});
    let nulls = h.create_key(admin, nulls).await;
    assert!(
        nulls.get("permissions").is_none() && nulls["expiresAt"] == "2026-10-08T13:00:00Z",
        "a key made with null members = {nulls}"
    );

    for (name, body) in [
        (
            "a fractional lifetime",
            json!({"name": "x", "expiresInSeconds": 1.5}),
        ),
        (
            "a lifetime as a string",
            json!({"name": "x", "expiresInSeconds": "60"}),
        ),
        ("a name that is no string", json!({"name": 5})),
        (
            "permissions as a string",
            json!({"name": "x", "permissions": "orders.read"}),
        ),
        ("an unknown member", json!({"name": "x", "ttl": 60})),
        ("a member in another case", json!({"Name": "x"})),
        ("a body that is a list", json!([])),
    ] {
        let r = refused_key(h, admin, body).await;
        assert_eq!(
            (r.status.as_u16(), r.code()),
            (400, "bad_request"),
            "{name}: {}",
            r.body
        );
    }
    1
}

/// A key made with permissions holds those alone, each while a role of its
/// user covers it; a key names only what its user holds; and one made with
/// none holds all its user does.
#[tokio::test]
async fn api_key_permissions() {
    each_key_harness(TEST_CONFIG, |h| async move {
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let named = json!(["orders.read", "identity.users.read"]);
        let created = h
            .create_key(&admin, json!({"name": "reader", "permissions": named}))
            .await;
        assert_eq!(created["permissions"], named, "createApiKey's permissions");
        let key = key_of(&created);
        let listed = h.keys("/auth/keys", &admin).await;
        assert_eq!(listed[0]["permissions"], named, "listApiKeys' permissions");

        a_key_holds_what_it_names(&h, &key).await;
        a_key_follows_its_users_roles(&h, &key).await;

        // A key made without permissions holds all its user does.
        let whole = h.create_key(&admin, json!({"name": "whole"})).await;
        assert!(whole.get("permissions").is_none(), "{whole}");
        let me = h.me(&key_of(&whole)).await;
        assert_eq!(me.data()["permissions"], json!(["identity", "orders"]));
        let order = h.call("POST", "/orders", None, &key_of(&whole)).await;
        expect(&order, 200, "");

        a_key_names_what_its_user_holds(&h, &admin).await;
        h
    })
    .await;
}

/// Fails unless the key's `me` answers the roles named `roles` and
/// `permissions`.
#[track_caller]
fn expect_holds(me: &Reply, roles: &[&str], permissions: &Value) {
    expect(me, 200, "");
    let names: Vec<&str> = me.data()["roles"]
        .as_array()
        .map(|roles| roles.iter().map(|role| text(role, "name")).collect())
        .unwrap_or_default();
    assert_eq!(
        (names.as_slice(), &me.data()["permissions"]),
        (roles, permissions),
        "the key's me = {}",
        me.data()
    );
}

/// The admin's role holds identity and orders: the key holds what it names
/// of them, and its routes follow.
async fn a_key_holds_what_it_names(h: &Harness, key: &str) {
    let named = json!(["orders.read", "identity.users.read"]);
    expect_holds(&h.me(key).await, &["admin"], &named);
    let caps = h.call("GET", "/auth/capabilities", None, key).await;
    assert_eq!(
        caps.data()["operations"],
        json!({"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": true}),
        "the key's capabilities"
    );
    expect(&h.call("GET", "/orders", None, key).await, 200, "");
    expect(&h.call("POST", "/orders", None, key).await, 403, "");
    let users = h.call("GET", "/auth/admin/users", None, key).await;
    expect(&users, 200, "");
    let user = json!({"login": "x@example.com", "password": USER_PASSWORD});
    let created = h.call("POST", "/auth/admin/users", Some(user), key).await;
    expect(&created, 403, CODE_FORBIDDEN);
    let roles = h.call("GET", "/auth/admin/roles", None, key).await;
    expect(&roles, 403, CODE_FORBIDDEN);
}

/// The role loses orders, and the key with it; given back, the key holds
/// it again.
async fn a_key_follows_its_users_roles(h: &Harness, key: &str) {
    let roles = h.store.list_roles().await.unwrap();
    assert_eq!(roles.len(), 1, "the harness's roles = {roles:?}");
    let rewrite = |permissions: &'static [&'static str]| {
        let (store, id) = (&h.store, roles[0].id.as_str());
        async move {
            let permissions: Vec<String> = permissions.iter().map(ToString::to_string).collect();
            store.update_role(id, "admin", &permissions).await.unwrap();
        }
    };
    let users_read = json!(["identity.users.read"]);
    rewrite(&["identity"]).await;
    expect_holds(&h.me(key).await, &["admin"], &users_read);
    expect(
        &h.call("GET", "/orders", None, key).await,
        403,
        CODE_FORBIDDEN,
    );
    // A role holding less than the key names gives it no part of it.
    rewrite(&["identity", "orders.read.archive"]).await;
    expect_holds(&h.me(key).await, &["admin"], &users_read);
    rewrite(&["billing"]).await;
    expect_holds(&h.me(key).await, &[], &json!([]));
    let users = h.call("GET", "/auth/admin/users", None, key).await;
    expect(&users, 403, CODE_FORBIDDEN);
    rewrite(&["identity", "orders"]).await;
    let named = json!(["orders.read", "identity.users.read"]);
    expect_holds(&h.me(key).await, &["admin"], &named);
}

/// A key names only what its user holds.
async fn a_key_names_what_its_user_holds(h: &Harness, admin: &str) {
    let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let not_held = |caller: String, permissions: Value, uncovered: Value| async move {
        let body = json!({"name": "wide", "permissions": permissions});
        let r = h.call("POST", "/auth/keys", Some(body), &caller).await;
        expect_detail(&r, 403, CODE_FORBIDDEN, NOT_HELD_DETAIL);
        assert_eq!(r.body["details"], json!({"permissions": uncovered}));
    };
    not_held(
        member.clone(),
        json!(["orders.read"]),
        json!(["orders.read"]),
    )
    .await;
    not_held(
        admin.to_owned(),
        json!(["orders.read", "billing.read"]),
        json!(["billing.read"]),
    )
    .await;
    let reader = h
        .store
        .create_role("reader", &["orders.read".to_owned()])
        .await
        .unwrap();
    h.store
        .grant_role(&h.member_id, &reader.id, h.clock.now())
        .await
        .unwrap();
    not_held(member.clone(), json!(["orders"]), json!(["orders"])).await;
    not_held(
        member.clone(),
        json!(["orders.read", "orders.write"]),
        json!(["orders.write"]),
    )
    .await;
    let written = h.keys("/auth/keys", &member).await;
    assert!(
        written.is_empty(),
        "a refused create wrote the member a key"
    );
    let narrow = json!({"name": "archive", "permissions": ["orders.read.archive"]});
    let narrow = key_of(&h.create_key(&member, narrow).await);
    let me = h.me(&narrow).await;
    assert_eq!(me.data()["permissions"], json!(["orders.read.archive"]));
    let orders = h.call("GET", "/orders", None, &narrow).await;
    expect(&orders, 403, CODE_FORBIDDEN);
}

/// A caller an API key authenticated neither makes, lists nor revokes
/// keys, signs out nor changes the password, while me and capabilities
/// answer it.
#[tokio::test]
async fn an_api_key_does_not_act_on_its_own_account() {
    each_key_harness(TEST_CONFIG, |h| async move {
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let first = h.create_key(&member, json!({"name": "first"})).await;
        let key = key_of(&first);
        let own = format!("/auth/keys/{}", text(&first, "id"));
        let change = |password: &str| Some(json!({"current": USER_PASSWORD, "password": password}));
        for (method, path, body) in [
            ("POST", "/auth/keys", Some(json!({"name": "minted"}))),
            ("GET", "/auth/keys", None),
            ("DELETE", own.as_str(), None),
            ("POST", "/auth/logout", None),
            ("POST", "/auth/password", change("a new password")),
            // The refusal comes before a member of the input is checked.
            ("POST", "/auth/keys", Some(json!({"name": ""}))),
            ("POST", "/auth/password", change("short")),
        ] {
            let r = h.call(method, path, body, &key).await;
            assert_eq!(
                (r.status.as_u16(), r.code(), &r.body["detail"]),
                (403, CODE_FORBIDDEN, &json!(SESSION_ONLY_DETAIL)),
                "{method} {path} by a key"
            );
        }
        // A body that is not the operation's input is refused as one first.
        let other = Some(json!({"title": "minted"}));
        let r = h.call("POST", "/auth/keys", other, &key).await;
        expect(&r, 400, "bad_request");

        nothing_was_done(&h, &member, &key, text(&first, "token")).await;
        h
    })
    .await;
}

/// Nothing was done, the key is a caller still, and the service refuses the
/// same principal whoever calls it.
async fn nothing_was_done(h: &Harness, member: &str, key: &str, token: &str) {
    expect(&h.me(key).await, 200, "");
    let caps = h.call("GET", "/auth/capabilities", None, key).await;
    expect(&caps, 200, "");
    assert_eq!(
        caps.data()["operations"],
        json!({"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": false}),
        "the key's capabilities"
    );
    let listed = h.key_names("/auth/keys", member).await;
    assert_eq!(listed, "first", "the member's keys");
    h.login("member@example.com", USER_PASSWORD).await;

    let p = h
        .service
        .authenticate_token(token, Transport::Bearer)
        .await
        .unwrap();
    let out = h.service.logout(&p).await.unwrap_err();
    assert_eq!(
        out.message, SESSION_ONLY_DETAIL,
        "logout took a key's principal"
    );
    let no_one = IdentityPrincipal {
        id: String::new(),
        login: String::new(),
        name: String::new(),
        roles: Vec::new(),
        permissions: Vec::new(),
        session_id: String::new(),
        api_key_id: String::new(),
        transport: Transport::Bearer,
    };
    let input = CreateApiKeyInput {
        name: "x".to_owned(),
        ..CreateApiKeyInput::default()
    };
    let refused = h.service.create_api_key(&no_one, &input).await.unwrap_err();
    assert_eq!(
        refused.status,
        StatusCode::UNAUTHORIZED,
        "create_api_key took a caller who is not authenticated"
    );
}

/// A caller with users.read lists any user's keys and one with users.write
/// revokes them, by a session or by a key that holds the permission; no
/// one else does.
#[tokio::test]
async fn api_key_administration() {
    each_key_harness(TEST_CONFIG, |h| async move {
        let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
        let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
        let one = h.create_key(&member, json!({"name": "one"})).await;
        h.clock.advance(Duration::from_secs(60));
        let two = h.create_key(&member, json!({"name": "two"})).await;
        let admins_own = json!({"name": "admin's", "permissions": ["identity.users"]});
        let admins_own = h.create_key(&admin, admins_own).await;

        an_administrator_lists_keys(&h, &admin, &member, &one, &two).await;
        an_administrator_revokes_keys(&h, &admin, &member, &one, &two).await;
        a_key_administers(&h, &admin, &member, &two, &admins_own).await;
        h
    })
    .await;
}

/// users.read lists any user's keys, newest first.
async fn an_administrator_lists_keys(
    h: &Harness,
    admin: &str,
    member: &str,
    one: &Value,
    two: &Value,
) {
    let keys = format!("/auth/admin/users/{}/keys", h.member_id);
    let list = h.keys(&keys, admin).await;
    let want = [
        json!({"id": two["id"], "name": "two", "createdAt": "2026-10-08T12:01:00Z", "expiresAt": "2027-10-08T12:01:00Z"}),
        json!({"id": one["id"], "name": "one", "createdAt": "2026-10-08T12:00:00Z", "expiresAt": "2027-10-08T12:00:00Z"}),
    ];
    assert_eq!(list, want, "listUserApiKeys");
    expect(
        &h.call("GET", &keys, None, member).await,
        403,
        CODE_FORBIDDEN,
    );
    expect(
        &h.call("GET", &keys, None, "").await,
        401,
        CODE_UNAUTHORIZED,
    );
    for user in [MISSING, "not-a-key"] {
        let path = format!("/auth/admin/users/{user}/keys");
        let r = h.call("GET", &path, None, admin).await;
        expect_detail(&r, 404, CODE_NOT_FOUND, "User not found");
    }
    let own = format!("/auth/admin/users/{}/keys", h.admin_id);
    assert_eq!(
        h.key_names(&own, admin).await,
        "admin's",
        "the admin's keys"
    );
}

/// users.write revokes any user's key, once.
async fn an_administrator_revokes_keys(
    h: &Harness,
    admin: &str,
    member: &str,
    one: &Value,
    two: &Value,
) {
    let keys = format!("/auth/admin/users/{}/keys", h.member_id);
    let (one_id, two_id) = (text(one, "id"), text(two, "id"));
    let first = format!("{keys}/{one_id}");
    expect(
        &h.call("DELETE", &first, None, member).await,
        403,
        CODE_FORBIDDEN,
    );
    expect(
        &h.call("DELETE", &first, None, "").await,
        401,
        CODE_UNAUTHORIZED,
    );
    expect(&h.me(&key_of(one)).await, 200, "");
    expect_true(&h.call("DELETE", &first, None, admin).await);
    expect(&h.me(&key_of(one)).await, 401, CODE_UNAUTHORIZED);
    for (name, path) in [
        ("a key revoked already", first.clone()),
        (
            "another user's key",
            format!("/auth/admin/users/{}/keys/{two_id}", h.admin_id),
        ),
        ("an unknown key", format!("{keys}/{MISSING}")),
        ("an id that is no key", format!("{keys}/not-a-key")),
        (
            "a key of an unknown user",
            format!("/auth/admin/users/{MISSING}/keys/{two_id}"),
        ),
        (
            "a key of an id that is none",
            format!("/auth/admin/users/not-a-key/keys/{two_id}"),
        ),
    ] {
        let r = h.call("DELETE", &path, None, admin).await;
        assert_eq!(
            (r.status.as_u16(), r.code(), &r.body["detail"]),
            (404, CODE_NOT_FOUND, &json!("API key not found")),
            "revoking {name}"
        );
    }
    expect(&h.me(&key_of(two)).await, 200, "");
    assert_eq!(
        h.key_names(&keys, admin).await,
        "two",
        "after the revocation"
    );
}

/// users.read lists and does not revoke, and a key that holds the
/// permission administers as a session does: only the routes of its own
/// account are a session's alone.
async fn a_key_administers(
    h: &Harness,
    admin: &str,
    member: &str,
    two: &Value,
    admins_own: &Value,
) {
    let auditor = json!({"name": "auditor", "permissions": ["identity.users.read"]});
    let auditor = h
        .call("POST", "/auth/admin/roles", Some(auditor), admin)
        .await;
    expect(&auditor, 200, "");
    let grant = format!(
        "/auth/admin/users/{}/roles/{}",
        h.member_id,
        text(auditor.data(), "id")
    );
    expect(&h.call("PUT", &grant, None, admin).await, 200, "");
    let admin_keys = format!("/auth/admin/users/{}/keys", h.admin_id);
    let admins = format!("{admin_keys}/{}", text(admins_own, "id"));
    expect(&h.call("GET", &admin_keys, None, member).await, 200, "");
    expect(
        &h.call("DELETE", &admins, None, member).await,
        403,
        CODE_FORBIDDEN,
    );

    let keys = format!("/auth/admin/users/{}/keys", h.member_id);
    let by_key = key_of(admins_own);
    expect(&h.call("GET", &keys, None, &by_key).await, 200, "");
    let second = format!("{keys}/{}", text(two, "id"));
    expect_true(&h.call("DELETE", &second, None, &by_key).await);
    expect(&h.me(&key_of(two)).await, 401, CODE_UNAUTHORIZED);
    // A key the member's role does not reach revokes nothing.
    let three = key_of(&h.create_key(member, json!({"name": "three"})).await);
    expect(
        &h.call("DELETE", &admins, None, &three).await,
        403,
        CODE_FORBIDDEN,
    );
}

/// Over a store whose descriptor names no API key table, on a database
/// without one, a bearer API key is no credential and every key operation
/// is 404, after its caller is admitted.
#[tokio::test]
async fn api_keys_without_the_table() {
    for db in databases().await {
        eprintln!("on {}", db.name);
        db.client
            .execute(r#"DROP TABLE "user_api_key""#, &[])
            .await
            .unwrap();
        let descriptor = support::descriptor(|d| {
            d.as_object_mut().unwrap().remove("apiKey");
        });
        let h = harness_over(db, &descriptor, TEST_CONFIG, with_api_keys).await;
        assert!(!h.store.has_api_keys(), "the store has API keys");
        let key = bearer(&new_api_key().unwrap());
        expect(&h.me(&key).await, 401, CODE_UNAUTHORIZED);
        no_key_operation_answers(&h).await;
        h.db.close().await;
    }
}

/// Every key operation is 404 once its caller is admitted, and the user
/// model runs as it did.
async fn no_key_operation_answers(h: &Harness) {
    let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
    let user = format!("/auth/admin/users/{}", h.member_id);
    let (user_keys, user_key) = (format!("{user}/keys"), format!("{user}/keys/{MISSING}"));
    let own_key = format!("/auth/keys/{MISSING}");
    let missing_user = format!("/auth/admin/users/{MISSING}/keys");
    for (method, path, body, caller) in [
        ("POST", "/auth/keys", Some(json!({"name": "ci"})), &member),
        ("POST", "/auth/keys", Some(json!({"name": ""})), &member),
        ("GET", "/auth/keys", None, &member),
        ("DELETE", own_key.as_str(), None, &member),
        ("GET", user_keys.as_str(), None, &admin),
        ("GET", missing_user.as_str(), None, &admin),
        ("DELETE", user_key.as_str(), None, &admin),
    ] {
        let r = h.call(method, path, body, caller).await;
        assert_eq!(
            (r.status.as_u16(), r.code(), &r.body["detail"]),
            (404, CODE_NOT_FOUND, &json!(NO_API_KEYS_DETAIL)),
            "{method} {path}"
        );
    }
    let unsigned = h.call("GET", "/auth/keys", None, "").await;
    expect(&unsigned, 401, CODE_UNAUTHORIZED);
    let listed = h.call("GET", &user_keys, None, &member).await;
    expect(&listed, 403, CODE_FORBIDDEN);
    let revoked = h.call("DELETE", &user_key, None, &member).await;
    expect(&revoked, 403, CODE_FORBIDDEN);

    // A disable revokes the sessions and reads no key table.
    let disabled = h
        .call("POST", &format!("{user}/disable"), None, &admin)
        .await;
    expect(&disabled, 200, "");
    expect(&h.me(&member).await, 401, CODE_UNAUTHORIZED);
}

/// The router's authenticator hands a key's principal on with the key's id
/// and no session id, which the identity runtime reads back, so a handler
/// behind the router's controls refuses it what a session alone does.
#[tokio::test]
async fn a_keys_principal_reaches_a_handler_behind_the_routers_controls() {
    let mut h = harness(sqlite(), TEST_CONFIG).await;
    let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let p = h
        .service
        .authenticate_token(&member[7..], Transport::Bearer)
        .await
        .unwrap();
    let input = CreateApiKeyInput {
        name: "ci".to_owned(),
        ..CreateApiKeyInput::default()
    };
    let created = h.service.create_api_key(&p, &input).await.unwrap();
    let key = bearer(&created.token);

    let authenticator = IdentityAuthenticator::new(Arc::clone(&h.service));
    let (parts, ()) = Request::get("/api/auth/keys")
        .header("authorization", &key)
        .body(())
        .unwrap()
        .into_parts();
    let principal = authenticator.authenticate(&parts).await.unwrap().unwrap();
    assert_eq!(principal.subject, h.member_id);
    assert_eq!(
        (
            &principal.claims["apiKeyId"],
            &principal.claims["sessionId"]
        ),
        (&json!(created.id), &json!("")),
        "a key's claims"
    );
    let caller = IdentityPrincipal::from_principal(&principal).unwrap();
    assert_eq!(
        (caller.api_key_id.as_str(), caller.session_id.as_str()),
        (created.id.as_str(), ""),
        "a key's principal read back"
    );
    assert!(
        !p.to_principal().claims.contains_key("apiKeyId"),
        "a session's claims name an API key"
    );
    assert_eq!(
        IdentityPrincipal::from_principal(&p.to_principal()),
        Some(p)
    );

    let authenticator: Arc<dyn Authenticator> = Arc::new(authenticator);
    let keys = RouteControls::new()
        .authorize(authenticator, &[])
        .apply(h.service.handler(OP_LIST_API_KEYS).unwrap());
    h.router = Router::new()
        .route("/api/auth/keys", keys)
        .layer(axum::middleware::from_fn(request_ids));
    let by_key = h.call("GET", "/api/auth/keys", None, &key).await;
    expect_detail(&by_key, 403, CODE_FORBIDDEN, SESSION_ONLY_DETAIL);
    assert_eq!(h.key_names("/api/auth/keys", &member).await, "ci");
}

/// The key routes of the routes `service` lists for the sets, as `METHOD
/// path`.
fn key_routes(
    service: &Arc<Service>,
    sessions: Option<&UserSessions>,
    administration: Option<&UserAdministration>,
) -> Vec<String> {
    service
        .routes::<()>(sessions, administration)
        .into_iter()
        .map(|(method, path, _)| format!("{method} {path}"))
        .filter(|route| route.contains("/keys"))
        .collect()
}

const OWN_KEY_ROUTES: [&str; 3] = [
    "POST /auth/keys",
    "GET /auth/keys",
    "DELETE /auth/keys/{id}",
];
const USER_KEY_ROUTES: [&str; 2] = [
    "GET /auth/admin/users/{id}/keys",
    "DELETE /auth/admin/users/{id}/keys/{keyId}",
];

/// A set has its API key routes only when the service turns them on for
/// it, and a key authenticates whether they are on or not.
#[tokio::test]
async fn api_key_routes_are_opt_in() {
    let h = harness(sqlite(), TEST_CONFIG).await;
    let sessions = UserSessions {
        register: true,
        ..UserSessions::default()
    };
    let administration = UserAdministration::default();
    assert!(
        !h.service.api_keys() && !h.service.api_key_administration(),
        "the API key operations are on without their options"
    );
    let routed = key_routes(&h.service, Some(&sessions), Some(&administration));
    assert!(routed.is_empty(), "without the options: {routed:?}");
    the_key_routes_are_not_mounted(&h).await;

    let build = |options: fn(Service) -> Service| {
        Arc::new(options(
            Service::new(h.store.clone(), h.config.clone()).unwrap(),
        ))
    };
    let routes =
        |service: &Arc<Service>| key_routes(service, Some(&sessions), Some(&administration));
    let only_own = build(Service::with_api_keys);
    assert_eq!(routes(&only_own), OWN_KEY_ROUTES, "with_api_keys");
    assert!(only_own.api_keys() && !only_own.api_key_administration());
    let only_users = build(Service::with_api_key_administration);
    assert_eq!(routes(&only_users), USER_KEY_ROUTES);
    assert!(!only_users.api_keys() && only_users.api_key_administration());
    let both = build(with_api_keys);
    assert_eq!(
        routes(&both),
        [&OWN_KEY_ROUTES[..], &USER_KEY_ROUTES[..]].concat()
    );
    let every = both.routes::<()>(Some(&sessions), Some(&administration));
    assert_eq!(every.len(), OPERATIONS.len(), "every set with its API keys");
    // A set the API does not declare has no route, its keys' included.
    assert_eq!(
        key_routes(&both, None, Some(&administration)),
        USER_KEY_ROUTES
    );
    assert_eq!(key_routes(&both, Some(&sessions), None), OWN_KEY_ROUTES);
    the_key_routes_follow_the_sets(&both);
}

/// Without the options the router has no key route, and authentication by
/// a key follows the table, not the routes.
async fn the_key_routes_are_not_mounted(h: &Harness) {
    let member = h.login("member@example.com", USER_PASSWORD).await;
    let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
    let user_keys = format!("/auth/admin/users/{}/keys", h.member_id);
    let user_key = format!("{user_keys}/{MISSING}");
    let own_key = format!("/auth/keys/{MISSING}");
    for (method, path, body, caller) in [
        (
            "POST",
            "/auth/keys",
            Some(json!({"name": "ci"})),
            bearer(&member),
        ),
        ("GET", "/auth/keys", None, bearer(&member)),
        ("DELETE", own_key.as_str(), None, bearer(&member)),
        ("GET", user_keys.as_str(), None, admin.clone()),
        ("DELETE", user_key.as_str(), None, admin.clone()),
    ] {
        // The router's own answer, for a route it does not have: the
        // status' reason, and no detail of a key handler's.
        let r = h.call(method, path, body, &caller).await;
        let reason = r.status.canonical_reason().unwrap_or_default();
        assert!(
            matches!(r.status.as_u16(), 404 | 405) && r.body["detail"] == reason,
            "{method} {path} answered {} {}",
            r.status,
            r.body
        );
    }
    let p = h
        .service
        .authenticate_token(&member, Transport::Bearer)
        .await
        .unwrap();
    let input = CreateApiKeyInput {
        name: "direct".to_owned(),
        ..CreateApiKeyInput::default()
    };
    let created = h.service.create_api_key(&p, &input).await.unwrap();
    expect(&h.me(&bearer(&created.token)).await, 200, "");
}

/// The routes follow each set's path, `login: false` leaves them, and the
/// operations say which are the keys' and of which set.
fn the_key_routes_follow_the_sets(both: &Arc<Service>) {
    let account = UserSessions {
        path: "account".to_owned(),
        no_login: true,
        register: false,
    };
    let manage = UserAdministration {
        path: "/manage/".to_owned(),
    };
    assert_eq!(
        key_routes(both, Some(&account), Some(&manage)),
        [
            "POST /account/keys",
            "GET /account/keys",
            "DELETE /account/keys/{id}",
            "GET /manage/users/{id}/keys",
            "DELETE /manage/users/{id}/keys/{keyId}"
        ]
    );
    let own = [OP_CREATE_API_KEY, OP_LIST_API_KEYS, OP_REVOKE_API_KEY];
    let users = [OP_LIST_USER_API_KEYS, OP_REVOKE_USER_API_KEY];
    for op in OPERATIONS {
        let is_key = own.contains(&op.name) || users.contains(&op.name);
        assert_eq!(op.api_keys, is_key, "operation {}", op.name);
        if is_key {
            assert_eq!(
                op.administration,
                users.contains(&op.name),
                "operation {} is in the wrong set",
                op.name
            );
        }
    }
}

/// A service has a handler for each API key operation whether its options
/// are on or not, since a generated server mounts the operations its schema
/// declares one by one and never reads `routes`. The handlers read the
/// key's id as `{id}` under keys, and the user's as `{id}` and the key's as
/// `{keyId}` under users.
#[tokio::test]
async fn api_key_handlers_without_the_options() {
    let mut h = harness(sqlite(), TEST_CONFIG).await;
    let mut router = Router::new();
    for (path, op) in [
        ("/api/account/keys", OP_CREATE_API_KEY),
        ("/api/account/keys", OP_LIST_API_KEYS),
        ("/api/account/keys/{id}", OP_REVOKE_API_KEY),
        ("/api/admin/users/{id}/keys", OP_LIST_USER_API_KEYS),
        ("/api/admin/users/{id}/keys/{keyId}", OP_REVOKE_USER_API_KEY),
        ("/auth/login", OP_LOGIN),
    ] {
        let handler = h.service.handler(op).unwrap_or_else(|| {
            panic!("no handler for {op} on a service built without the API key options")
        });
        router = router.route(path, handler);
    }
    h.router = router.layer(axum::middleware::from_fn(request_ids));
    let member = bearer(&h.login("member@example.com", USER_PASSWORD).await);
    let admin = bearer(&h.login("admin@example.com", ADMIN_PASSWORD).await);
    let mut made = Vec::new();
    for name in ["one", "two"] {
        let body = Some(json!({"name": name}));
        let created = h.call("POST", "/api/account/keys", body, &member).await;
        expect(&created, 200, "");
        made.push(text(created.data(), "id").to_owned());
    }
    let own = h.keys("/api/account/keys", &member).await;
    assert_eq!(own.len(), 2, "listApiKeys answered {own:?}");
    let first = format!("/api/account/keys/{}", made[0]);
    expect_true(&h.call("DELETE", &first, None, &member).await);
    let users = format!("/api/admin/users/{}/keys", h.member_id);
    assert_eq!(h.key_names(&users, &admin).await, "two", "listUserApiKeys");
    // The user's id and the key's are not read one for the other.
    let swapped = format!("/api/admin/users/{}/keys/{}", made[1], h.member_id);
    let r = h.call("DELETE", &swapped, None, &admin).await;
    expect(&r, 404, CODE_NOT_FOUND);
    let second = format!("{users}/{}", made[1]);
    expect_true(&h.call("DELETE", &second, None, &admin).await);
    let left = h.keys(&users, &admin).await;
    assert!(left.is_empty(), "after both revocations: {left:?}");
}
