//! The back office: what staff do in the console the crate generates,
//! through `Router::handle`, as a browser would, over a SQLite file of
//! shop-db's tables and shop-db's engine on a SQLite file of its own.
//!
//! `seed` writes what the stack's shop-orders and acmepay's adapter would:
//! an Inventory of the anvil with stock, a CollectionPoint, a Checkout of
//! Grace's that held two anvils and a pickup slot, was paid at acmepay and
//! placed, and a Refund of it Grace asked for. Ada, staff with the back
//! office's roles, then works through the console's engine pages:
//! - the engine's index lists the four types she may read, and nobody
//!   else's console offers them;
//! - she receives stock through the Stock panel's form, and its levels and
//!   its movements show it;
//! - the collection point's Calendar panel shows its week and the
//!   checkout's booking;
//! - the checkout's Holds and Payments panels show every amount as money,
//!   and point to the Refund type for its refunds;
//! - the refund is refused (409, `approvals_missing`) while one sign-off
//!   is all it has; Ada adds Katherine through the users pages and grants
//!   her the refunds role, Katherine signs off, and the move to approved
//!   asks the checkout for the money back, which acmepay's adapter then
//!   reports paid, as the refund's Payments panel shows;
//! - the users and roles pages list the shop's people and the roles the
//!   seed made, to Ada alone.
//!
//! A signed-out caller's engine page links to the app's own `/sign-in`.
//! Every page passes the extension's Enter rule
//! (extensions/topcoat/testdata/enter.rs).

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{SHOPPER_PERMISSIONS, add_shopper, add_staff, users_at};
use acme_shop_orders_topcoat::api::runtime::identity::Service;
use acme_shop_topcoat::{BACK_OFFICE_ROLES, Engine, add_back_office, app, open_engine, product, stock_sqlite};
use serde_json::{Value, json};
use superschematic_engine::{CreateOptions, InstanceRecord, Principal, PrincipalService, WriteOptions};
use topcoat::router::{Body, Router, StatusCode, header, to_bytes};

#[path = "../../../../extensions/topcoat/testdata/enter.rs"]
mod enter;

/// The app's identity config at a low password cost, so a test hashes
/// fast.
const CONFIG: &str = r#"{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;

