//! Drives the app through `Router::handle`, as a browser would: the
//! reviews page renders the form from `WriteReviewInput`'s rules; a review
//! posted signed out, or one that breaks a rule, renders again with 422;
//! a shopper signs in with their password, their review is listed, and the
//! JSON API at `/api` serves the same reviews and admits the same session.
//! An order is placed through the form with its lines as rows, each line's
//! product a select of the products `listProducts` lists, filtered by the
//! GET form on `/orders`, paged through its links to the next page and the
//! first, and cancelled from its page, each refusal rendered at its control
//! with the route's status, and a forged cursor or a limit out of range as
//! the form's own message with the route's 400; an order's page names its
//! lines' products through the catalog's lookup; a post of far more lines
//! than an order holds renders a form of 51. The console the crate
//! generates at `/console` lists what the shopper may list, the catalog to
//! anyone: their orders with a link to each, which it cancels from its
//! page, places one, and lists the products, each linked to its page, which
//! shows the first page of its reviews, and searches and writes a product's
//! reviews, each list a page at a time; a signed-out shopper gets 401 and a
//! sign-in link that comes back. The orders' procedure answers a page
//! record. The pages, the search and the refusals of a forged cursor are
//! the same over the shop in memory and over a SQLite file of shop-db's
//! tables, where the reviews, orders and sessions outlive the app, and the
//! console lists the orders. A shopper's pages, the console's and the JSON
//! API's list, show and cancel their own orders alone, another customer's
//! answering 404, and staff's every customer's. Every page is one HTML
//! document, whose head loads Topcoat's runtime from a hosted catalog of its
//! script alone, as a bundle would serve it; the console's lists render
//! again in place through their shards, for a filter, a page and a search,
//! the runtime renders a page again as its GET, and without the runtime the
//! pages load no script and the lists are GET forms.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::thread::sleep;
use std::time::Duration;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{SHOPPER_PERMISSIONS, Shop, add_shopper, add_staff, users_at, users_in_memory};
use acme_shop_orders_topcoat::api::runtime::identity::Service;
use acme_shop_orders_topcoat::api::runtime::{Principal, RequestContext};
use acme_shop_orders_topcoat::api::{ProductReviewsImplementation, ProductReviewsWriteReviewArgs, types};
use acme_shop_orders_topcoat::procedures::{OrderListOrdersArgsRecord, call_order_list_orders};
use acme_shop_orders_topcoat::records::OrderViewPageRecord;
use acme_shop_topcoat::{CATALOG, PRODUCT, Product, app, product, shop_in_memory, stock_sqlite};
use topcoat::asset::{AssetConfig, Manifest};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{Body, Router, StatusCode, header, page, to_bytes};
use topcoat::view::{View, view};

/// The app's identity config at a low password cost, so a test hashes
/// fast.
const CONFIG: &str = r#"{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;

/// The sign-in form of the shopper `shop` adds, who holds `orders.read`
/// and `orders.write` through the shopper role.
const SHOPPER: &str = "login=grace%40example.com&password=grace%27s+password";

