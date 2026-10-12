//! The identity routes end to end, through `tower::ServiceExt::oneshot`, as
//! a generated server mounts them: the session and administration handlers,
//! an app route the router guards with `RouteControls::authorize` and the
//! `IdentityAuthenticator`, the request-id middleware, and the CORS layer
//! around it all. Each test runs on SQLite, and on Postgres with the
//! `identity-postgres` feature and `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL`.
//! The cases are the Go runtime's handler tests.

mod support;

use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use axum::body::Body;
use axum::extract::Extension;
use axum::routing::post;
use axum::Router;
use http::{HeaderMap, Method, Request, StatusCode};
use serde_json::{json, Value};
use superschematic_http_runtime::identity::{
    hash_password, Argon2Params, Config, IdentityAuthenticator, IdentityPrincipal, NewUser, Route,
    Service, SqlStore, Store, UserAdministration, UserSessions, CODE_CONFLICT, CODE_CROSS_ORIGIN,
    CODE_FORBIDDEN, CODE_INVALID_CREDENTIALS, CODE_INVALID_PERMISSION, CODE_NOT_FOUND,
    CODE_UNAUTHORIZED, HOST_COOKIE_NAME, OPERATIONS, TOKEN_LENGTH,
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
/// mounts it, with an app route the router guards.
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
    let store = Arc::new(db.store(&support::descriptor(|_| {})));
    let config = Config::parse(config.as_bytes()).unwrap();
    let clock = TestClock(Arc::new(Mutex::new(at(Duration::ZERO))));
    let now = clock.clone();
    let service = Arc::new(
        Service::new(store.clone(), config.clone())
            .unwrap()
            .with_clock(Arc::new(move || now.now()))
            .with_routes(route_table()),
    );

    let authenticator: Arc<dyn Authenticator> =
        Arc::new(IdentityAuthenticator::new(Arc::clone(&service)));
    let orders = RouteControls::new()
        .authorize(authenticator, &["orders.write"])
        .apply(post(
            |Extension(principal): Extension<Principal>| async move {
                principal.claims["name"].as_str().unwrap_or("").to_owned()
            },
        ));
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
    assert_eq!(every.len(), OPERATIONS.len());
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