/// A user's login, name and password.
type Person = (&'static str, &'static str, &'static str);

/// The shopper whose checkout the seed writes.
const GRACE: Person = ("grace@example.com", "Grace Hopper", "grace's password");

/// Staff with the back office's roles.
const ADA: Person = ("ada@example.com", "Ada Lovelace", "ada's password");

/// The second member of staff to sign a refund off, whom Ada adds.
const KATHERINE: Person = ("katherine@example.com", "Katherine Johnson", "katherine's password");

/// The engine's namespace, shop-db's.
const NAMESPACE: &str = "shop-db";

/// The provider whose adapter records payments.
const PROVIDER: &str = "acmepay";

/// The console's engine pages.
const ENGINE: &str = "/console/engine";

/// What the seed wrote: the ids of the anvil's Inventory, the collection
/// point, the checkout and the refund.
struct Seeded {
    inventory: String,
    counter: String,
    checkout: String,
    refund: String,
}

/// The app, its users and its engine.
struct BackOffice {
    router: Router,
    users: Arc<Service>,
    engine: Arc<Engine>,
    seeded: Seeded,
}

/// The back office: the app over a new SQLite file of shop-db's tables,
/// holding Grace, a shopper, and Ada, staff with the back office's roles,
/// and shop-db's engine on a SQLite file, seeded.
async fn back_office(name: &str) -> BackOffice {
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
    let grace = users.store().find_login(GRACE.0).await.unwrap().user.id;
    let engine = open_engine(temp_path(&format!("{name}.engine.db"))).expect("shop-db's engine opens");
    let seeded = seed(&engine, &grace);
    let router = app(Arc::new(shop), Arc::clone(&users), Arc::clone(&engine), None);
    BackOffice { router, users, engine, seeded }
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

/// A call shop-orders makes in-process for `subject`, holding
/// `permissions`: shop-db's engine policy lets shop-orders read and write
/// every type.
fn shop_orders(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().copied()).with_service(PrincipalService {
        deployable: "shop-orders".to_owned(),
        serves: vec!["shop-orders".to_owned()],
        subject: "shop-orders".to_owned(),
        stands_in: false,
    })
}

/// acmepay's adapter, which shop-orders' webhook and settle job run as: it
/// holds `payments.record`.
fn adapter() -> Principal {
    shop_orders("acmepay-adapter", &["payments.record"])
}

fn create(engine: &Engine, who: &Principal, schema: &str, id: Option<&str>, data: Value, behaviors: Option<Value>) -> InstanceRecord {
    let options = CreateOptions { namespace: Some(NAMESPACE), id, behaviors: behaviors.as_ref() };
    engine.instances().create(who, schema, &data, options).unwrap_or_else(|err| panic!("create {schema}: {err}"))
}

fn invoke(engine: &Engine, who: &Principal, schema: &str, id: &str, operation: &str, params: Value) -> Value {
    let options = WriteOptions { namespace: Some(NAMESPACE), ..WriteOptions::default() };
    engine
        .instances()
        .invoke(who, schema, id, operation, &params, options)
        .unwrap_or_else(|err| panic!("{schema}.{operation}: {err}"))
}

/// Now, in epoch milliseconds.
fn now_ms() -> i64 {
    i64::try_from(SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis()).unwrap()
}

/// An instant in epoch milliseconds as RFC 3339 in UTC, as a
/// `Temporal.DateTime` holds it.
fn rfc3339(ms: i64) -> String {
    let (days, rest) = (ms.div_euclid(86_400_000), ms.rem_euclid(86_400_000) / 1000);
    // The civil date of a day count since 1970-01-01, by Howard Hinnant's
    // days_from_civil inverse.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!("{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z", rest / 3600, rest / 60 % 60, rest % 60)
}

/// Writes, as the stack's shop-stock, shop-orders and acmepay's adapter
/// would, what the back office then keeps:
/// - the anvil's Inventory, at 19.99 and low at 3, with 10 received;
/// - the front counter, a CollectionPoint;
/// - Grace's Checkout of two anvils, collected at the counter's first free
///   slot: held, which holds the units and the slot, paid at acmepay
///   (39.98, captured), and placed, which commits what it holds;
/// - a Refund of 10.00 of it, which Grace asked for.
fn seed(engine: &Engine, grace: &str) -> Seeded {
    // An Inventory's id is its product's, as an Identity.UUID writes it,
    // which a checkout's lines name.
    let inventory = product().to_string();
    let stock = shop_orders("shop-stock", &["stock.write"]);
    create(engine, &stock, "Inventory", Some(&inventory), json!({"sku": "anvil", "priceCents": 1999, "reorderAt": 3}), None);
    invoke(engine, &stock, "Inventory", &inventory, "receiveStock", json!({"quantity": 10, "reason": "Opening stock"}));

    let manager = shop_orders("shop-orders", &["calendar.manage"]);
    let counter = create(engine, &manager, "CollectionPoint", None, json!({"name": "Front counter"}), None).id;
    let start = now_ms();
    let free = engine
        .instances()
        .invoke_schema(
            &manager,
            "CollectionPoint",
            "findAvailability",
            &json!({"startMs": start, "endMs": start + 14 * 86_400_000, "durationMs": 900_000, "ids": [counter], "limit": 1}),
            Some(NAMESPACE),
        )
        .expect("findAvailability");
    let slot = free["slots"][0]["startMs"].as_i64().unwrap_or_else(|| panic!("no free slot: {free}"));

    // shop-orders acts for Grace, so she is the checkout's and the refund's
    // creator, and neither Ada nor Katherine is.
    let shopper = shop_orders(grace, &SHOPPER_PERMISSIONS);
    let address = json!({"recipient": "Grace Hopper", "line1": "1 Navy Yard", "city": "London", "postcode": "SE1 9GU", "country": "GB"});
    let lines = json!([{"inventoryId": inventory, "quantity": 2}]);
    let data = json!({"customerId": grace, "lines": lines, "pickupAt": rfc3339(slot), "address": address});
    let checkout = create(engine, &shopper, "Checkout", None, data, Some(json!({"Links": {"counter": counter}}))).id;
    invoke(engine, &shopper, "Checkout", &checkout, "transition", json!({"to": "held"}));
    let opened = invoke(engine, &shopper, "Checkout", &checkout, "openPayment", json!({"provider": PROVIDER, "idempotencyKey": "pay-1"}));
    let paid = json!({
        "provider": PROVIDER, "eventId": "evt-paid", "kind": "captured", "amount": 3998, "currency": "GBP",
        "at": now_ms(), "attempt": opened["attempt"],
    });
    invoke(engine, &adapter(), "Checkout", &checkout, "recordPaymentEvent", paid);
    invoke(engine, &shopper, "Checkout", &checkout, "transition", json!({"to": "placed"}));

    let data = json!({"amountCents": 1000, "reason": "The anvil came dented", "requestedBy": grace});
    let refund = create(engine, &shopper, "Refund", None, data, Some(json!({"Links": {"order": checkout}}))).id;
    Seeded { inventory, counter, checkout, refund }
}

/// Reports, as acmepay's adapter, that acmepay paid every refund the
/// checkout asked it for: the settle job's work, which this app has no
/// adapter to do.
fn acmepay_pays_the_refunds(engine: &Engine, checkout: &str) -> usize {
    let actions = engine
        .instances()
        .invoke_schema(&adapter(), "Checkout", "listPaymentActions", &json!({"provider": PROVIDER}), Some(NAMESPACE))
        .expect("listPaymentActions");
    let refunds: Vec<&Value> =
        actions["items"].as_array().into_iter().flatten().filter(|action| action["kind"] == "refund" && action["id"] == checkout).collect();
    for (n, action) in refunds.iter().enumerate() {
        let event = json!({
            "provider": PROVIDER, "eventId": format!("evt-refund-{n}"), "kind": "refunded", "amount": action["amount"],
            "currency": "GBP", "at": now_ms(), "refund": action["refund"],
        });
        invoke(engine, &adapter(), "Checkout", checkout, "recordPaymentEvent", event);
    }
    refunds.len()
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
    let headers = response.headers().clone();
    let header_text = |name| headers.get(name).map(|value: &http::HeaderValue| value.to_str().unwrap().to_owned());
    let location = header_text(header::LOCATION);
    let cookie = header_text(header::SET_COOKIE).map(|set| set.split(';').next().unwrap().to_owned());
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &body);
    Response { status, location, cookie, body }
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

/// Each text of `body` between `start` and the next `end`.
fn each_between<'a>(body: &'a str, start: &str, end: &str) -> Vec<&'a str> {
    body.split(start).skip(1).filter_map(|rest| rest.split_once(end).map(|(text, _)| text)).collect()
}

