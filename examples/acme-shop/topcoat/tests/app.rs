//! Drives the app through `Router::handle`, as a browser would: the
//! reviews page renders the form from `WriteReviewInput`'s rules; a review
//! posted signed out, or one that breaks a rule, renders again with 422;
//! a shopper signs in with their password, their review is listed, and the
//! JSON API at `/api` serves the same reviews and admits the same session.
//! An order is placed through the form with its lines as rows, filtered by
//! the GET form on `/orders` and cancelled from its page, each refusal
//! rendered at its control; a post of far more lines than an order holds
//! renders a form of 51. Over a SQLite file of shop-db's tables, the
//! reviews, orders and sessions outlive the app.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{add_shopper, users_at, users_in_memory};
use acme_shop_topcoat::{PRODUCT, app, product, shop_in_memory, stock_sqlite};
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
    app(Arc::new(shop_in_memory()), users)
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

/// The app over the SQLite shop at `path`, its users the file's, with the
/// demo's shopper and the catalog's products, as src/main.rs opens it: the
/// shopper is added unless an earlier app over the file added them.
async fn sqlite_shop(path: &Path) -> Router {
    let url = format!("sqlite:{}", path.display());
    let shop = SqliteShop::open(&url).unwrap();
    let users = users_at(&url, CONFIG).unwrap();
    if users.store().find_login("grace@example.com").await.is_err() {
        add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    }
    stock_sqlite(&shop).unwrap();
    app(Arc::new(shop), users)
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

/// Calls the JSON API with a JSON body, as the shopper whose session
/// `cookie` holds.
async fn call_api(router: &Router, method: &str, uri: &str, cookie: &str, json: &str) -> Response {
    let request = http::Request::builder()
        .method(method)
        .uri(uri)
        .header(header::COOKIE, cookie)
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

/// Signs the demo shopper in: the session cookie, as the browser sends it
/// back.
async fn sign_in(router: &Router) -> String {
    send(router, "POST", "/sign-in", None, Some(SHOPPER)).await.cookie.expect("the session cookie")
}

/// The shipping address the place-order form posts.
const ADDRESS: &str = "shippingAddress.recipient=Ada+Lovelace&shippingAddress.line1=1+Analytical+Way\
    &shippingAddress.city=London&shippingAddress.postcode=N1+9GU&shippingAddress.country=GB";

/// The place-order form as a browser posts it: one line, `quantity` of
/// the catalog's anvil, chosen in the line's select by its id as the API
/// sends it, and the address.
fn order_form(quantity: &str) -> String {
    format!("lines[0].productId={}&lines[0].quantity={quantity}&{ADDRESS}", product())
}

/// Places an order of two anvils through the form: the order's page.
async fn place_order(router: &Router, cookie: &str) -> String {
    let placed = send(router, "POST", "/orders/new", Some(cookie), Some(&order_form("2"))).await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.body);
    placed.location.expect("the order's page")
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
    assert!(page.body.contains(r#"<div class="ss-detail" data-type="ReviewView" role="group" aria-label="Great">"#), "{}", page.body);
    assert!(page.body.contains(r#"<div data-field="rating"><dt>Rating</dt><dd>5</dd></div>"#), "{}", page.body);

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
    assert!(signed_out.body.contains(r#"<p class="refusal">Authentication required</p>"#), "{}", signed_out.body);
    // The sign-in button, which opens the sign-in page with the page to
    // come back to.
    let button = r#"<form method="get" action="/sign-in" class="sign-in"><input type="hidden" name="next" value="/orders">"#;
    assert!(signed_out.body.contains(button), "{}", signed_out.body);
    let sign_in_page = send(&router, "GET", "/sign-in?next=%2Forders", None, None).await;
    assert_eq!(sign_in_page.status, StatusCode::OK);
    let form = r#"<form method="post" action="/sign-in"><input type="hidden" name="next" value="/orders">"#;
    assert!(sign_in_page.body.contains(form), "{}", sign_in_page.body);
    // A refused sign-in keeps it.
    let refused = send(&router, "POST", "/sign-in", None, Some("login=grace%40example.com&password=not+her+password&next=%2Forders")).await;
    assert_eq!(refused.status, StatusCode::UNAUTHORIZED);
    assert!(refused.body.contains(form), "{}", refused.body);

    let signed_in = send(&router, "POST", "/sign-in", None, Some(&format!("{SHOPPER}&next=%2Forders"))).await;
    assert_eq!((signed_in.status, signed_in.location.as_deref()), (StatusCode::SEE_OTHER, Some("/orders")));
    let cookie = signed_in.cookie.expect("the session cookie");
    let page = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No orders yet."), "{}", page.body);
    assert!(!page.body.contains(r#"action="/sign-in""#), "{}", page.body);

    // The JSON API admits the session the page signed in.
    let api = send(&router, "GET", "/api/orders", Some(&cookie), None).await;
    assert_eq!(api.status, StatusCode::OK, "{}", api.body);
    assert!(api.body.contains(r#""data":[]"#), "{}", api.body);

    // `next` names a page of the app, never another host.
    for next in ["%2F%2Fevil.example", "https%3A%2F%2Fevil.example", "%2F%5Cevil.example"] {
        let signed_in = send(&router, "POST", "/sign-in", None, Some(&format!("{SHOPPER}&next={next}"))).await;
        assert_eq!(signed_in.location.as_deref(), Some("/reviews"), "{next}");
    }

    // An order's page answers as the operation's route does: 401 signed
    // out, 404 for an order no one placed, and 404 for an id that is none.
    let order = place_order(&router, &cookie).await;
    let signed_out = send(&router, "GET", &order, None, None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED);
    assert!(signed_out.body.contains(&format!(r#"<input type="hidden" name="next" value="{order}">"#)), "{}", signed_out.body);
    let missing = send(&router, "GET", "/orders/6f1c2a3e-4b5d-4e6f-8a9b-0c1d2e3f4a5c", Some(&cookie), None).await;
    assert_eq!(missing.status, StatusCode::NOT_FOUND);
    assert!(missing.body.contains(r#"<p class="refusal">order not found</p>"#), "{}", missing.body);
    let malformed = send(&router, "GET", "/orders/not-an-id", Some(&cookie), None).await;
    assert_eq!(malformed.status, StatusCode::NOT_FOUND);

    // Placing an order signed out: the form, with the sign-in button
    // first, refuses with the operation's message.
    let form = send(&router, "GET", "/orders/new", None, None).await;
    assert!(form.body.contains(r#"<input type="hidden" name="next" value="/orders/new">"#), "{}", form.body);
    let refused = send(&router, "POST", "/orders/new", None, Some(&order_form("2"))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(refused.body.contains(r#"<p class="form-error">Authentication required</p>"#), "{}", refused.body);
}

#[tokio::test]
async fn an_order_is_placed_through_its_form_with_a_row_per_line() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let anvil = product().to_string();

    // A new form: one line, its product a select of the catalog's.
    let form = send(&router, "GET", "/orders/new", Some(&cookie), None).await;
    assert_eq!(form.status, StatusCode::OK);
    for want in [
        r#"<fieldset class="ss-row"><legend>Line 1</legend><input type="hidden" name="lines[0]" value="">"#,
        r#"<select id="place-order-input-lines-0-product-id" name="lines[0].productId" required=""><option value="" selected=""></option>"#,
        &format!(r#"<option value="{anvil}">Anvil</option></select>"#),
        r#"name="lines[0].quantity" type="number" required="" step="any" min="1" max="99">"#,
        r#"<button type="submit" class="ss-add" name="_action" value="add:lines" formnovalidate="">Add a line</button>"#,
        r#"name="shippingAddress.country" type="text" required="" pattern="^[A-Z]{2}$">"#,
    ] {
        assert!(form.body.contains(want), "{want}\n{}", form.body);
    }
    assert!(!form.body.contains("Line 2"), "{}", form.body);

    // A row button adds a line and renders the form again, as sent, without
    // placing an order.
    let added = send(&router, "POST", "/orders/new", Some(&cookie), Some(&format!("_action=add%3Alines&{}", order_form("2")))).await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.body);
    for want in [
        &format!(r#"<option value="{anvil}" selected="">Anvil</option>"#),
        r#"name="lines[0].quantity" type="number" required="" step="any" min="1" max="99" value="2">"#,
        r#"<legend>Line 2</legend>"#,
        r#"<select id="place-order-input-lines-1-product-id" name="lines[1].productId" required="">"#,
        r#"value="remove:lines[1]" formnovalidate="" aria-label="Remove Line 2">Remove</button>"#,
        r#"value="Ada Lovelace""#,
    ] {
        assert!(added.body.contains(want), "{want}\n{}", added.body);
    }
    let orders = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert!(orders.body.contains("No orders yet."), "{}", orders.body);

    // A quantity out of range: 422, the message at the line's quantity, and
    // the form as sent.
    let refused = send(&router, "POST", "/orders/new", Some(&cookie), Some(&order_form("0"))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY);
    let quantity = r#"name="lines[0].quantity" type="number" required="" step="any" min="1" max="99" value="0" aria-invalid="true"><p class="field-error">must be at least 1</p>"#;
    assert!(refused.body.contains(quantity), "{}", refused.body);
    assert!(refused.body.contains(&format!(r#"<option value="{anvil}" selected="">Anvil</option>"#)), "{}", refused.body);
    assert!(refused.body.contains(r#"name="shippingAddress.postcode" type="text" required="" maxlength="12" value="N1 9GU">"#), "{}", refused.body);

    // A valid form places the order and goes to its page, which the orders
    // page links to.
    let order = place_order(&router, &cookie).await;
    assert!(order.starts_with("/orders/"), "{order}");
    let page = send(&router, "GET", &order, Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains(r#"<div data-field="status"><dt>Status</dt><dd><data value="placed">Placed</data></dd></div>"#), "{}", page.body);
    assert!(page.body.contains(r#"<div data-field="totalCents"><dt>Total cents</dt><dd>3998</dd></div>"#), "{}", page.body);
    assert!(page.body.contains(&format!(r#"<td data-field="productId">{anvil}</td>"#)), "{}", page.body);
    let orders = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert!(orders.body.contains(&format!(r#"<li><a href="{order}">"#)), "{}", orders.body);
}

#[tokio::test]
async fn a_post_of_far_more_lines_than_an_order_holds_renders_a_bounded_form() {
    // Anyone may post the form, signed in or not. 4,000 lines, past the 50
    // an order holds, render the form again with 51 and the list's message,
    // rather than every line's errors.
    let router = shop().await;
    let lines = |count: usize| (0..count).map(|index| format!("lines[{index}]=")).collect::<Vec<_>>().join("&");
    let refused = send(&router, "POST", "/orders/new", None, Some(&lines(4_000))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY);
    let head = refused.body.get(..4000).unwrap_or(&refused.body);
    assert!(refused.body.contains(r#"<legend>Lines</legend><p class="field-error">must contain at most 50 items</p>"#), "{head}");
    assert_eq!(refused.body.matches(r#"<fieldset class="ss-row">"#).count(), 51);
    assert!(refused.body.len() < 256 * 1024, "a response of {} bytes", refused.body.len());

    // 50,000, more pairs than a form reads at all: the form's own message,
    // and the same 51 lines.
    let refused = send(&router, "POST", "/orders/new", None, Some(&lines(50_000))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY);
    let head = refused.body.get(..4000).unwrap_or(&refused.body);
    assert!(refused.body.contains(r#"<p class="form-error">The form sent more than 5000 fields, more than it reads</p>"#), "{head}");
    assert_eq!(refused.body.matches(r#"<fieldset class="ss-row">"#).count(), 51);
}

#[tokio::test]
async fn an_order_is_cancelled_from_its_page() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let order = place_order(&router, &cookie).await;
    let other = place_order(&router, &cookie).await;
    let id = order.trim_start_matches("/orders/");
    let other_id = other.trim_start_matches("/orders/");

    // A placed order's page has the form that cancels it.
    let page = send(&router, "GET", &order, Some(&cookie), None).await;
    let form = format!(r#"<form method="post" action="{order}/cancel"><input type="hidden" name="id" value="{id}">"#);
    assert!(page.body.contains(&form), "{}", page.body);
    assert!(page.body.contains(r#"<input id="order-cancel-order-reason" name="reason" type="text" maxlength="500">"#), "{}", page.body);
    assert!(page.body.contains(r#"<button type="submit">Cancel order</button>"#), "{}", page.body);

    // A reason past its 500 characters: 422, the route's message at the
    // reason, and the order still placed.
    let cancel = format!("{order}/cancel");
    let long = send(&router, "POST", &cancel, Some(&cookie), Some(&format!("reason={}", "x".repeat(501)))).await;
    assert_eq!(long.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(long.body.contains(r#"aria-invalid="true"><p class="field-error">must be at most 500 characters</p>"#), "{}", long.body);
    assert!(long.body.contains(r#"<data value="placed">Placed</data>"#), "{}", long.body);

    // The route's id is the order cancelled, whatever the hidden input says.
    let cancelled = send(&router, "POST", &cancel, Some(&cookie), Some(&format!("id={other_id}&reason=Changed+my+mind"))).await;
    assert_eq!((cancelled.status, cancelled.location.as_deref()), (StatusCode::SEE_OTHER, Some(order.as_str())), "{}", cancelled.body);
    let page = send(&router, "GET", &order, Some(&cookie), None).await;
    assert!(page.body.contains(r#"<data value="cancelled">Cancelled</data>"#), "{}", page.body);
    assert!(page.body.contains(r#"<div data-field="cancelReason"><dt>Cancel reason</dt><dd>Changed my mind</dd></div>"#), "{}", page.body);
    assert!(!page.body.contains("Cancel order"), "{}", page.body);
    let untouched = send(&router, "GET", &other, Some(&cookie), None).await;
    assert!(untouched.body.contains(r#"<data value="placed">Placed</data>"#), "{}", untouched.body);

    // Cancelling it again: the operation's 409, as the form's own message.
    let again = send(&router, "POST", &cancel, Some(&cookie), Some("reason=Twice")).await;
    assert_eq!(again.status, StatusCode::UNPROCESSABLE_ENTITY);
    let message = format!(r#"<form method="post" action="{cancel}"><p class="form-error">order {id} is cancelled</p>"#);
    assert!(again.body.contains(&message), "{}", again.body);
    assert!(again.body.contains(r#"value="Twice""#), "{}", again.body);
}

#[tokio::test]
async fn orders_are_filtered_by_the_get_form() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let placed = place_order(&router, &cookie).await;
    let cancelled = place_order(&router, &cookie).await;
    let done = send(&router, "POST", &format!("{cancelled}/cancel"), Some(&cookie), Some("reason=")).await;
    assert_eq!(done.status, StatusCode::SEE_OTHER, "{}", done.body);

    // The filter reads the query, and renders the box checked.
    let page = send(&router, "GET", "/orders?statuses=cancelled", Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains(r#"<input type="checkbox" name="statuses" value="cancelled" checked="">"#), "{}", page.body);
    assert!(page.body.contains(r#"<input type="checkbox" name="statuses" value="placed">"#), "{}", page.body);
    assert!(page.body.contains(&format!(r#"<a href="{cancelled}">"#)), "{}", page.body);
    assert!(!page.body.contains(&placed), "{}", page.body);
    let none = send(&router, "GET", "/orders?statuses=shipped&limit=", Some(&cookie), None).await;
    assert!(none.body.contains("No orders match the filter."), "{}", none.body);

    // A limit out of its range: 422, the route's message at the control.
    let refused = send(&router, "GET", "/orders?limit=500", Some(&cookie), None).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY);
    let limit = r#"name="limit" type="number" step="any" min="1" max="100" value="500" aria-invalid="true"><p class="field-error">must be at most 100</p>"#;
    assert!(refused.body.contains(limit), "{}", refused.body);
    assert!(!refused.body.contains("<table"), "{}", refused.body);
}

#[tokio::test]
async fn a_sqlite_shop_outlives_the_app() {
    let path = sqlite_file("outlives.db");
    let router = sqlite_shop(&path).await;
    let cookie = sign_in(&router).await;
    let review = "rating=4&title=Sturdy&body=Survived+three+coyotes.";
    let written = send(&router, "POST", "/reviews", Some(&cookie), Some(review)).await;
    assert_eq!((written.status, written.location.as_deref()), (StatusCode::SEE_OTHER, Some("/reviews")), "{}", written.body);
    // shop-db's one_per_author index refuses the shopper's second review.
    let again = send(&router, "POST", "/reviews", Some(&cookie), Some(review)).await;
    assert_eq!(again.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert!(again.body.contains("you have already reviewed this product"), "{}", again.body);
    // An order through the JSON API, and one through the form, priced from
    // the product's row, which its page then cancels.
    let json = format!(
        r#"{{"lines":[{{"productId":"{PRODUCT}","quantity":2}}],"shippingAddress":{{"recipient":"Ada Lovelace","line1":"1 Analytical Way","city":"London","postcode":"N1 9GU","country":"GB"}}}}"#
    );
    let placed = call_api(&router, "POST", "/api/orders", &cookie, &json).await;
    assert!(placed.status.is_success(), "{}: {}", placed.status, placed.body);
    assert!(placed.body.contains(r#""totalCents":3998"#), "{}", placed.body);
    let order = place_order(&router, &cookie).await;
    let cancel = send(&router, "POST", &format!("{order}/cancel"), Some(&cookie), Some("reason=Found+a+bigger+anvil")).await;
    assert_eq!(cancel.status, StatusCode::SEE_OTHER, "{}", cancel.body);
    drop(router);

    // A new app over the file, as after a restart. The session is in the
    // file too, so the shopper's cookie still signs them in.
    let router = sqlite_shop(&path).await;
    let page = send(&router, "GET", "/reviews", None, None).await;
    assert!(page.body.contains(r#"<div data-field="title"><dt>Title</dt><dd>Sturdy</dd></div>"#), "{}", page.body);
    let orders = send(&router, "GET", "/orders", Some(&cookie), None).await;
    assert_eq!(orders.status, StatusCode::OK);
    assert!(orders.body.contains(r#"<table class="ss-table" data-type="OrderView"><caption>Orders</caption>"#), "{}", orders.body);
    assert!(orders.body.contains(r#"<td data-field="status"><data value="placed">Placed</data></td><td data-field="totalCents">3998</td>"#), "{}", orders.body);
    let page = send(&router, "GET", &order, Some(&cookie), None).await;
    assert!(page.body.contains(r#"<data value="cancelled">Cancelled</data>"#), "{}", page.body);
    assert!(page.body.contains(r#"<dd>Found a bigger anvil</dd>"#), "{}", page.body);

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

#[test]
fn a_url_of_another_database_is_refused_without_its_password() {
    for url in [
        "postgres://shop:hunter2@127.0.0.1:5432/shop_db",
        "postgresql://shop:hunter2@db.internal/shop_db?sslmode=require",
        "d1://hunter2@shop-db",
    ] {
        let err = SqliteShop::open(url).err().expect("a refusal");
        let (message, debug) = (err.to_string(), format!("{err:?}"));
        assert!(message.contains("is not a SQLite database: give a sqlite: URL, a file: URI or a path"), "{message}");
        assert!(!message.contains("hunter2") && !debug.contains("hunter2"), "the refusal shows the password: {message} / {debug}");
    }
    let err = SqliteShop::open("postgres://shop:hunter2@127.0.0.1/shop_db").err().expect("a refusal");
    assert!(err.to_string().starts_with("a postgres:// URL "), "{err}");

    // A path is still a path, and a file: URI a URI.
    let migrated = sqlite_file("by-path.db");
    SqliteShop::open(&migrated.display().to_string()).unwrap();
    SqliteShop::open(&format!("file:{}?mode=rw", migrated.display())).unwrap();
}
