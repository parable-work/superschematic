//! The registration modes of D81 end to end, through
//! `tower::ServiceExt::oneshot`, on each database the store tests run on,
//! as the Go runtime's `registration_test.go` holds them: approval-gated
//! registration (202 and no session, a pending login refused with its own
//! code, the administrator's pending list and `approveUser`), default
//! roles, and the first administrator, two of whom at once make one.

mod support;

use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::Router;
use http::{Method, Request};
use serde_json::{json, Value};
use superschematic_http_runtime::identity::{
    hash_password, Config, FirstUser, NewUser, Registration, Service, SqlStore, Store,
    UserAdministration, UserSessions, CODE_FORBIDDEN, CODE_INVALID_CREDENTIALS, CODE_NOT_FOUND,
    CODE_NOT_PENDING, CODE_PENDING_APPROVAL, CODE_UNAUTHORIZED,
};
use superschematic_http_runtime::request_ids;
use support::{at, databases, TestDb};
use tower::ServiceExt;

const TEST_CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://app.example.com"]}"#;
const ADMIN_PASSWORD: &str = "admin password";
const USER_PASSWORD: &str = "user password";
const NEW_PASSWORD: &str = "new user password";

fn approval() -> Registration {
    Registration {
        approval: true,
        default_roles: vec!["member".to_owned()],
        first_user: None,
    }
}

fn first_user() -> Registration {
    Registration {
        first_user: Some(FirstUser {
            role: "admin".to_owned(),
            permissions: vec!["identity".to_owned(), "orders".to_owned()],
        }),
        ..approval()
    }
}

struct Reply {
    status: u16,
    body: Value,
    cookies: usize,
}

impl Reply {
    fn data(&self) -> &Value {
        &self.body["data"]
    }
}

struct Harness {
    db: TestDb,
    store: Arc<SqlStore>,
    router: Router,
    admin_id: String,
    member_id: String,
}