/// The `dd` of the field `field` of a panel's `dl`.
fn field<'a>(body: &'a str, field: &str) -> &'a str {
    let start = format!(r#"<div data-field="{field}">"#);
    let found = each_between(body, &start, "</div>");
    let cell = found.first().unwrap_or_else(|| panic!("no {field}: {body}"));
    each_between(cell, "<dd>", "</dd>").first().copied().unwrap_or_default()
}

/// The section of the panel of `behavior` on an instance's page.
fn panel<'a>(body: &'a str, behavior: &str) -> &'a str {
    let start = format!(r#"<section class="ss-panel" data-behavior="{behavior}">"#);
    each_between(body, &start, "</section>").first().copied().unwrap_or_else(|| panic!("no {behavior} panel: {body}"))
}

/// An operation's route on an instance.
fn operation(schema: &str, id: &str, operation: &str) -> String {
    format!("{ENGINE}/{schema}/{id}/operations/{operation}")
}

/// The instance's page.
fn instance(schema: &str, id: &str) -> String {
    format!("{ENGINE}/{schema}/{id}")
}

/// The sequence the instance's page's forms send back (`expectedSeq`).
fn seq(body: &str) -> String {
    each_between(body, r#"<input type="hidden" name="expectedSeq" value=""#, "\"").first().expect("a write's form").to_string()
}

#[tokio::test]
async fn the_engine_pages_list_what_staff_may_read() {
    let BackOffice { router, seeded, .. } = back_office("index").await;

    // Signed out, the engine's pages ask the caller to sign in at the
    // app's own page, and come back.
    let signed_out = send(&router, "GET", ENGINE, None, None).await;
    assert_eq!(signed_out.status, StatusCode::UNAUTHORIZED, "{}", signed_out.body);
    assert!(signed_out.body.contains(r#"href="/sign-in?next=%2Fconsole%2Fengine""#), "{}", signed_out.body);
    assert!(!signed_out.body.contains(r#"href="/console/sign-in"#), "{}", signed_out.body);

    // A shopper may read none of the engine's types: no Engine in the nav,
    // and an instance is forbidden.
    let grace = sign_in(&router, GRACE).await;
    let index = send(&router, "GET", "/console", Some(&grace), None).await;
    assert!(!index.body.contains(ENGINE), "{}", index.body);
    let refused = send(&router, "GET", &instance("Inventory", &seeded.inventory), Some(&grace), None).await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.body);

    // Ada reads all four, which the nav and the console's index link.
    let ada = sign_in(&router, ADA).await;
    let index = send(&router, "GET", "/console", Some(&ada), None).await;
    assert!(index.body.contains(r#"<li><a href="/console/engine">Engine</a></li>"#), "{}", index.body);
    let engine = send(&router, "GET", ENGINE, Some(&ada), None).await;
    assert_eq!(engine.status, StatusCode::OK, "{}", engine.body);
    for schema in ["Checkout", "CollectionPoint", "Inventory", "Refund"] {
        assert!(engine.body.contains(&format!(r#"href="/console/engine/{schema}""#)), "{schema}: {}", engine.body);
    }

    // Each type's list links its instances.
    for (schema, id) in [("Inventory", seeded.inventory.as_str()), ("CollectionPoint", seeded.counter.as_str()), ("Checkout", seeded.checkout.as_str()), ("Refund", seeded.refund.as_str())] {
        let list = send(&router, "GET", &format!("{ENGINE}/{schema}"), Some(&ada), None).await;
        assert_eq!(list.status, StatusCode::OK, "{schema}: {}", list.body);
        assert!(list.body.contains(&format!(r#"href="{}""#, instance(schema, id))), "{schema}: {}", list.body);
    }
}

#[tokio::test]
async fn stock_is_received_through_the_console() {
    let BackOffice { router, seeded, .. } = back_office("stock").await;
    let anvil = seeded.inventory.as_str();
    let ada = sign_in(&router, ADA).await;
    let page = send(&router, "GET", &instance("Inventory", anvil), Some(&ada), None).await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.body);
    // Its own price is money, in the currency its @display names.
    assert_eq!(field(&page.body, "priceCents"), r#"<data value="1999">GBP 19.99</data>"#, "{}", page.body);
    // 10 received, 2 sold to Grace's checkout, which committed them.
    let stock = panel(&page.body, "Stock");
    assert_eq!(field(stock, "Stock.onHand"), "8", "{stock}");
    assert_eq!(field(stock, "Stock.held"), "0", "{stock}");
    assert!(stock.contains(&format!(r#"action="{}""#, operation("Inventory", anvil, "receiveStock"))), "{stock}");

    // A delivery of 5, through the panel's form.
    let delivery = form(&[("quantity", "5"), ("reason", "Delivery from the forge"), ("reference", "PO-17")]);
    let received = send(&router, "POST", &operation("Inventory", anvil, "receiveStock"), Some(&ada), Some(&delivery)).await;
    assert_eq!(received.status, StatusCode::SEE_OTHER, "{}", received.body);
    assert_eq!(received.location.as_deref(), Some(instance("Inventory", anvil).as_str()));
    let page = send(&router, "GET", &instance("Inventory", anvil), Some(&ada), None).await;
    let stock = panel(&page.body, "Stock");
    assert_eq!(field(stock, "Stock.onHand"), "13", "{stock}");
    assert_eq!(field(stock, "Stock.available"), "13", "{stock}");
    // The movements, newest first: the delivery, the sale, the opening stock.
    assert!(stock.contains("Delivery from the forge"), "{stock}");
    let delivery_at = stock.find("Delivery from the forge").unwrap();
    let opening_at = stock.find("Opening stock").expect("the opening stock's movement");
    assert!(delivery_at < opening_at, "{stock}");

    // A count below what the form allows is refused at its control.
    let refused = send(&router, "POST", &operation("Inventory", anvil, "receiveStock"), Some(&ada), Some(&form(&[("quantity", "many"), ("reason", "x")]))).await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.body);

    // Grace may not receive stock.
    let grace = sign_in(&router, GRACE).await;
    let refused = send(&router, "POST", &operation("Inventory", anvil, "receiveStock"), Some(&grace), Some(&delivery)).await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.body);
}

#[tokio::test]
async fn a_collection_point_shows_its_week_and_its_bookings() {
    let BackOffice { router, seeded, .. } = back_office("calendar").await;
    let ada = sign_in(&router, ADA).await;
    let page = send(&router, "GET", &instance("CollectionPoint", &seeded.counter), Some(&ada), None).await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.body);
    let calendar = panel(&page.body, "Calendar");
    assert!(calendar.contains("Europe/London"), "{calendar}");
    assert!(calendar.contains(r#"data-field="Calendar.hours""#), "{calendar}");
    // The checkout's pickup slot is booked within the week the panel
    // shows by default.
    assert!(!calendar.contains("No bookings."), "{calendar}");
    assert!(calendar.contains(&seeded.checkout), "{calendar}");
}

#[tokio::test]
async fn a_checkout_shows_its_holds_and_payment_as_money() {
    let BackOffice { router, seeded, .. } = back_office("checkout").await;
    let ada = sign_in(&router, ADA).await;
    let page = send(&router, "GET", &instance("Checkout", &seeded.checkout), Some(&ada), None).await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.body);

    // Two anvils at 19.99, and the pickup slot, which has no price.
    let holds = panel(&page.body, "Holds");
    assert_eq!(field(holds, "Holds.total"), r#"<data value="3998">GBP 39.98</data>"#, "{holds}");
    assert!(holds.contains(r#"<data value="1999">GBP 19.99</data>"#), "{holds}");

    let payments = panel(&page.body, "Payments");
    assert_eq!(field(payments, "Payments.captured"), r#"<data value="3998">GBP 39.98</data>"#, "{payments}");
    assert_eq!(field(payments, "Payments.state"), "paid", "{payments}");
    // Its refunds go through the Refund type's sign-offs, not a form here.
    assert!(payments.contains(r#"<a href="/console/engine/Refund">Refund</a>"#), "{payments}");
    assert!(!page.body.contains(&operation("Checkout", &seeded.checkout, "refundPayment")), "{}", page.body);
}

#[tokio::test]
async fn a_refund_is_refused_until_two_staff_sign_it_off_then_paid() {
    let BackOffice { router, users, engine, seeded } = back_office("refund").await;
    let refund = instance("Refund", &seeded.refund);
    let ada = sign_in(&router, ADA).await;
    let page = send(&router, "GET", &refund, Some(&ada), None).await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.body);
    assert!(panel(&page.body, "Workflow").contains(r#"data-state="requested""#), "{}", page.body);
    let approvals = panel(&page.body, "Approvals");
    assert!(approvals.contains("0 of 2 sign-offs"), "{approvals}");
    // Its own amount is money, in the currency its @display names.
    assert_eq!(field(&page.body, "amountCents"), r#"<data value="1000">GBP 10.00</data>"#, "{}", page.body);
    // The refund's Payments links the checkout it refunds, and its amount
    // is money.
    let payments = panel(&page.body, "Payments");
    assert!(payments.contains(&format!(r#"<a href="{}">"#, instance("Checkout", &seeded.checkout))), "{payments}");
    assert_eq!(field(payments, "Payments.due"), r#"<data value="1000">GBP 10.00</data>"#, "{payments}");

    // Ada signs it off.
    let signed = send(&router, "POST", &operation("Refund", &seeded.refund, "signOff"), Some(&ada), Some(&form(&[("expectedSeq", &seq(&page.body)), ("state", "approved"), ("note", "Photos show the dent")]))).await;
    assert_eq!(signed.status, StatusCode::SEE_OTHER, "{}", signed.body);
    let page = send(&router, "GET", &refund, Some(&ada), None).await;
    assert!(panel(&page.body, "Approvals").contains("1 of 2 sign-offs"), "{}", page.body);

    // One sign-off of two: the move to approved is refused, and the page
    // says why, with the veto's code.
    let approve = |seq: String| form(&[("expectedSeq", &seq), ("to", "approved")]);
    let refused = send(&router, "POST", &operation("Refund", &seeded.refund, "transition"), Some(&ada), Some(&approve(seq(&page.body)))).await;
    assert_eq!(refused.status, StatusCode::CONFLICT, "{}", refused.body);
    assert!(refused.body.contains("approvals_missing"), "{}", refused.body);

    // Ada adds Katherine through the users pages, and grants her the
    // refunds role.
    let (login, name, password) = KATHERINE;
    let added = send(&router, "POST", "/console/users/new", Some(&ada), Some(&form(&[("login", login), ("name", name), ("password", password)]))).await;
    assert_eq!(added.status, StatusCode::SEE_OTHER, "{}", added.body);
    let katherine_page = added.location.expect("Katherine's page");
    let role = users.store().list_roles().await.unwrap().into_iter().find(|role| role.name == "refunds").expect("the refunds role");
    let user = send(&router, "GET", &katherine_page, Some(&ada), None).await;
    assert!(user.body.contains(&format!(r#"<option value="{}">refunds</option>"#, role.id)), "{}", user.body);
    let granted = send(&router, "POST", &format!("{katherine_page}/grant"), Some(&ada), Some(&form(&[("role", &role.id)]))).await;
    assert_eq!(granted.status, StatusCode::SEE_OTHER, "{}", granted.body);

    // Katherine signs it off too, and Ada moves it to approved, which asks
    // the checkout for the money back: the refund waits on acmepay.
    let katherine = sign_in(&router, KATHERINE).await;
    let page = send(&router, "GET", &refund, Some(&katherine), None).await;
    assert!(panel(&page.body, "Approvals").contains(r#"class="ss-sign-off""#), "{}", page.body);
    let signed = send(&router, "POST", &operation("Refund", &seeded.refund, "signOff"), Some(&katherine), Some(&form(&[("expectedSeq", &seq(&page.body)), ("state", "approved"), ("note", "")]))).await;
    assert_eq!(signed.status, StatusCode::SEE_OTHER, "{}", signed.body);
    let page = send(&router, "GET", &refund, Some(&ada), None).await;
    assert!(panel(&page.body, "Approvals").contains("2 of 2 sign-offs"), "{}", page.body);
    let approved = send(&router, "POST", &operation("Refund", &seeded.refund, "transition"), Some(&ada), Some(&approve(seq(&page.body)))).await;
    assert_eq!(approved.status, StatusCode::SEE_OTHER, "{}", approved.body);
    let page = send(&router, "GET", &refund, Some(&ada), None).await;
    assert!(panel(&page.body, "Workflow").contains(r#"data-state="approved""#), "{}", page.body);
    assert_eq!(field(panel(&page.body, "Payments"), "Payments.state"), "pending", "{}", page.body);

    // acmepay pays it, as its adapter reports to the checkout, and both
    // pages show the money back.
    assert_eq!(acmepay_pays_the_refunds(&engine, &seeded.checkout), 1);
    let page = send(&router, "GET", &refund, Some(&ada), None).await;
    let payments = panel(&page.body, "Payments");
    assert_eq!(field(payments, "Payments.state"), "refunded", "{payments}");
    assert_eq!(field(payments, "Payments.refunded"), r#"<data value="1000">GBP 10.00</data>"#, "{payments}");
    let checkout = send(&router, "GET", &instance("Checkout", &seeded.checkout), Some(&ada), None).await;
    let payments = panel(&checkout.body, "Payments");
    assert_eq!(field(payments, "Payments.refunded"), r#"<data value="1000">GBP 10.00</data>"#, "{payments}");
    assert_eq!(field(payments, "Payments.state"), "partiallyRefunded", "{payments}");
}

#[tokio::test]
async fn staff_administer_the_users_and_roles() {
    let BackOffice { router, .. } = back_office("people").await;
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
