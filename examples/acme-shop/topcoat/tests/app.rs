//! Drives the app through `Router::handle`, as a browser would: the
//! reviews page renders the form from `WriteReviewInput`'s rules; a review
//! posted signed out, or one that breaks a rule, renders again with 422;
//! a shopper signs in with their password, their review is listed, and the
//! JSON API at `/api` serves the same reviews and admits the same session.

use std::sync::Arc;

use acme_shop_orders_server::{Shop, add_shopper, users_in_memory};
use acme_shop_topcoat::{PRODUCT, app, product};
use topcoat::router::{Body, Router, StatusCode, header, to_bytes};

/// The app's identity config at a low password cost, so a test hashes
/// fast.
const CONFIG: &str = r#"{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;

/// The sign-in form of the shopper `shop` adds, who holds `orders`
/// through the shopper role.
const SHOPPER: &str = "login=grace%40example.com&password=grace%27s+password";

async fn shop() -> Router {
    let users = users_in_memory(CONFIG).unwrap();
    add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    app(Arc::new(Shop::with_prices([(product(), 1999)])), users)
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
    let response = router.handle(request.body(body).unwrap()).await;
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
    let page = send(&shop().await, "GET", "/reviews", None, None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No reviews yet."), "{}", page.body);
    assert!(page.body.contains(r#"name="rating" type="number" required="" step="any" min="1" max="5""#), "{}", page.body);
    assert!(page.body.contains(r#"name="title" type="text" required="" minlength="1" maxlength="120""#), "{}", page.body);
}

#[tokio::test]
async fn a_review_needs_a_signed_in_shopper_and_its_rules() {
    let router = shop().await;
    let signed_out = send(&router, "POST", "/reviews", None, Some("rating=5&title=Great&body=Works")).await;
    assert_eq!(signed_out.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(signed_out.body.contains("Authentication required"), "{}", signed_out.body);
    assert!(signed_out.body.contains(r#"value="Great""#), "{}", signed_out.body);

    let signed_in = send(&router, "POST", "/sign-in", None, Some(SHOPPER)).await;
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
async fn a_wrong_password_signs_no_one_in() {
    let router = shop().await;
    let form = "login=grace%40example.com&password=not+her+password";
    let refused = send(&router, "POST", "/sign-in", None, Some(form)).await;
    assert_eq!(refused.status, StatusCode::UNAUTHORIZED);
    assert_eq!(refused.cookie, None);
    assert!(refused.body.contains(r#"value="grace@example.com""#), "{}", refused.body);
}

#[tokio::test]
async fn orders_need_their_permission() {
    let router = shop().await;
    let signed_out = send(&router, "GET", "/orders", None, None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED);
    assert!(signed_out.body.contains("Authentication required"), "{}", signed_out.body);

    let cookie = send(&router, "POST", "/sign-in", None, Some(SHOPPER)).await.cookie.expect("the session cookie");
    let page = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No orders yet."), "{}", page.body);

    // The JSON API admits the session the page signed in.
    let api = send(&router, "GET", "/api/orders", Some(&cookie), None).await;
    assert_eq!(api.status, StatusCode::OK, "{}", api.body);
    assert!(api.body.contains(r#""data":[]"#), "{}", api.body);
}