/// A service with `registration` over `db`, and, when `seed`, an admin
/// holding admin (identity, orders) and a member holding none.
async fn harness(db: TestDb, registration: Registration, seed: bool) -> Harness {
    let store = Arc::new(db.store(&support::descriptor(|_| {})));
    let config = Config::parse(TEST_CONFIG.as_bytes()).unwrap();
    let service = Arc::new(
        Service::new(store.clone(), config.clone())
            .unwrap()
            .with_clock(Arc::new(|| at(Duration::ZERO)))
            .with_registration(registration)
            .unwrap(),
    );
    let router = service
        .router(
            Some(&UserSessions {
                register: true,
                ..UserSessions::default()
            }),
            Some(&UserAdministration::default()),
        )
        .layer(axum::middleware::from_fn(request_ids));
    let (mut admin_id, mut member_id) = (String::new(), String::new());
    if seed {
        let create = |login: &str, name: &str, password: &str| NewUser {
            login: login.to_owned(),
            name: name.to_owned(),
            password_hash: hash_password(password, config.argon2_params()).unwrap(),
            at: at(Duration::ZERO),
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
            .grant_role(&admin.id, &role.id, at(Duration::ZERO))
            .await
            .unwrap();
        admin_id = admin.id;
        member_id = member.id;
    }
    Harness {
        db,
        store,
        router,
        admin_id,
        member_id,
    }
}

impl Harness {
    async fn send(&self, method: &str, path: &str, body: Option<Value>, token: &str) -> Reply {
        let mut request = Request::builder()
            .method(Method::from_bytes(method.as_bytes()).unwrap())
            .uri(path)
            .header("host", "api.example.com")
            .header("sec-fetch-site", "same-origin");
        if !token.is_empty() {
            request = request.header("authorization", format!("Bearer {token}"));
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
        Reply {
            status: parts.status.as_u16(),
            body: serde_json::from_slice(&bytes).unwrap_or(Value::Null),
            cookies: parts.headers.get_all("set-cookie").iter().count(),
        }
    }

    async fn login(&self, login: &str, password: &str) -> String {
        let r = self
            .send(
                "POST",
                "/auth/login",
                Some(json!({"login": login, "password": password})),
                "",
            )
            .await;
        expect(&r, 200, "");
        r.data()["token"].as_str().unwrap().to_owned()
    }
}

#[track_caller]
fn expect(r: &Reply, status: u16, code: &str) {
    assert_eq!(r.status, status, "{}", r.body);
    if !code.is_empty() {
        assert_eq!(r.body["code"], code, "{}", r.body);
    }
}

fn role_names(roles: &Value) -> Vec<&str> {
    roles
        .as_array()
        .unwrap()
        .iter()
        .map(|role| role["name"].as_str().unwrap())
        .collect()
}

/// G7's acceptance: under approval register answers 202 with no session
/// and no cookie, login is refused as pending with a code of its own, the
/// administrator lists and approves the user, and the user then signs in
/// holding the default roles.
#[tokio::test]
async fn registration_under_approval() {
    for db in databases().await {
        eprintln!("on {}", db.name);
        let h = harness(db, approval(), true).await;
        for session in ["cookie", "bearer"] {
            let login = format!("{session}@example.com");
            let body = json!({"login": login, "name": "New", "password": NEW_PASSWORD, "session": session});
            let r = h.send("POST", "/auth/register", Some(body), "").await;
            expect(&r, 202, "");
            assert_eq!(r.cookies, 0, "a pending registration set a cookie");
            assert_eq!(
                r.data(),
                &json!({"pending": true, "user": {"id": r.data()["user"]["id"], "login": login, "name": "New"}})
            );
        }
        let pending_login = json!({"login": "bearer@example.com", "password": NEW_PASSWORD});
        let r = h.send("POST", "/auth/login", Some(pending_login), "").await;
        expect(&r, 403, CODE_PENDING_APPROVAL);
        let wrong = json!({"login": "bearer@example.com", "password": "wrong password"});
        let r = h.send("POST", "/auth/login", Some(wrong), "").await;
        expect(&r, 401, CODE_INVALID_CREDENTIALS);

        let admin = h.login("admin@example.com", ADMIN_PASSWORD).await;
        let pending = h
            .send("GET", "/auth/admin/users?pending=true", None, &admin)
            .await;
        expect(&pending, 200, "");
        let users = pending.data().as_array().unwrap();
        assert_eq!(users.len(), 2, "{}", pending.body);
        for user in users {
            assert_eq!(user["pending"], true);
            assert_eq!(user["disabled"], false);
            assert_eq!(role_names(&user["roles"]), ["member"]);
        }
        let others = h
            .send("GET", "/auth/admin/users?pending=false", None, &admin)
            .await;
        assert_eq!(others.data().as_array().unwrap().len(), 2);
        let all = h.send("GET", "/auth/admin/users", None, &admin).await;
        assert_eq!(all.data().as_array().unwrap().len(), 4);
        let bad = h
            .send("GET", "/auth/admin/users?pending=yes", None, &admin)
            .await;
        expect(&bad, 400, "bad_request");

        let id = users[0]["id"].as_str().unwrap().to_owned();
        let login = users[0]["login"].as_str().unwrap().to_owned();
        let approve = format!("/auth/admin/users/{id}/approve");
        let member = h.login("member@example.com", USER_PASSWORD).await;
        expect(
            &h.send("POST", &approve, None, &member).await,
            403,
            CODE_FORBIDDEN,
        );
        expect(
            &h.send("POST", &approve, None, "").await,
            401,
            CODE_UNAUTHORIZED,
        );
        let approved = h.send("POST", &approve, None, &admin).await;
        expect(&approved, 200, "");
        assert_eq!(approved.data()["pending"], false);
        expect(
            &h.send("POST", &approve, None, &admin).await,
            409,
            CODE_NOT_PENDING,
        );
        let admin_approve = format!("/auth/admin/users/{}/approve", h.admin_id);
        expect(
            &h.send("POST", &admin_approve, None, &admin).await,
            409,
            CODE_NOT_PENDING,
        );
        expect(
            &h.send(
                "POST",
                "/auth/admin/users/1aB2cD3eF4gH5iJ6kL7mN8/approve",
                None,
                &admin,
            )
            .await,
            404,
            CODE_NOT_FOUND,
        );

        let token = h.login(&login, NEW_PASSWORD).await;
        let me = h.send("GET", "/auth/me", None, &token).await;
        assert_eq!(role_names(&me.data()["roles"]), ["member"]);
        h.db.close().await;
    }
}

/// Approval gives a user the roles register granted them, so the caller's
/// permissions must cover theirs.
#[tokio::test]
async fn approval_needs_the_users_permissions() {
    for db in databases().await {
        let h = harness(db, approval(), true).await;
        h.store
            .create_role("member", &["orders.read".to_owned()])
            .await
            .unwrap();
        let clerk = h
            .store
            .create_role("clerk", &["identity.users".to_owned()])
            .await
            .unwrap();
        h.store
            .grant_role(&h.member_id, &clerk.id, at(Duration::ZERO))
            .await
            .unwrap();
        let body = json!({"login": "new@example.com", "password": NEW_PASSWORD});
        expect(
            &h.send("POST", "/auth/register", Some(body), "").await,
            202,
            "",
        );
        let admin = h.login("admin@example.com", ADMIN_PASSWORD).await;
        let pending = h
            .send("GET", "/auth/admin/users?pending=true", None, &admin)
            .await;
        let id = pending.data()[0]["id"].as_str().unwrap().to_owned();
        let member = h.login("member@example.com", USER_PASSWORD).await;
        let r = h
            .send(
                "POST",
                &format!("/auth/admin/users/{id}/approve"),
                None,
                &member,
            )
            .await;
        expect(&r, 403, CODE_FORBIDDEN);
        assert_eq!(r.body["details"]["permissions"], json!(["orders.read"]));
        h.db.close().await;
    }
}

/// Open registration signs the user in with the default roles, creating
/// one no role has the name of.
#[tokio::test]
async fn open_registration_grants_the_default_roles() {
    for db in databases().await {
        let registration = Registration {
            default_roles: vec!["member".to_owned(), "reader".to_owned()],
            ..Registration::default()
        };
        let h = harness(db, registration, true).await;
        h.store
            .create_role("reader", &["orders.read".to_owned()])
            .await
            .unwrap();
        let body = json!({"login": "new@example.com", "password": NEW_PASSWORD});
        let r = h.send("POST", "/auth/register", Some(body), "").await;
        expect(&r, 200, "");
        assert!(r.data().get("pending").is_none(), "{}", r.body);
        let token = r.data()["token"].as_str().unwrap().to_owned();
        let me = h.send("GET", "/auth/me", None, &token).await;
        assert_eq!(role_names(&me.data()["roles"]), ["member", "reader"]);
        assert_eq!(me.data()["permissions"], json!(["orders.read"]));
        h.db.close().await;
    }
}

/// The first registrant of a database with no grant is the administrator,
/// signed in; the next waits for them; a database with a grant has its
/// administrator already.
#[tokio::test]
async fn the_first_registrant_is_the_administrator() {
    for db in databases().await {
        let h = harness(db, first_user(), false).await;
        let ada = json!({"login": "ada@example.com", "password": "ada's password"});
        let first = h.send("POST", "/auth/register", Some(ada), "").await;
        expect(&first, 200, "");
        assert_eq!(first.data()["pending"], false);
        assert!(first.data()["expiresAt"].is_string(), "{}", first.body);
        let token = first.data()["token"].as_str().unwrap().to_owned();
        let me = h.send("GET", "/auth/me", None, &token).await;
        assert_eq!(role_names(&me.data()["roles"]), ["admin", "member"]);
        assert_eq!(me.data()["permissions"], json!(["identity", "orders"]));

        let bob = json!({"login": "bob@example.com", "password": "bob's password"});
        expect(
            &h.send("POST", "/auth/register", Some(bob.clone()), "")
                .await,
            202,
            "",
        );
        expect(
            &h.send("POST", "/auth/login", Some(bob), "").await,
            403,
            CODE_PENDING_APPROVAL,
        );
        let pending = h
            .send("GET", "/auth/admin/users?pending=true", None, &token)
            .await;
        let id = pending.data()[0]["id"].as_str().unwrap().to_owned();
        let approve = format!("/auth/admin/users/{id}/approve");
        expect(&h.send("POST", &approve, None, &token).await, 200, "");
        h.login("bob@example.com", "bob's password").await;
        h.db.close().await;
    }
    for db in databases().await {
        let h = harness(db, first_user(), true).await;
        let eve = json!({"login": "eve@example.com", "password": "eve's password"});
        expect(
            &h.send("POST", "/auth/register", Some(eve), "").await,
            202,
            "",
        );
        h.db.close().await;
    }
}

/// Registrations that race on an empty database make one administrator.
#[tokio::test]
async fn registrations_at_once_make_one_administrator() {
    for db in databases().await {
        let h = Arc::new(harness(db, first_user(), false).await);
        let mut tasks = Vec::new();
        for i in 0..8 {
            let h = Arc::clone(&h);
            tasks.push(tokio::spawn(async move {
                let body =
                    json!({"login": format!("racer{i}@example.com"), "password": "racer's password"});
                h.send("POST", "/auth/register", Some(body), "")
                    .await
                    .status
            }));
        }
        let mut statuses = Vec::new();
        for task in tasks {
            statuses.push(task.await.unwrap());
        }
        assert_eq!(
            statuses.iter().filter(|s| **s == 200).count(),
            1,
            "{statuses:?}"
        );
        assert_eq!(
            statuses.iter().filter(|s| **s == 202).count(),
            7,
            "{statuses:?}"
        );
        let users = h.store.list_users().await.unwrap();
        let admins = users
            .iter()
            .filter(|u| u.roles.iter().any(|r| r.name == "admin"))
            .count();
        assert_eq!(admins, 1);
        match Arc::try_unwrap(h) {
            Ok(h) => h.db.close().await,
            Err(_) => panic!("a task still holds the harness"),
        }
    }
}
