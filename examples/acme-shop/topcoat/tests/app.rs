//! Drives the app through `Router::handle`, as a browser would: the
//! reviews page renders the form from `WriteReviewInput`'s rules; a review
//! posted signed out, or one that breaks a rule, renders again with 422;
//! a signed-in shopper's review is listed, and the JSON API at `/api`
//! serves the same reviews. Over a SQLite file of shop-db's tables, the
//! reviews and orders outlive the app.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use acme_shop_orders_server::Shop;
use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_topcoat::{PRODUCT, app, product, shopper};
use topcoat::router::{Body, Router, StatusCode, header, to_bytes};

fn shop() -> Router {
    app(Arc::new(Shop::with_prices([(product(), 1999)])))
}

/// A new SQLite file of shop-db's tables at `name`: a copy of the file
/// `ACME_SHOP_SQLITE` names, which scripts/check.sh migrates with
/// superschematic-migrate from shop-db's SQLite plan, or else a file built
/// from the build's `sqlite/create.sql`, the same plan from an empty
/// database.
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
    let dir = std::env::temp_dir().join(format!("acme-shop-topcoat-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join(name);
    let _ = std::fs::remove_file(&path);
    path
}

/// The app over the SQLite shop at `path`, with the demo's shopper and
/// product, as src/main.rs opens it.
fn sqlite_shop(path: &Path) -> Router {
    let shop = SqliteShop::open(&format!("sqlite:{}", path.display())).unwrap();
    shop.add_user(&shopper(), "shopper@example.com", "Demo Shopper").unwrap();
    shop.add_product(&product(), "anvil", "Anvil", 1999).unwrap();
    app(Arc::new(shop))
}

struct Response {
    status: StatusCode,
    location: Option<String>,
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
    respond(router, request.body(body).unwrap()).await
}

/// Calls the JSON API with a JSON body as token-1, the shopper the Rust
/// server's authenticator knows.
async fn call_api(router: &Router, method: &str, uri: &str, json: &str) -> Response {
    let request = http::Request::builder()
        .method(method)
        .uri(uri)
        .header(header::AUTHORIZATION, "Bearer token-1")
        .header(header::CONTENT_TYPE, "application/json")
        .body(Body::from(json.to_owned()))
        .unwrap();
    respond(router, request).await
}

async fn respond(router: &Router, request: http::Request<Body>) -> Response {
    let response = router.handle(request).await;
    let header_text = |name| response.headers().get(name).map(|value: &http::HeaderValue| value.to_str().unwrap().to_owned());
    let location = header_text(header::LOCATION);
    // The session cookie, as the browser sends it back: name=value.
    let cookie = header_text(header::SET_COOKIE).map(|set| set.split(';').next().unwrap().to_owned());
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    Response { status, location, cookie, body: String::from_utf8(bytes.to_vec()).unwrap() }
}

#[tokio::test]
async fn the_reviews_page_renders_the_form_from_the_input_rules() {
    let page = send(&shop(), "GET", "/reviews", None, None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No reviews yet."), "{}", page.body);
    assert!(page.body.contains(r#"name="rating" type="number" required="" step="any" min="1" max="5""#), "{}", page.body);
    assert!(page.body.contains(r#"name="title" type="text" required="" minlength="1" maxlength="120""#), "{}", page.body);
}

#[tokio::test]
async fn a_review_needs_a_signed_in_shopper_and_its_rules() {
    let router = shop();
    let signed_out = send(&router, "POST", "/reviews", None, Some("rating=5&title=Great&body=Works")).await;
    assert_eq!(signed_out.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(signed_out.body.contains("Authentication required"), "{}", signed_out.body);
    assert!(signed_out.body.contains(r#"value="Great""#), "{}", signed_out.body);

    let signed_in = send(&router, "POST", "/sign-in", None, None).await;
    assert_eq!((signed_in.status, signed_in.location.as_deref()), (StatusCode::SEE_OTHER, Some("/reviews")));
    let cookie = signed_in.cookie.expect("the session cookie");

    let too_high = send(&router, "POST", "/reviews", Some(&cookie), Some("rating=9&title=Great&body=Works")).await;
    assert_eq!(too_high.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(too_high.body.contains(r#"value="9" aria-invalid="true""#), "{}", too_high.body);

    let written = send(&router, "POST", "/reviews", Some(&cookie), Some("rating=5&title=Great&body=Works")).await;
    assert_eq!((written.status, written.location.as_deref()), (StatusCode::SEE_OTHER, Some("/reviews")));
    let page = send(&router, "GET", "/reviews", Some(&cookie), None).await;
    assert!(page.body.contains("Great (5/5)"), "{}", page.body);

    // The same review through the JSON API the app mounts.
    let api = send(&router, "GET", &format!("/api/products/{PRODUCT}/reviews"), None, None).await;
    assert_eq!(api.status, StatusCode::OK, "{}", api.body);
    assert!(api.body.contains(r#""title":"Great""#), "{}", api.body);
}

#[tokio::test]
async fn orders_need_their_permission() {
    let router = shop();
    let signed_out = send(&router, "GET", "/orders", None, None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED);
    assert!(signed_out.body.contains("Authentication required"), "{}", signed_out.body);

    let cookie = send(&router, "POST", "/sign-in", None, None).await.cookie.expect("the session cookie");
    let page = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No orders yet."), "{}", page.body);
}

#[tokio::test]
async fn a_sqlite_shop_outlives_the_app() {
    let path = sqlite_file("outlives.db");
    let router = sqlite_shop(&path);
    let cookie = send(&router, "POST", "/sign-in", None, None).await.cookie.expect("the session cookie");
    let review = "rating=4&title=Sturdy&body=Survived+three+coyotes.";
    let written = send(&router, "POST", "/reviews", Some(&cookie), Some(review)).await;
    assert_eq!((written.status, written.location.as_deref()), (StatusCode::SEE_OTHER, Some("/reviews")), "{}", written.body);
    // shop-db's one_per_author index refuses the shopper's second review.
    let again = send(&router, "POST", "/reviews", Some(&cookie), Some(review)).await;
    assert_eq!(again.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(again.body.contains("you have already reviewed this product"), "{}", again.body);
    let order = format!(
        r#"{{"lines":[{{"productId":"{PRODUCT}","quantity":2}}],"shippingAddress":{{"recipient":"Ada Lovelace","line1":"1 Analytical Way","city":"London","postcode":"N1 9GU","country":"GB"}}}}"#
    );
    let placed = call_api(&router, "POST", "/api/orders", &order).await;
    assert!(placed.status.is_success(), "{}: {}", placed.status, placed.body);
    assert!(placed.body.contains(r#""totalCents":3998"#), "{}", placed.body);
    drop(router);

    // A new app over the file, as after a restart. Sessions are in memory,
    // so the shopper signs in again.
    let router = sqlite_shop(&path);
    let page = send(&router, "GET", "/reviews", None, None).await;
    assert!(page.body.contains("Sturdy (4/5)"), "{}", page.body);
    let cookie = send(&router, "POST", "/sign-in", None, None).await.cookie.expect("the session cookie");
    let orders = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert_eq!(orders.status, StatusCode::OK);
    assert!(orders.body.contains(" placed</p>"), "{}", orders.body);

    // review's search_text, the VIRTUAL column shop-db's @searchField gives
    // it on SQLite, joins its title and body.
    let search_text: String = rusqlite::Connection::open(&path)
        .unwrap()
        .query_row("SELECT search_text FROM review", [], |row| row.get(0))
        .unwrap();
    assert_eq!(search_text, "Sturdy Survived three coyotes.");
}

#[test]
fn a_file_nothing_migrated_is_refused() {
    let empty = temp_path("empty.db");
    rusqlite::Connection::open(&empty).unwrap();
    let err = SqliteShop::open(&format!("sqlite:{}", empty.display())).err().expect("a refusal");
    assert!(err.to_string().ends_with("holds no shop-db tables: apply shop-db's SQLite plan to it with superschematic-migrate"), "{err}");

    // The file must be there: opening one creates nothing.
    let missing = temp_path("missing.db");
    let err = SqliteShop::open(&missing.display().to_string()).err().expect("a refusal");
    assert!(err.to_string().starts_with("open "), "{err}");
    assert!(!missing.exists());
}