/// A user's login, name and password.
type Person = (&'static str, &'static str, &'static str);

/// A second shopper, whom `each_shop` adds beside the demo's.
const ALAN: Person = ("alan@example.com", "Alan Turing", "alan's password");

/// A member of staff, whom `each_shop` adds too, who holds `orders.any`
/// through the staff role, and lists, reads and cancels every customer's
/// orders.
const ADA: Person = ("ada@example.com", "Ada Lovelace", "ada's password");

/// The asset catalog the tests serve Topcoat's runtime with: its script
/// alone, hosted at `/_topcoat/assets`, as the bundle `topcoat asset bundle`
/// writes would serve it, so the pages render the script's tag without a
/// bundle beside the test binary.
fn runtime_assets() -> AssetConfig {
    let toml = format!(
        "version = 1\n\n[[assets]]\nid = {}\nfile = \"topcoat.js\"\nhash = \"0\"\ncontent_type = \"text/javascript\"\n",
        topcoat::runtime::SCRIPT.id().as_u64()
    );
    AssetConfig::hosted_at("/_topcoat/assets", Manifest::parse(&toml).expect("a manifest"))
}

/// The runtime's script tag, as the root layout renders it with
/// `runtime_assets`.
const SCRIPT: &str = r#"<script type="module" src="/_topcoat/assets/topcoat.js""#;

async fn shop() -> Router {
    let users = users_in_memory(CONFIG).unwrap();
    add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    app(Arc::new(shop_in_memory()), users, Some(runtime_assets()))
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

/// The app over the SQLite shop at `path`, as src/main.rs opens it.
async fn sqlite_shop(path: &Path) -> Router {
    let (shop, users) = open_sqlite(path).await;
    app(Arc::new(shop), users, Some(runtime_assets()))
}

/// The SQLite shop at `path` and its users, the file's, with the demo's
/// shopper and the catalog's products: the shopper is added unless an
/// earlier app over the file added them.
async fn open_sqlite(path: &Path) -> (SqliteShop, Arc<Service>) {
    let url = format!("sqlite:{}", path.display());
    let shop = SqliteShop::open(&url).unwrap();
    let users = users_at(&url, CONFIG).unwrap();
    if users.store().find_login("grace@example.com").await.is_err() {
        add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    }
    stock_sqlite(&shop).unwrap();
    (shop, users)
}

/// Adds `ALAN`, a shopper, and `ADA`, staff, beside the demo's shopper.
async fn add_alan_and_ada(users: &Service) {
    let (login, name, password) = ALAN;
    add_shopper(users, login, name, password).await.unwrap();
    let (login, name, password) = ADA;
    add_staff(users, login, name, password).await.unwrap();
}

/// Products beside the app's catalog, so that a page of one product, or
/// of two, has a next: the catalog lists them by name, after the anvil.
const MORE: [Product; 2] = [
    Product { id: "00000000-0000-4000-8000-0000000000b2", sku: "bellows", name: "Bellows", price_cents: 2500 },
    Product { id: "00000000-0000-4000-8000-0000000000c3", sku: "chisel", name: "Chisel", price_cents: 450 },
];

/// Reviews of the anvil for the search to find: a title and a body each.
const REVIEWS: [(&str, &str); 3] = [
    ("Sturdy and heavy", "Survived three coyotes."),
    ("Too heavy", "100% iron, and it shows."),
    ("Rings true", "A STURDY anvil, heavy enough."),
];

/// The `n`th reviewer's id: each review has a shopper of its own, since
/// shop-db's one_per_author index lets a shopper review a product once.
fn reviewer(n: usize) -> String {
    format!("00000000-0000-4000-8000-{:012x}", 0xe00 + n)
}

/// Writes a review of the anvil for each title and body, the `n`th as the
/// `n`th reviewer, a few milliseconds apart, so the reviews list, oldest
/// first, in the order written.
async fn write_reviews<S: ProductReviewsImplementation>(shop: &S, reviews: &[(&str, &str)]) {
    for (n, (title, body)) in reviews.iter().enumerate() {
        let caller = RequestContext { principal: Some(Principal::new(reviewer(n), SHOPPER_PERMISSIONS)), ..RequestContext::default() };
        let input = types::WriteReviewInput { rating: 4.0, title: (*title).to_owned(), body: (*body).to_owned() };
        shop.write_review(caller, ProductReviewsWriteReviewArgs { product_id: product(), input }).await.unwrap();
        sleep(Duration::from_millis(3));
    }
}

/// The app over each kind of shop, by its name: in memory, and over a new
/// SQLite file of shop-db's tables (`file`, as `sqlite_file` makes it).
/// Each holds the app's catalog and `MORE`, the demo's shopper, `ALAN` and
/// `ADA`, and `reviews` of the anvil, each by a reviewer of its own.
async fn each_shop(file: &str, reviews: &[(&str, &str)]) -> [(&'static str, Router); 2] {
    let memory = Shop::with_products(CATALOG.iter().chain(&MORE).map(Product::view));
    write_reviews(&memory, reviews).await;
    let users = users_in_memory(CONFIG).unwrap();
    add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    add_alan_and_ada(&users).await;
    let in_memory = app(Arc::new(memory), users, Some(runtime_assets()));

    let (sqlite, users) = open_sqlite(&sqlite_file(file)).await;
    add_alan_and_ada(&users).await;
    for product in &MORE {
        sqlite.add_product(&product.id(), product.sku, product.name, product.price_cents).unwrap();
    }
    for n in 0..reviews.len() {
        let id = reviewer(n).parse().unwrap();
        sqlite.add_user(&id, &format!("reviewer{n}@example.com"), "A reviewer").unwrap();
    }
    write_reviews(&sqlite, reviews).await;
    [("in memory", in_memory), ("over SQLite", app(Arc::new(sqlite), users, Some(runtime_assets())))]
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

/// Signs `person` in, as `sign_in` signs the demo shopper in.
async fn sign_in_as(router: &Router, (login, _, password): Person) -> String {
    let form = form_urlencoded::Serializer::new(String::new()).append_pair("login", login).append_pair("password", password).finish();
    send(router, "POST", "/sign-in", None, Some(&form)).await.cookie.expect("the session cookie")
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

/// Places `count` orders through the form, a few milliseconds apart, so the
/// orders list, oldest first, in the order placed: their pages.
async fn place_orders(router: &Router, cookie: &str, count: usize) -> Vec<String> {
    let mut placed = Vec::new();
    for _ in 0..count {
        placed.push(place_order(router, cookie).await);
        sleep(Duration::from_millis(3));
    }
    placed
}

/// Each text of `body` between `start` and the next `end`.
fn each_between<'a>(body: &'a str, start: &str, end: &str) -> Vec<&'a str> {
    body.split(start).skip(1).filter_map(|rest| rest.split_once(end).map(|(text, _)| text)).collect()
}

/// The page's link to another of the list's pages, `rel` `next` or
/// `first`, as the browser follows it.
fn link(body: &str, rel: &str) -> Option<String> {
    each_between(body, &format!(r#"<a rel="{rel}" href=""#), "\"").first().map(|href| href.replace("&amp;", "&"))
}

/// The orders the shop's orders page links to, by their pages.
fn order_links(body: &str) -> Vec<&str> {
    each_between(body, r#"<li><a href=""#, "\"").into_iter().filter(|href| href.starts_with("/orders/")).collect()
}

/// The ids of the orders a console table lists.
fn console_orders(body: &str) -> Vec<&str> {
    each_between(body, r#"<td data-field="id"><a href="/console/orders/"#, "\"")
}

/// The names of the products a console table lists.
fn product_names(body: &str) -> Vec<&str> {
    each_between(body, r#"<th scope="row" data-field="name"><a href=""#, "</a>")
        .into_iter()
        .filter_map(|cell| cell.split_once("\">").map(|(_, name)| name))
        .collect()
}

/// The titles of the reviews a console table lists.
fn review_titles(body: &str) -> Vec<&str> {
    each_between(body, r#"<th scope="row" data-field="title">"#, "</th>")
}

#[tokio::test]
async fn the_reviews_page_renders_the_form_from_the_input_rules() {
    let page = send(&shop().await, "GET", "/reviews", None, None).await;
    assert_eq!(page.status, StatusCode::OK);
    assert!(page.body.contains("No reviews yet."), "{}", page.body);
    assert!(page.body.contains(r#"name="rating" type="number" required="" step="any" min="1" max="5""#), "{}", page.body);
    assert!(page.body.contains(r#"name="title" type="text" required="" minlength="1" maxlength="120""#), "{}", page.body);
    // A body of up to 5000 characters is long text, a textarea.
    assert!(page.body.contains("<textarea id=\"write-review-input-body\" name=\"body\" rows=\"6\" required=\"\" maxlength=\"5000\">\n</textarea>"), "{}", page.body);
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
    // The filter's submit keeps the route's 401, its message the form's.
    let signed_out = send(&router, "GET", "/orders", None, None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED);
    assert!(signed_out.body.contains(r#"<p class="form-error">Authentication required</p>"#), "{}", signed_out.body);
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
    assert!(api.body.contains(r#""data":{"items":[],"next":null}"#), "{}", api.body);

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
    // first, refuses with the operation's status and message.
    let form = send(&router, "GET", "/orders/new", None, None).await;
    assert!(form.body.contains(r#"<input type="hidden" name="next" value="/orders/new">"#), "{}", form.body);
    let refused = send(&router, "POST", "/orders/new", None, Some(&order_form("2"))).await;
    assert_eq!(refused.status, StatusCode::UNAUTHORIZED);
    assert!(refused.body.contains(r#"<p class="form-error">Authentication required</p>"#), "{}", refused.body);
}

#[tokio::test]
async fn an_order_is_placed_through_its_form_with_a_row_per_line() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let anvil = product().to_string();

    // A new form: one line, its product a select of the products
    // `listProducts` lists, each by its name, its value the id as the API
    // sends it.
    let form = send(&router, "GET", "/orders/new", Some(&cookie), None).await;
    assert_eq!(form.status, StatusCode::OK);
    for want in [
        r#"<fieldset class="ss-row"><legend>Line 1</legend><input type="hidden" name="lines[0]" value="">"#,
        r#"<select id="place-order-input-lines-0-product-id" name="lines[0].productId" required=""><option value="" selected=""></option>"#,
        &format!(r#"<option value="{anvil}">Anvil</option></select>"#),
        r#"name="lines[0].quantity" type="number" required="" step="any" min="1" max="99">"#,
        r#"<button type="submit" class="ss-add" name="_action" value="add:lines" formnovalidate="">Add a line</button>"#,
        r#"name="shippingAddress.country" type="text" required="" pattern="^[A-Z]{2}$">"#,
        // A number that ends a field's name is a word of its label.
        r#"<label for="place-order-input-shipping-address-line1">Line 1</label>"#,
    ] {
        assert!(form.body.contains(want), "{want}\n{}", form.body);
    }
    assert!(!form.body.contains("<legend>Line 2</legend>"), "{}", form.body);

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
    // Each line's product by its name, which the catalog's lookup gives.
    assert!(page.body.contains(r#"<td data-field="productId">Anvil</td>"#), "{}", page.body);
    assert!(!page.body.contains(&format!(">{anvil}<")), "{}", page.body);
    // When it was placed, in UTC to the minute, the value as sent kept.
    assert!(page.body.contains(r#"<div data-field="placedAt"><dt>Placed at</dt><dd><time datetime=""#), "{}", page.body);
    assert!(page.body.contains(" UTC</time>"), "{}", page.body);
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
    assert!(page.body.contains("<textarea id=\"order-cancel-order-reason\" name=\"reason\" rows=\"6\" maxlength=\"500\">\n</textarea>"), "{}", page.body);
    assert!(page.body.contains(r#"<button type="submit">Cancel order</button>"#), "{}", page.body);

    // A reason past its 500 characters: 422, the route's message at the
    // reason, and the order still placed.
    let cancel = format!("{order}/cancel");
    let long = send(&router, "POST", &cancel, Some(&cookie), Some(&format!("reason={}", "x".repeat(501)))).await;
    assert_eq!(long.status, StatusCode::UNPROCESSABLE_ENTITY);
    let sent = format!("aria-invalid=\"true\">\n{}</textarea><p class=\"field-error\">must be at most 500 characters</p>", "x".repeat(501));
    assert!(long.body.contains(&sent), "{}", long.body);
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

    // Cancelling it again: the operation's 409, its status the page's and
    // its message the form's own.
    let again = send(&router, "POST", &cancel, Some(&cookie), Some("reason=Twice")).await;
    assert_eq!(again.status, StatusCode::CONFLICT);
    let message = format!(r#"<form method="post" action="{cancel}"><p class="form-error">order {id} is cancelled</p>"#);
    assert!(again.body.contains(&message), "{}", again.body);
    assert!(again.body.contains("maxlength=\"500\">\nTwice</textarea>"), "{}", again.body);
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
    let none = send(&router, "GET", "/orders?statuses=shipped", Some(&cookie), None).await;
    assert!(none.body.contains("No orders match the filter."), "{}", none.body);
}

#[tokio::test]
async fn a_shopper_sees_their_own_orders_and_staff_every_customers() {
    for (shop, router) in each_shop("customers.db", &[]).await {
        let grace = sign_in(&router).await;
        let alan = sign_in_as(&router, ALAN).await;
        let ada = sign_in_as(&router, ADA).await;
        let graces = place_orders(&router, &grace, 2).await;
        let alans = place_orders(&router, &alan, 2).await;
        let ids = |pages: &[String]| pages.iter().map(|page| page.trim_start_matches("/orders/").to_owned()).collect::<Vec<_>>();

        for (who, cookie, own, other) in [("Grace", &grace, &graces, &alans), ("Alan", &alan, &alans, &graces)] {
            // Their orders page, a page of one at a time, and the
            // console's, list their own orders alone, and so does the API.
            let first = send(&router, "GET", "/orders?limit=1", Some(cookie), None).await;
            assert_eq!(order_links(&first.body), own[..1], "{shop} {who}");
            let next = link(&first.body, "next").expect("a next page");
            let second = send(&router, "GET", &next, Some(cookie), None).await;
            assert_eq!(order_links(&second.body), own[1..], "{shop} {who}");
            assert_eq!(link(&second.body, "next"), None, "{shop} {who}");
            let console = send(&router, "GET", "/console/orders", Some(cookie), None).await;
            assert_eq!(console_orders(&console.body), ids(own), "{shop} {who}");
            let api = send(&router, "GET", "/api/orders", Some(cookie), None).await;
            assert_eq!(api.status, StatusCode::OK, "{shop} {who}: {}", api.body);
            assert_eq!(api.body.matches(r#""shippingAddress""#).count(), 2, "{shop} {who}: {}", api.body);

            // Another customer's order is the 404 of one no one placed:
            // its page, its console page, its cancel from either and the
            // API's get and cancel, and it stays placed.
            let theirs = &other[0];
            let id = theirs.trim_start_matches("/orders/");
            let page = send(&router, "GET", theirs, Some(cookie), None).await;
            assert_eq!(page.status, StatusCode::NOT_FOUND, "{shop} {who}: {}", page.body);
            assert!(page.body.contains(r#"<p class="refusal">order not found</p>"#), "{shop} {who}: {}", page.body);
            assert!(!page.body.contains("Analytical Way"), "{shop} {who}: {}", page.body);
            let console_page = send(&router, "GET", &format!("/console{theirs}"), Some(cookie), None).await;
            assert_eq!(console_page.status, StatusCode::NOT_FOUND, "{shop} {who}: {}", console_page.body);
            for cancel in [format!("{theirs}/cancel"), format!("/console{theirs}/cancel")] {
                let refused = send(&router, "POST", &cancel, Some(cookie), Some("reason=Not+mine")).await;
                assert_eq!(refused.status, StatusCode::NOT_FOUND, "{shop} {who} {cancel}: {}", refused.body);
            }
            let api = send(&router, "GET", &format!("/api/orders/{id}"), Some(cookie), None).await;
            assert_eq!(api.status, StatusCode::NOT_FOUND, "{shop} {who}: {}", api.body);
            let api = call_api(&router, "POST", &format!("/api/orders/{id}/cancel"), cookie, r#"{"reason":"Not mine"}"#).await;
            assert_eq!(api.status, StatusCode::NOT_FOUND, "{shop} {who}: {}", api.body);
        }

        // Staff list every customer's orders, as they were placed, on the
        // orders page and the console's, each still placed, and cancel any.
        let everyone: Vec<String> = graces.iter().chain(&alans).cloned().collect();
        let orders = send(&router, "GET", "/orders?statuses=placed", Some(&ada), None).await;
        assert_eq!(order_links(&orders.body), everyone, "{shop}");
        let console = send(&router, "GET", "/console/orders", Some(&ada), None).await;
        assert_eq!(console_orders(&console.body), ids(&everyone), "{shop}");
        let page = send(&router, "GET", &graces[0], Some(&ada), None).await;
        assert_eq!(page.status, StatusCode::OK, "{shop}: {}", page.body);
        let cancelled = send(&router, "POST", &format!("{}/cancel", alans[0]), Some(&ada), Some("reason=Out+of+stock")).await;
        assert_eq!((cancelled.status, cancelled.location.as_deref()), (StatusCode::SEE_OTHER, Some(alans[0].as_str())), "{shop}: {}", cancelled.body);
        let console_cancel = format!("/console{}/cancel", graces[1]);
        let cancelled = send(&router, "POST", &console_cancel, Some(&ada), Some("reason=Out+of+stock")).await;
        assert_eq!(cancelled.status, StatusCode::SEE_OTHER, "{shop}: {}", cancelled.body);

        // Each shopper sees their order cancelled.
        for (cookie, order) in [(&alan, &alans[0]), (&grace, &graces[1])] {
            let page = send(&router, "GET", order, Some(cookie), None).await;
            assert!(page.body.contains(r#"<data value="cancelled">Cancelled</data>"#), "{shop}: {}", page.body);
            assert!(page.body.contains("<dd>Out of stock</dd>"), "{shop}: {}", page.body);
        }
    }
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
    // The console lists the file's orders, each linked to its page.
    let console = send(&router, "GET", "/console/orders", Some(&cookie), None).await;
    assert_eq!(console.status, StatusCode::OK, "{}", console.body);
    assert!(console.body.contains(&format!(r#"<a href="/console{order}">"#)), "{}", console.body);

    // review's search_text, the VIRTUAL column shop-db's @searchField gives
    // it on SQLite, joins its title and body.
    let search_text: String = rusqlite::Connection::open(&path)
        .unwrap()
        .query_row("SELECT search_text FROM review", [], |row| row.get(0))
        .unwrap();
    assert_eq!(search_text, "Sturdy Survived three coyotes.");
}

#[tokio::test]
async fn the_console_shows_what_the_shopper_may_list() {
    let router = shop().await;
    // The shop's own pages link the console.
    let reviews = send(&router, "GET", "/reviews", None, None).await;
    assert!(reviews.body.contains(r#"<a href="/console">Console</a>"#), "{}", reviews.body);

    // Signed out, the console lists the products, which anyone may list,
    // and offers to sign in, and the orders answer 401 with a sign-in link
    // that comes back to them.
    let index = send(&router, "GET", "/console", None, None).await;
    assert_eq!(index.status, StatusCode::OK);
    assert!(index.body.contains(r#"<header class="ss-header"><a href="/console">Acme shop console</a></header>"#), "{}", index.body);
    assert!(index.body.contains(r#"<nav class="ss-nav" aria-label="Resources"><ul><li><a href="/console/products">Products</a></li></ul></nav>"#), "{}", index.body);
    assert!(index.body.contains(r#"<ul class="ss-index"><li><a href="/console/products">Products</a></li></ul>"#), "{}", index.body);
    assert!(index.body.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole">Sign in</a>"#), "{}", index.body);
    let orders = send(&router, "GET", "/console/orders", None, None).await;
    assert_eq!(orders.status, StatusCode::UNAUTHORIZED);
    assert!(orders.body.contains(r#"<a class="ss-sign-in" href="/sign-in?next=%2Fconsole%2Forders">Sign in</a>"#), "{}", orders.body);
    let signed_in = send(&router, "POST", "/sign-in", None, Some(&format!("{SHOPPER}&next=%2Fconsole%2Forders"))).await;
    assert_eq!((signed_in.status, signed_in.location.as_deref()), (StatusCode::SEE_OTHER, Some("/console/orders")));

    // Signed in, the nav and the index list the orders too.
    let cookie = signed_in.cookie.expect("the session cookie");
    let index = send(&router, "GET", "/console", Some(&cookie), None).await;
    let links = r#"<li><a href="/console/orders">Orders</a></li><li><a href="/console/products">Products</a></li>"#;
    assert!(index.body.contains(&format!(r#"<nav class="ss-nav" aria-label="Resources"><ul>{links}</ul></nav>"#)), "{}", index.body);
    assert!(index.body.contains(&format!(r#"<ul class="ss-index">{links}</ul>"#)), "{}", index.body);
    assert!(!index.body.contains("ss-sign-in"), "{}", index.body);
}

#[tokio::test]
async fn the_console_lists_orders_and_cancels_one_from_its_page() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let order = place_order(&router, &cookie).await;
    let id = order.trim_start_matches("/orders/");
    let other = place_order(&router, &cookie).await;
    let cancelled = send(&router, "POST", &format!("{other}/cancel"), Some(&cookie), Some("reason=")).await;
    assert_eq!(cancelled.status, StatusCode::SEE_OTHER, "{}", cancelled.body);

    // The placed orders, each linked to its console page, and the link to
    // place one.
    let list = send(&router, "GET", "/console/orders?statuses=placed", Some(&cookie), None).await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.body);
    assert!(list.body.contains(&format!(r#"<td data-field="id"><a href="/console/orders/{id}">{id}</a></td>"#)), "{}", list.body);
    assert!(!list.body.contains(other.trim_start_matches("/orders/")), "{}", list.body);
    assert!(list.body.contains(r#"<input type="checkbox" name="statuses" value="placed" checked="">"#), "{}", list.body);
    assert!(list.body.contains(r#"<a class="ss-new" href="/console/orders/new">New order</a>"#), "{}", list.body);

    // Its page: the order, its lines by their products' names, and the
    // form that cancels it.
    let page_path = format!("/console/orders/{id}");
    let page = send(&router, "GET", &page_path, Some(&cookie), None).await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.body);
    assert!(page.body.contains(r#"<td data-field="productId">Anvil</td>"#), "{}", page.body);
    assert!(page.body.contains(&format!(r#"<form method="post" action="{page_path}/cancel" class="ss-action" data-action="cancel">"#)), "{}", page.body);
    assert!(page.body.contains(&format!(r#"<li><a href="/console/orders">Orders</a></li><li><span aria-current="page">Order {id}</span></li>"#)), "{}", page.body);

    // A cancel goes back to the order (303), whatever the hidden id says.
    let other_id = other.trim_start_matches("/orders/");
    let cancel = format!("{page_path}/cancel");
    let done = send(&router, "POST", &cancel, Some(&cookie), Some(&format!("id={other_id}&reason=Changed+my+mind"))).await;
    assert_eq!((done.status, done.location.as_deref()), (StatusCode::SEE_OTHER, Some(page_path.as_str())), "{}", done.body);
    let page = send(&router, "GET", &page_path, Some(&cookie), None).await;
    assert!(page.body.contains(r#"<data value="cancelled">Cancelled</data>"#), "{}", page.body);
    assert!(page.body.contains("<dd>Changed my mind</dd>"), "{}", page.body);

    // A second cancel: the operation's 409 on the order's page, its
    // message the form's own.
    let again = send(&router, "POST", &cancel, Some(&cookie), Some("reason=Twice")).await;
    assert_eq!(again.status, StatusCode::CONFLICT, "{}", again.body);
    assert!(again.body.contains(&format!(r#"<p class="form-error">order {id} is cancelled</p>"#)), "{}", again.body);
    let missing = send(&router, "GET", "/console/orders/not-an-order", Some(&cookie), None).await;
    assert_eq!(missing.status, StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn the_console_places_an_order_with_the_catalogs_products() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    // Each line's product is a select of the products `listProducts`
    // lists, which the console names with no choices of the app's.
    let new = send(&router, "GET", "/console/orders/new", Some(&cookie), None).await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.body);
    let select = format!(
        r#"<select id="place-order-input-lines-0-product-id" name="lines[0].productId" required=""><option value="" selected=""></option><option value="{}">Anvil</option></select>"#,
        product()
    );
    assert!(new.body.contains(&select), "{}", new.body);

    // A row button adds a line and places nothing.
    let added = send(&router, "POST", "/console/orders/new", Some(&cookie), Some(&format!("{}&_action=add:lines", order_form("2")))).await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.body);
    assert!(added.body.contains(r#"name="lines[1].productId""#), "{}", added.body);

    // A quantity out of range: 422 at its control.
    let refused = send(&router, "POST", "/console/orders/new", Some(&cookie), Some(&order_form("0"))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.body);

    let placed = send(&router, "POST", "/console/orders/new", Some(&cookie), Some(&order_form("2"))).await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.body);
    let location = placed.location.expect("the order's console page");
    assert!(location.starts_with("/console/orders/"), "{location}");
    let page = send(&router, "GET", &location, Some(&cookie), None).await;
    assert!(page.body.contains(r#"<data value="placed">Placed</data>"#) && page.body.contains("Anvil"), "{}", page.body);

    // Signed out, the post is refused with 401, its sign-in link coming
    // back to the new page.
    let signed_out = send(&router, "POST", "/console/orders/new", None, Some(&order_form("2"))).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{}", signed_out.body);
    assert!(signed_out.body.contains(r#"href="/sign-in?next=%2Fconsole%2Forders%2Fnew""#), "{}", signed_out.body);
}

#[tokio::test]
async fn the_console_lists_and_writes_a_products_reviews() {
    let router = shop().await;
    let reviews = format!("/console/products/{PRODUCT}/reviews");
    // Anyone may list them; only a signed-in shopper gets the new page.
    let empty = send(&router, "GET", &reviews, None, None).await;
    assert_eq!(empty.status, StatusCode::OK, "{}", empty.body);
    assert!(empty.body.contains(r#"<p class="ss-empty">There are no reviews.</p>"#), "{}", empty.body);
    assert!(!empty.body.contains("ss-new"), "{}", empty.body);
    // The trail names the product by its title, through getProduct.
    assert!(empty.body.contains(&format!(
        r#"<li><a href="/console/products">Products</a></li><li><a href="/console/products/{PRODUCT}">Anvil</a></li><li><span aria-current="page">Reviews</span></li>"#
    )), "{}", empty.body);

    let cookie = sign_in(&router).await;
    let list = send(&router, "GET", &reviews, Some(&cookie), None).await;
    assert!(list.body.contains(&format!(r#"<a class="ss-new" href="{reviews}/new">New review</a>"#)), "{}", list.body);
    let new = send(&router, "GET", &format!("{reviews}/new"), Some(&cookie), None).await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.body);
    assert!(new.body.contains(r#"name="body" rows="6" required="" maxlength="5000">"#), "{}", new.body);

    let refused = send(&router, "POST", &format!("{reviews}/new"), Some(&cookie), Some("rating=9&title=Heavy&body=Very")).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.body);
    let written = send(&router, "POST", &format!("{reviews}/new"), Some(&cookie), Some("rating=5&title=Heavy&body=Very+heavy")).await;
    assert_eq!((written.status, written.location.as_deref()), (StatusCode::SEE_OTHER, Some(reviews.as_str())), "{}", written.body);
    let list = send(&router, "GET", &reviews, None, None).await;
    assert!(list.body.contains(r#"<th scope="row" data-field="title">Heavy</th>"#), "{}", list.body);
}

#[tokio::test]
async fn the_orders_page_links_its_next_page_and_its_first() {
    for (shop, router) in each_shop("orders-pages.db", &[]).await {
        let cookie = sign_in(&router).await;
        let placed = place_orders(&router, &cookie, 3).await;

        // Two a page, oldest first: the first page links the next, which
        // links back to the first, each with the limit.
        let first = send(&router, "GET", "/orders?limit=2", Some(&cookie), None).await;
        assert_eq!(first.status, StatusCode::OK, "{shop}: {}", first.body);
        assert_eq!(order_links(&first.body), placed[..2], "{shop}");
        assert_eq!(link(&first.body, "first"), None, "{shop}");
        let next = link(&first.body, "next").expect("a next page");
        assert!(next.starts_with("/orders?limit=2&cursor="), "{shop}: {next}");
        let second = send(&router, "GET", &next, Some(&cookie), None).await;
        assert_eq!(second.status, StatusCode::OK, "{shop}: {}", second.body);
        assert_eq!(order_links(&second.body), placed[2..], "{shop}");
        assert_eq!(link(&second.body, "next"), None, "{shop}");
        assert_eq!(link(&second.body, "first").as_deref(), Some("/orders?limit=2"), "{shop}");

        // The filter holds on every page, and the links carry it.
        let cancelled = send(&router, "POST", &format!("{}/cancel", placed[1]), Some(&cookie), Some("reason=")).await;
        assert_eq!(cancelled.status, StatusCode::SEE_OTHER, "{shop}: {}", cancelled.body);
        let first = send(&router, "GET", "/orders?statuses=placed&limit=1", Some(&cookie), None).await;
        assert_eq!(order_links(&first.body), [placed[0].as_str()], "{shop}");
        let next = link(&first.body, "next").expect("a next page");
        assert!(next.starts_with("/orders?statuses=placed&limit=1&cursor="), "{shop}: {next}");
        let second = send(&router, "GET", &next, Some(&cookie), None).await;
        assert_eq!(order_links(&second.body), [placed[2].as_str()], "{shop}");
        assert!(second.body.contains(r#"<input type="checkbox" name="statuses" value="placed" checked="">"#), "{shop}: {}", second.body);
        assert_eq!(link(&second.body, "next"), None, "{shop}");
        assert_eq!(link(&second.body, "first").as_deref(), Some("/orders?statuses=placed&limit=1"), "{shop}");
    }
}

#[tokio::test]
async fn a_forged_cursor_or_a_limit_out_of_range_is_the_routes_400() {
    for (shop, router) in each_shop("forged.db", &REVIEWS).await {
        let cookie = sign_in(&router).await;
        place_order(&router, &cookie).await;

        // A cursor no list returned, `{}` encoded: the route's 400, its
        // message the form's own, no orders, and the link to the first page.
        let forged = send(&router, "GET", "/orders?cursor=e30", Some(&cookie), None).await;
        assert_eq!(forged.status, StatusCode::BAD_REQUEST, "{shop}: {}", forged.body);
        assert!(forged.body.contains(r#"<p class="form-error">cursor: not a cursor this operation returned</p>"#), "{shop}: {}", forged.body);
        assert!(!forged.body.contains("<table"), "{shop}: {}", forged.body);
        assert_eq!(link(&forged.body, "first").as_deref(), Some("/orders"), "{shop}");
        // One outside the base64url alphabet, and a limit past the route's
        // 100, the same.
        let alphabet = send(&router, "GET", "/orders?cursor=e30%3D", Some(&cookie), None).await;
        assert_eq!(alphabet.status, StatusCode::BAD_REQUEST, "{shop}: {}", alphabet.body);
        assert!(alphabet.body.contains(r#"<p class="form-error">cursor: does not match the required pattern</p>"#), "{shop}: {}", alphabet.body);
        let limit = send(&router, "GET", "/orders?limit=500", Some(&cookie), None).await;
        assert_eq!(limit.status, StatusCode::BAD_REQUEST, "{shop}: {}", limit.body);
        assert!(limit.body.contains(r#"<p class="form-error">limit: must be at most 100</p>"#), "{shop}: {}", limit.body);
        assert!(!limit.body.contains("<table"), "{shop}: {}", limit.body);
        // A signed-out shopper is refused first, with no pages to link.
        let signed_out = send(&router, "GET", "/orders?cursor=e30", None, None).await;
        assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{shop}: {}", signed_out.body);
        assert_eq!(link(&signed_out.body, "first"), None, "{shop}");

        // The console's lists answer a forged cursor alike.
        let reviews = format!("/console/products/{PRODUCT}/reviews");
        for list in ["/console/orders", "/console/products", reviews.as_str()] {
            let forged = send(&router, "GET", &format!("{list}?cursor=e30"), Some(&cookie), None).await;
            assert_eq!(forged.status, StatusCode::BAD_REQUEST, "{shop} {list}: {}", forged.body);
            assert!(forged.body.contains(r#"<p class="form-error">cursor: not a cursor this operation returned</p>"#), "{shop} {list}: {}", forged.body);
            assert_eq!(link(&forged.body, "first").as_deref(), Some(list), "{shop}");
        }
    }
}

#[tokio::test]
async fn the_console_pages_the_products_and_the_orders() {
    for (shop, router) in each_shop("console-pages.db", &[]).await {
        // Anyone may list the products, by name, two a page here.
        let first = send(&router, "GET", "/console/products?limit=2", None, None).await;
        assert_eq!(first.status, StatusCode::OK, "{shop}: {}", first.body);
        assert_eq!(product_names(&first.body), ["Anvil", "Bellows"], "{shop}");
        assert_eq!(link(&first.body, "first"), None, "{shop}");
        let next = link(&first.body, "next").expect("a next page");
        assert!(next.starts_with("/console/products?limit=2&cursor="), "{shop}: {next}");
        let second = send(&router, "GET", &next, None, None).await;
        assert_eq!(product_names(&second.body), ["Chisel"], "{shop}");
        assert_eq!(link(&second.body, "next"), None, "{shop}");
        assert_eq!(link(&second.body, "first").as_deref(), Some("/console/products?limit=2"), "{shop}");
        let again = send(&router, "GET", "/console/products?limit=2", None, None).await;
        assert_eq!(product_names(&again.body), ["Anvil", "Bellows"], "{shop}");

        // Each row links to its product's page, by its id as the API sends
        // it, compact, which getProduct's argument refers to.
        let anvil = product().to_string();
        let row = format!(r#"<th scope="row" data-field="name"><a href="/console/products/{anvil}">Anvil</a></th>"#);
        assert!(first.body.contains(&row), "{shop}: {}", first.body);
        assert!(!first.body.contains(PRODUCT), "{shop}: {}", first.body);

        // The lookup, `ids`, is a select of the products for each id, and
        // lists those given.
        let bellows = MORE[0].id().to_string();
        let looked_up = send(&router, "GET", &format!("/console/products?ids={bellows}"), None, None).await;
        assert_eq!(looked_up.status, StatusCode::OK, "{shop}: {}", looked_up.body);
        assert_eq!(product_names(&looked_up.body), ["Bellows"], "{shop}");
        let select = format!(
            r#"<select id="product-catalog-list-products-ids-0" name="ids"><option value=""></option><option value="{anvil}">Anvil</option><option value="{bellows}" selected="">Bellows</option><option value="{}">Chisel</option></select>"#,
            MORE[1].id()
        );
        assert!(looked_up.body.contains(&select), "{shop}: {}", looked_up.body);

        // The shopper's orders, one a page.
        let cookie = sign_in(&router).await;
        let placed = place_orders(&router, &cookie, 2).await;
        let ids: Vec<&str> = placed.iter().map(|order| order.trim_start_matches("/orders/")).collect();
        let first = send(&router, "GET", "/console/orders?limit=1", Some(&cookie), None).await;
        assert_eq!(console_orders(&first.body), ids[..1], "{shop}");
        let next = link(&first.body, "next").expect("a next page");
        assert!(next.starts_with("/console/orders?limit=1&cursor="), "{shop}: {next}");
        let second = send(&router, "GET", &next, Some(&cookie), None).await;
        assert_eq!(console_orders(&second.body), ids[1..], "{shop}");
        assert_eq!(link(&second.body, "next"), None, "{shop}");
        assert_eq!(link(&second.body, "first").as_deref(), Some("/console/orders?limit=1"), "{shop}");
    }
}

#[tokio::test]
async fn a_products_page_shows_the_first_page_of_its_reviews() {
    let titles: Vec<String> = (1..=21).map(|n| format!("Review {n}")).collect();
    let reviews: Vec<(&str, &str)> = titles.iter().map(|title| (title.as_str(), "Heavy.")).collect();
    for (shop, router) in each_shop("product-page.db", &reviews).await {
        let page = send(&router, "GET", &format!("/console/products/{PRODUCT}"), None, None).await;
        assert_eq!(page.status, StatusCode::OK, "{shop}: {}", page.body);
        assert!(page.body.contains(r#"<li><a href="/console/products">Products</a></li><li><span aria-current="page">Anvil</span></li>"#), "{shop}: {}", page.body);
        assert!(page.body.contains(r#"<h1>Anvil</h1><div class="ss-detail" data-type="ProductView" role="group" aria-label="Anvil">"#), "{shop}: {}", page.body);

        // Its reviews' first page, of the route's default 20, oldest first,
        // and the link to the next, the list's own page.
        let (_, section) = page.body.split_once(r#"<section class="ss-section" data-resource="reviews">"#).expect("the reviews");
        assert_eq!(review_titles(section), titles[..20], "{shop}");
        let reviews = format!("/console/products/{PRODUCT}/reviews");
        let next = link(section, "next").expect("a next page");
        assert!(next.starts_with(&format!("{reviews}?cursor=")), "{shop}: {next}");
        let rest = send(&router, "GET", &next, None, None).await;
        assert_eq!(rest.status, StatusCode::OK, "{shop}: {}", rest.body);
        assert_eq!(review_titles(&rest.body), ["Review 21"], "{shop}");
        assert_eq!(link(&rest.body, "first"), Some(reviews), "{shop}");
    }
}

#[tokio::test]
async fn the_console_searches_a_products_reviews() {
    for (shop, router) in each_shop("search.db", &REVIEWS).await {
        let reviews = format!("/console/products/{PRODUCT}/reviews");
        let titles = |path: String| {
            let router = &router;
            async move {
                let page = send(router, "GET", &path, None, None).await;
                assert_eq!(page.status, StatusCode::OK, "{shop} {path}: {}", page.body);
                review_titles(&page.body).join(" | ")
            }
        };

        // The search box comes first, in a form that is a search, which
        // the runtime submits in place (pages_live).
        let all = send(&router, "GET", &reviews, None, None).await;
        let form = r#"<form method="get" class="ss-filter" role="search" data-ss-live="products_reviews""#;
        let search = r#"<div class="field"><label for="product-reviews-list-reviews-q">Search</label><input id="product-reviews-list-reviews-q" name="q" type="search" maxlength="100"></div>"#;
        let first_field = all.body.split_once(form).and_then(|(_, rest)| rest.find(r#"<div class="field">"#).map(|at| &rest[at..]));
        assert!(first_field.is_some_and(|field| field.starts_with(search)), "{shop}: {}", all.body);
        assert_eq!(review_titles(&all.body), ["Sturdy and heavy", "Too heavy", "Rings true"], "{shop}");

        // Every word, in any case, in the title or the body; the box holds
        // the search sent, and a % matches itself.
        assert_eq!(titles(format!("{reviews}?q=sturdy")).await, "Sturdy and heavy | Rings true", "{shop}");
        assert_eq!(titles(format!("{reviews}?q=COYOTES+sturdy")).await, "Sturdy and heavy", "{shop}");
        assert_eq!(titles(format!("{reviews}?q=100%25")).await, "Too heavy", "{shop}");
        assert_eq!(titles(format!("{reviews}?q=1%250")).await, "", "{shop}");
        let held = send(&router, "GET", &format!("{reviews}?q=sturdy"), None, None).await;
        assert!(held.body.contains(r#"name="q" type="search" maxlength="100" value="sturdy">"#), "{shop}: {}", held.body);

        // A search pages as the list does, its links carrying it.
        let mut page = send(&router, "GET", &format!("{reviews}?q=heavy&limit=1"), None, None).await;
        let mut seen = review_titles(&page.body).join(" | ");
        while let Some(next) = link(&page.body, "next") {
            assert!(next.starts_with(&format!("{reviews}?q=heavy&limit=1&cursor=")), "{shop}: {next}");
            page = send(&router, "GET", &next, None, None).await;
            for title in review_titles(&page.body) {
                seen.push_str(" | ");
                seen.push_str(title);
            }
        }
        assert_eq!(seen, "Sturdy and heavy | Too heavy | Rings true", "{shop}");
        assert_eq!(link(&page.body, "first"), Some(format!("{reviews}?q=heavy&limit=1")), "{shop}");
    }
}

/// A shard's request, as Topcoat's runtime sends one when a live list
/// renders again: its arguments in JSON, as the shopper whose session
/// `cookie` holds, if any. No argument here needs escaping.
async fn shard(router: &Router, uri: &str, cookie: Option<&str>, args: &[&str]) -> Response {
    let args: Vec<String> = args
        .iter()
        .map(|arg| {
            assert!(!arg.contains(['"', '\\']), "{arg}");
            format!("\"{arg}\"")
        })
        .collect();
    let mut request = http::Request::builder().method("POST").uri(uri).header(header::CONTENT_TYPE, "application/json");
    if let Some(cookie) = cookie {
        request = request.header(header::COOKIE, cookie);
    }
    respond(router, request.body(Body::from(format!(r#"{{"args": [{}]}}"#, args.join(", ")))).unwrap()).await
}

/// A page rendered again, as Topcoat's runtime asks for it: a POST to the
/// page's URL with the runtime's header and the document's signals, which
/// the runtime's layer turns into the page's GET.
async fn rerun(router: &Router, uri: &str, cookie: &str) -> Response {
    let request = http::Request::builder()
        .method("POST")
        .uri(uri)
        .header(header::COOKIE, cookie)
        .header(header::CONTENT_TYPE, "application/json")
        .header("x-topcoat-runtime", "true")
        .body(Body::from(r#"{"signals": {}}"#))
        .unwrap();
    respond(router, request).await
}

/// The ids of the orders at `pages`, the orders' pages of the shop.
fn order_ids(pages: &[String]) -> Vec<&str> {
    pages.iter().map(|page| page.trim_start_matches("/orders/")).collect()
}

#[tokio::test]
async fn every_page_is_one_document_with_the_runtime() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    for path in ["/reviews", "/orders", "/orders/new", "/sign-in", "/console", "/console/orders", "/console/products"] {
        let page = send(&router, "GET", path, Some(&cookie), None).await;
        assert_eq!(page.status, StatusCode::OK, "{path}: {}", page.body);
        assert!(page.body.starts_with("<!DOCTYPE html>"), "{path}: {}", page.body);
        assert_eq!(page.body.matches("<html").count(), 1, "{path}: {}", page.body);
        // The runtime's script once, in the head, and no other.
        let (head, body) = page.body.split_once("<body>").expect("a body");
        assert_eq!(head.matches(SCRIPT).count(), 1, "{path}: {}", page.body);
        assert_eq!(page.body.matches("<script").count(), 1, "{path}: {}", page.body);
        assert!(body.ends_with("</body></html>"), "{path}: {}", page.body);
    }
    // The console's layout sits inside the document's body.
    let console = send(&router, "GET", "/console/orders", Some(&cookie), None).await;
    let (_, body) = console.body.split_once("<body>").expect("a body");
    assert!(body.starts_with(r#"<div class="ss-pages">"#), "{}", console.body);
}

#[tokio::test]
async fn the_consoles_lists_are_live() {
    for (shop, router) in each_shop("live-orders.db", &[]).await {
        let cookie = sign_in(&router).await;
        let placed = place_orders(&router, &cookie, 3).await;
        let ids = order_ids(&placed);
        let cancelled = send(&router, "POST", &format!("{}/cancel", placed[1]), Some(&cookie), Some("reason=")).await;
        assert_eq!(cancelled.status, StatusCode::SEE_OTHER, "{shop}: {}", cancelled.body);

        // The list renders through its shard, its filter a form the
        // runtime submits in place.
        let list = send(&router, "GET", "/console/orders", Some(&cookie), None).await;
        assert_eq!(list.status, StatusCode::OK, "{shop}: {}", list.body);
        assert!(list.body.contains(r#"::topcoat::shard::start("/console/_live/orders""#), "{shop}: {}", list.body);
        assert!(list.body.contains(r#"data-ss-live="orders""#), "{shop}: {}", list.body);

        // The shard renders the orders a filter finds, without the layout
        // or the document.
        let placed_only = shard(&router, "/console/_live/orders", Some(&cookie), &["statuses=placed", "/console/orders"]).await;
        assert_eq!(placed_only.status, StatusCode::OK, "{shop}: {}", placed_only.body);
        assert_eq!(console_orders(&placed_only.body), [ids[0], ids[2]], "{shop}");
        assert!(!placed_only.body.contains("ss-nav") && !placed_only.body.contains("<html"), "{shop}: {}", placed_only.body);
        assert!(placed_only.body.contains(r#"<input type="checkbox" name="statuses" value="placed" checked="">"#), "{shop}: {}", placed_only.body);

        // And a page, whose links' queries it renders in turn.
        let first = shard(&router, "/console/_live/orders", Some(&cookie), &["limit=1", "/console/orders"]).await;
        assert_eq!(console_orders(&first.body), ids[..1], "{shop}");
        let next = link(&first.body, "next").expect("a next page");
        let query = next.strip_prefix("/console/orders?").expect("the list's query");
        let second = shard(&router, "/console/_live/orders", Some(&cookie), &[query, "/console/orders"]).await;
        assert_eq!(second.status, StatusCode::OK, "{shop}: {}", second.body);
        assert_eq!(console_orders(&second.body), ids[1..2], "{shop}");
        assert_eq!(link(&second.body, "first").as_deref(), Some("/console/orders?limit=1"), "{shop}");

        // A signed-out shard is refused as the list is.
        let signed_out = shard(&router, "/console/_live/orders", None, &["", "/console/orders"]).await;
        assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{shop}: {}", signed_out.body);

        // The runtime renders the page again: its layer makes the POST the
        // page's GET, which the console's controls, registered before it,
        // meet as one.
        let again = rerun(&router, "/console/orders?statuses=placed", &cookie).await;
        assert_eq!(again.status, StatusCode::OK, "{shop}: {}", again.body);
        assert!(again.body.starts_with("<!DOCTYPE html>"), "{shop}: {}", again.body);
        assert_eq!(console_orders(&again.body), [ids[0], ids[2]], "{shop}");
    }
}

#[tokio::test]
async fn the_reviews_shard_is_live() {
    for (shop, router) in each_shop("live-reviews.db", &REVIEWS).await {
        let reviews = format!("/console/products/{PRODUCT}/reviews");
        let list = send(&router, "GET", &reviews, None, None).await;
        assert_eq!(list.status, StatusCode::OK, "{shop}: {}", list.body);
        assert!(list.body.contains(r#"::topcoat::shard::start("/console/_live/products_reviews""#), "{shop}: {}", list.body);

        // The nested list's shard takes the product from its arguments, as
        // the page takes it from its route, and carries the search.
        let live = "/console/_live/products_reviews";
        let sturdy = shard(&router, live, None, &[PRODUCT, "q=sturdy", &reviews]).await;
        assert_eq!(sturdy.status, StatusCode::OK, "{shop}: {}", sturdy.body);
        assert_eq!(review_titles(&sturdy.body), ["Sturdy and heavy", "Rings true"], "{shop}");
        assert!(sturdy.body.contains(r#"name="q" type="search" maxlength="100" value="sturdy">"#), "{shop}: {}", sturdy.body);
        let paged = shard(&router, live, None, &[PRODUCT, "q=heavy&limit=1", &reviews]).await;
        assert_eq!(review_titles(&paged.body), ["Sturdy and heavy"], "{shop}");
        let next = link(&paged.body, "next").expect("a next page");
        assert!(next.starts_with(&format!("{reviews}?q=heavy&limit=1&cursor=")), "{shop}: {next}");
        let query = next.strip_prefix(&format!("{reviews}?")).expect("the list's query");
        let second = shard(&router, live, None, &[PRODUCT, query, &reviews]).await;
        assert_eq!(review_titles(&second.body), ["Too heavy"], "{shop}");
    }
}

#[tokio::test]
async fn without_the_runtime_the_pages_have_no_script() {
    let users = users_in_memory(CONFIG).unwrap();
    add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await.unwrap();
    let router = app(Arc::new(shop_in_memory()), users, None);
    let cookie = sign_in(&router).await;
    place_order(&router, &cookie).await;
    for path in ["/reviews", "/orders", "/console/orders", &format!("/console/products/{PRODUCT}/reviews")] {
        let page = send(&router, "GET", path, Some(&cookie), None).await;
        assert_eq!(page.status, StatusCode::OK, "{path}: {}", page.body);
        assert!(page.body.starts_with("<!DOCTYPE html>"), "{path}: {}", page.body);
        assert!(!page.body.contains("<script"), "{path}: {}", page.body);
    }
    // The console's lists are the GET forms and links they are without the
    // runtime, and a filter loads the page.
    let filtered = send(&router, "GET", "/console/orders?statuses=placed", Some(&cookie), None).await;
    assert!(filtered.body.contains(r#"<form method="get" class="ss-filter" data-ss-live="orders""#), "{}", filtered.body);
    assert_eq!(console_orders(&filtered.body).len(), 1, "{}", filtered.body);
    // No layer renders a page again: the POST is no page's.
    let again = rerun(&router, "/console/orders", &cookie).await;
    assert_eq!(again.status, StatusCode::METHOD_NOT_ALLOWED, "{}", again.body);
}

/// The orders' procedure, called from a page the test adds, which the app's
/// `.discover()` registers as it registers the app's own: with the query's
/// `cursor` and `limit`, it says the ids of the page record's orders and
/// its `next`, or the problem's status and detail.
#[page("/test/orders-procedure")]
async fn orders_procedure(cx: &Cx) -> topcoat::Result<impl View> {
    let query = parts(cx).uri.query().unwrap_or_default().to_owned();
    let value = |name: &str| form_urlencoded::parse(query.as_bytes()).find(|(key, _)| key == name).map(|(_, value)| value.into_owned());
    let args = OrderListOrdersArgsRecord { statuses: None, cursor: value("cursor"), limit: value("limit").and_then(|limit| limit.parse().ok()) };
    let said = match call_order_list_orders(cx, args).await {
        Ok(OrderViewPageRecord { items, next }) => {
            let ids: Vec<String> = items.into_iter().map(|order| order.id).collect();
            format!("{} next={}", ids.join(","), next.unwrap_or_default())
        }
        Err(problem) => format!("{} {}", problem.status, problem.detail),
    };
    Ok(view! { <p id="said">(said)</p> })
}

#[tokio::test]
async fn the_orders_procedure_answers_a_page_record() {
    let router = shop().await;
    let cookie = sign_in(&router).await;
    let placed = place_orders(&router, &cookie, 3).await;
    let ids: Vec<&str> = placed.iter().map(|order| order.trim_start_matches("/orders/")).collect();
    let said = |uri: String, cookie: Option<String>| {
        let router = &router;
        async move {
            let page = send(router, "GET", &uri, cookie.as_deref(), None).await;
            each_between(&page.body, r#"<p id="said">"#, "</p>").first().map(|said| (*said).to_owned()).unwrap_or(page.body)
        }
    };
    let first = said("/test/orders-procedure?limit=2".to_owned(), Some(cookie.clone())).await;
    let (orders, next) = first.split_once(" next=").expect("a page record");
    assert_eq!(orders, ids[..2].join(","));
    assert!(!next.is_empty(), "{first}");
    let second = said(format!("/test/orders-procedure?limit=2&cursor={next}"), Some(cookie.clone())).await;
    assert_eq!(second, format!("{} next=", ids[2]));
    // The page asked for meets the route's rules, and its caller the route's.
    let forged = said("/test/orders-procedure?cursor=e30".to_owned(), Some(cookie.clone())).await;
    assert_eq!(forged, "400 cursor is not one this operation returned");
    let past = said("/test/orders-procedure?limit=101".to_owned(), Some(cookie)).await;
    assert_eq!(past, "400 Invalid query parameter limit: must be at most 100");
    let signed_out = said("/test/orders-procedure".to_owned(), None).await;
    assert_eq!(signed_out, "401 Authentication required");
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
