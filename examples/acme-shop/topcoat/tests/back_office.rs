//! The back office: what staff do in the console the crate generates,
//! through `Router::handle`, as a browser would, over a SQLite file of
//! shop-db's tables. Ada, staff with the back office's roles, lists the
//! shop's people and the roles at `/console/users` and `/console/roles`,
//! which a shopper is refused.
//!
//! shop-db's commerce types are tables whose behaviors the console renders
//! no panel of yet (D103): the engine console, whose Inventory, Calendar,
//! Holds, Payments and Approvals panels this test once drove, went with
//! engine types. Staff receive stock through shop-stock's Backoffice and
//! sign refunds off through shop-api's RefundActions routes.
//!
//! Every page passes the extension's Enter rule
//! (extensions/topcoat/testdata/enter.rs).

use std::path::PathBuf;
use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{add_shopper, add_staff, users_at};
use acme_shop_topcoat::{BACK_OFFICE_ROLES, add_back_office, app, stock_sqlite};
use topcoat::router::{Body, Router, StatusCode, header, to_bytes};

#[path = "../../../../extensions/topcoat/testdata/enter.rs"]
mod enter;

/// The app's identity config at a low password cost, so a test hashes
/// fast.
const CONFIG: &str = r#"{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;

/// A user's login, name and password.
type Person = (&'static str, &'static str, &'static str);

/// A shopper.
const GRACE: Person = ("grace@example.com", "Grace Hopper", "grace's password");

/// Staff with the back office's roles.
const ADA: Person = ("ada@example.com", "Ada Lovelace", "ada's password");

/// The back office: the app over a new SQLite file of shop-db's tables,
/// holding Grace, a shopper, and Ada, staff with the back office's roles.
async fn back_office(name: &str) -> Router {
    let path = sqlite_file(&format!("{name}.db"));
    let url = format!("sqlite:{}", path.display());
    let shop = SqliteShop::open(&url).unwrap();
    stock_sqlite(&shop).unwrap();
    let users = users_at(&url, CONFIG).unwrap();
    let (login, full_name, password) = GRACE;
    add_shopper(&users, login, full_name, password).await.unwrap();
    let (login, full_name, password) = ADA;
    add_staff(&users, login, full_name, password).await.unwrap();
    add_back_office(&users, login).await.unwrap();
    app(Arc::new(shop), Arc::clone(&users), None)
}

/// A new SQLite file of shop-db's tables, as tests/app.rs makes one: a
/// copy of the file `ACME_SHOP_SQLITE` names, which scripts/check.sh
/// migrates, or else one built from the build's `sqlite/create.sql`.
fn sqlite_file(name: &str) -> PathBuf {
    let path = temp_path(name);
    match std::env::var("ACME_SHOP_SQLITE") {
        Ok(migrated) => {
            std::fs::copy(migrated, &path).unwrap();
        }
        Err(_) => rusqlite::Connection::open(&path)
            .unwrap()
            .execute_batch(include_str!("../../schemas/dist-rust/sql/shop-db/sqlite/create.sql"))
            .unwrap(),
    }
    path
}

/// A path of this test run's own, with nothing at it.
fn temp_path(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("acme-shop-back-office-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join(name);
    let _ = std::fs::remove_file(&path);
    path
}

struct Response {
    status: StatusCode,
    cookie: Option<String>,
    body: String,
}

async fn send(router: &Router, method: &str, uri: &str, cookie: Option<&str>, form: Option<&str>) -> Response {
    let mut request = http::Request::builder().method(method).uri(uri);
    if let Some(cookie) = cookie {
        request = request.header(header::COOKIE, cookie);
    }
    let body = match form {
        Some(form) => {
            request = request.header(header::CONTENT_TYPE, "application/x-www-form-urlencoded");
            Body::from(form.to_owned())
        }
        None => Body::empty(),
    };
    let response = router.handle(request.body(body).unwrap()).await;
    let headers = response.headers().clone();
    let cookie = headers
        .get(header::SET_COOKIE)
        .map(|value: &http::HeaderValue| value.to_str().unwrap().split(';').next().unwrap().to_owned());
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &body);
    Response { status, cookie, body }
}

/// A form's fields, percent-encoded.
fn form(pairs: &[(&str, &str)]) -> String {
    form_urlencoded::Serializer::new(String::new()).extend_pairs(pairs).finish()
}

/// Signs `person` in through the app's own sign-in page: the session
/// cookie, as the browser sends it back.
async fn sign_in(router: &Router, (login, _, password): Person) -> String {
    let signed_in = send(router, "POST", "/sign-in", None, Some(&form(&[("login", login), ("password", password)]))).await;
    assert_eq!(signed_in.status, StatusCode::SEE_OTHER, "{}", signed_in.body);
    signed_in.cookie.expect("the session cookie")
}

#[tokio::test]
async fn staff_administer_the_users_and_roles() {
    let router = back_office("people").await;
    let ada = sign_in(&router, ADA).await;
    let index = send(&router, "GET", "/console", Some(&ada), None).await;
    for link in [r#"<li><a href="/console/users">Users</a></li>"#, r#"<li><a href="/console/roles">Roles</a></li>"#] {
        assert!(index.body.contains(link), "{link}: {}", index.body);
    }
    let people = send(&router, "GET", "/console/users", Some(&ada), None).await;
    assert_eq!(people.status, StatusCode::OK, "{}", people.body);
    assert!(people.body.contains(r#"<td data-field="roles">shopper</td>"#), "{}", people.body);
    assert!(people.body.contains(">ada@example.com</a>"), "{}", people.body);
    let roles = send(&router, "GET", "/console/roles", Some(&ada), None).await;
    assert_eq!(roles.status, StatusCode::OK, "{}", roles.body);
    for (role, _) in BACK_OFFICE_ROLES.iter().chain(&[("staff", &[][..]), ("shopper", &[][..])]) {
        assert!(roles.body.contains(&format!(">{role}</a>")), "{role}: {}", roles.body);
    }

    // A shopper administers no one.
    let grace = sign_in(&router, GRACE).await;
    for page in ["/console/users", "/console/roles"] {
        let refused = send(&router, "GET", page, Some(&grace), None).await;
        assert_eq!(refused.status, StatusCode::FORBIDDEN, "{page}: {}", refused.body);
    }
}
