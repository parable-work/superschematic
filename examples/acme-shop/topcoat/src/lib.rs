//! The acme shop as a Topcoat app over shop-orders. Its pages call the
//! operations in-process through the crate the Topcoat extension writes
//! (`acme_shop_orders_topcoat`), by each route's rules, and take their input
//! through the forms it builds from the schema: a shopper signs in with
//! their email and password, writes a review (`WriteReviewInputForm`),
//! places an order with its lines as rows (`PlaceOrderInputForm`), filters
//! their orders (`OrderListOrdersArgsForm`, a GET form) and cancels one
//! (`OrderCancelOrderArgsForm`). The reviews and the orders render through
//! the display components the crate builds from their types, and the orders
//! a page at a time, with links to the next page and back to the first.
//! An order line's product is a select of the catalog's products, and an
//! order's lines name theirs, both from `listProducts`, which the crate's
//! `references` call (D61). The crate's generated pages serve a console of
//! shop-orders' resources at `/console`, the catalog among them. The JSON API
//! is mounted at `/api` beside the pages, on the same implementations,
//! which keep the shop in memory or in a SQLite file of shop-db's tables,
//! and the shop's users are the core user model's (D50): one session
//! cookie signs the shopper in on both.

use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{Shop, ShopImplementation, implementations};
use acme_shop_orders_topcoat::api::runtime::identity::{LoginInput, Service};
use acme_shop_orders_topcoat::api::runtime::{ApiError, Principal};
use acme_shop_orders_topcoat::api::{
    OrderGetOrderArgs, OrderPlaceOrderArgs, ProductReviewsListReviewsArgs, ProductReviewsWriteReviewArgs, types,
};
use acme_shop_orders_topcoat::forms::{
    Choices, FormErrors, OrderCancelOrderArgsForm, OrderListOrdersArgsForm, PlaceOrderInputForm, Refusal,
    WriteReviewInputForm, order_cancel_order_args_fields, order_list_orders_args_fields, place_order_input_fields,
    write_review_input_fields,
};
use acme_shop_orders_topcoat::records::{OrderViewRecord, ReviewViewRecord};
use acme_shop_orders_topcoat::views::{order_view_detail, order_view_table, review_view_detail};
use acme_shop_orders_topcoat::{IdentityPageAuthenticator, RouterBuilderShopOrdersExt, operations, pages, references};
use serde::Deserialize;
use topcoat::context::{Cx, app_context};
use topcoat::router::content::Form;
use topcoat::router::error::see_other;
use topcoat::router::header::SET_COOKIE;
use topcoat::router::request::parts;
use topcoat::router::response::response_headers;
use topcoat::router::{HeaderValue, Router, RouterBuilderDiscoverExt, StatusCode, page, path_param};
use topcoat::runtime::shard;
use topcoat::view::{Child, View, component, view};

/// The one product the shop sells, at 19.99.
pub const PRODUCT: &str = "6f1c2a3e-4b5d-4e6f-8a9b-0c1d2e3f4a5b";

pub fn product() -> types::IdentityUUID {
    PRODUCT.parse().expect("a UUID")
}

/// A product the shop sells.
pub struct Product {
    pub id: &'static str,
    pub sku: &'static str,
    pub name: &'static str,
    pub price_cents: i64,
}

impl Product {
    pub fn id(&self) -> types::IdentityUUID {
        self.id.parse().expect("a UUID")
    }

    /// The product as `listProducts` lists it, in stock.
    pub fn view(&self) -> types::ProductView {
        types::ProductView {
            id: self.id(),
            sku: self.sku.to_owned(),
            name: self.name.to_owned(),
            price_cents: self.price_cents,
            in_stock: true,
        }
    }
}

/// The shop's catalog, which stocks the shop, in memory (`shop_in_memory`)
/// or in SQLite (`stock_sqlite`). The shop lists it (`listProducts`), which
/// the place-order form offers and an order's lines are named from, and
/// prices an order's lines from it.
pub const CATALOG: &[Product] = &[Product { id: PRODUCT, sku: "anvil", name: "Anvil", price_cents: 1999 }];

/// The shop in memory, its catalog the app's.
pub fn shop_in_memory() -> Shop {
    Shop::with_products(CATALOG.iter().map(Product::view))
}

/// Adds the catalog's products to a SQLite shop, unless an earlier run
/// added them, as a real shop's own flows would: `SqliteShop::place_order`
/// reads a line's price from the product's row.
pub fn stock_sqlite(shop: &SqliteShop) -> Result<(), Box<dyn std::error::Error>> {
    for product in CATALOG {
        shop.add_product(&product.id(), product.sku, product.name, product.price_cents)?;
    }
    Ok(())
}

/// The identity config the app serves with: plain HTTP on a local port, so
/// the session cookie is not `Secure`.
pub const IDENTITY_CONFIG: &str = r#"{"cookie": {"secure": false}}"#;

/// The app: its pages, and shop-orders' JSON API and in-process operations
/// over `shop`, in memory (`shop_in_memory`) or in SQLite
/// (`acme_shop_orders_server::sqlite::SqliteShop`), whose callers are
/// `users`' users. A page's caller is the user whose session the request
/// carries, read by the identity runtime as the JSON API reads it. Topcoat's
/// origin policy runs before that check, so an origin the identity config
/// trusts must be one the router's `OriginPolicy` trusts too; this app
/// trusts none. `.discover()` registers the console the crate generates
/// under `/console`, whose `pages::Settings` name its title and the sign-in
/// page a refused shopper comes back from.
pub fn app<S: ShopImplementation>(shop: Arc<S>, users: Arc<Service>) -> Router {
    let implementations = implementations(shop, Arc::clone(&users));
    let authenticator = IdentityPageAuthenticator::of(&implementations);
    Router::builder()
        .discover()
        .app_context(users)
        .app_context(console_settings())
        .shop_orders(implementations, authenticator)
        .build()
}

/// The console's settings: its title, and the sign-in page, which comes
/// back to the console page it is given as `next`. The console names the
/// products an order's lines refer to itself, from `listProducts`.
fn console_settings() -> pages::Settings {
    pages::Settings::new().title("Acme shop console").sign_in("/sign-in")
}

/// The sign-in form, as the browser sends it: the shopper's email and
/// password, and the page to come back to (`next`), which the sign-in
/// button of a page that refused a signed-out shopper names.
#[derive(Default, Deserialize)]
#[serde(default)]
pub struct SignInForm {
    login: String,
    password: String,
    next: String,
}

/// Signs a shopper in with their email and password: the identity service
/// checks them, after the cross-origin check a cookie login passes, and
/// starts a cookie session, which every page and the JSON API read, then
/// goes back to `next` when it is a page of the app, else to the reviews.
/// A refusal renders the form again with its status, 401 for a wrong
/// password.
#[page(POST "/sign-in")]
async fn sign_in(cx: &Cx, Form(form): Form<SignInForm>) -> topcoat::Result<impl View> {
    let users: &Arc<Service> = app_context(cx);
    let input = LoginInput { login: form.login.clone(), password: form.password, session: "cookie".to_owned() };
    let signed_in = match users.check_cookie_login(parts(cx), &input.session) {
        Ok(()) => users.login(&input).await,
        Err(err) => Err(err),
    };
    let refused = match signed_in {
        Ok(session) => {
            let cookie = users.config().session_cookie(&session.token, users.config().session_ttl());
            response_headers(cx).append(SET_COOKIE, HeaderValue::from_str(&cookie)?);
            let next = if is_local(&form.next) { form.next.as_str() } else { "/reviews" };
            return Err(see_other(next).into());
        }
        Err(err) => err,
    };
    Ok(view! {
        (refused.status)
        sign_in_page(login: form.login, next: form.next, refused: Some(refused.message))
    })
}

/// Whether `next` is a path of this app: a slash, then anything but a second
/// one or a backslash, which a browser reads as another host's address.
fn is_local(next: &str) -> bool {
    next.starts_with('/') && !next.starts_with("//") && !next.starts_with("/\\")
}

/// The sign-in page, which a page's sign-in button opens with the page to
/// come back to in its query (`next`).
#[page("/sign-in")]
async fn show_sign_in(Form(form): Form<SignInForm>) -> topcoat::Result<impl View> {
    Ok(view! { sign_in_page(next: form.next) })
}

/// The sign-in form, with the login as sent, the page to come back to, and
/// why the sign-in was refused.
#[component]
async fn sign_in_page(
    #[default] login: String,
    #[default] next: String,
    #[default] refused: Option<String>,
) -> topcoat::Result<impl View> {
    Ok(view! {
        shop_page(
            title: "Sign in",
            if let Some(message) = refused {
                <p class="form-error">(message)</p>
            }
            <form method="post" action="/sign-in">
                if !next.is_empty() {
                    <input type="hidden" name="next" value=(next)>
                }
                <label for="login">"Email"</label>
                <input id="login" name="login" type="email" required=(true) value=(login)>
                <label for="password">"Password"</label>
                <input id="password" name="password" type="password" required=(true)>
                <button type="submit">"Sign in"</button>
            </form>
        )
    })
}

/// The frame of each page: the links between them, then its heading and
/// its content.
#[component]
async fn shop_page(title: &str, #[default] child: Child<'_>) -> topcoat::Result<impl View> {
    Ok(view! {
        <nav>
            <a href="/reviews">"Reviews"</a>
            " "
            <a href="/orders">"Orders"</a>
            " "
            <a href="/orders/new">"Place an order"</a>
            " "
            <a href="/console">"Console"</a>
        </nav>
        <h1>(title)</h1>
        (child)
    })
}

/// The sign-in button, which opens the sign-in page, which comes back to
/// `next`.
#[component]
async fn sign_in_button(#[into] next: String) -> topcoat::Result<impl View> {
    Ok(view! {
        <form method="get" action="/sign-in" class="sign-in">
            <input type="hidden" name="next" value=(next)>
            <button type="submit">"Sign in"</button>
        </form>
    })
}

/// An operation's refusal in place of what it would have shown: the
/// route's status and message, and the sign-in button when the operation
/// needs a signed-in caller (401).
#[component]
async fn refusal(err: ApiError, #[into] next: String) -> topcoat::Result<impl View> {
    let signed_out = err.status == StatusCode::UNAUTHORIZED;
    Ok(view! {
        (err.status)
        <p class="refusal">(err.message)</p>
        if signed_out {
            sign_in_button(next: next)
        }
    })
}

/// Whether a guard (`operations::can_<operation>`) refused the caller for
/// want of a signed-in one (401), so the page shows the sign-in button.
fn needs_sign_in(admitted: Result<Option<Principal>, ApiError>) -> bool {
    admitted.is_err_and(|err| err.status == StatusCode::UNAUTHORIZED)
}

/// One review, as a shard: the browser can render it again from its record.
/// `review_view_detail` lists its fields, labeled as `ReviewView` declares
/// them and named by its title.
#[shard("/shards/review")]
async fn review_card(review: ReviewViewRecord) -> topcoat::Result<impl View> {
    Ok(view! {
        <article class="review">
            review_view_detail(record: review)
        </article>
    })
}

/// The product's reviews, the first page of them, oldest first, and the
/// form to write one, with the form as sent and its errors after a refused
/// post, and the sign-in button for a shopper the operation would refuse.
#[component]
async fn reviews_page(
    cx: &Cx,
    #[default] form: WriteReviewInputForm,
    #[default] errors: FormErrors,
) -> topcoat::Result<impl View> {
    let args = ProductReviewsListReviewsArgs {
        product_id: product(),
        min_rating: None,
        q: None,
        page: ProductReviewsListReviewsArgs::PAGE.first(),
    };
    let reviews: Vec<ReviewViewRecord> = operations::product_reviews_list_reviews(cx, args)
        .await?
        .items
        .iter()
        .map(ReviewViewRecord::from)
        .collect();
    let empty = reviews.is_empty();
    let signed_out = needs_sign_in(operations::can_product_reviews_write_review(cx).await);
    Ok(view! {
        shop_page(
            title: "Reviews",
            if empty {
                <p>"No reviews yet."</p>
            }
            #[key(review.id.clone())]
            for review in reviews {
                review_card(review: review)
            }
            if signed_out {
                sign_in_button(next: "/reviews")
            }
            <form method="post" action="/reviews">
                write_review_input_fields(form: form, errors: errors)
                <button type="submit">"Post review"</button>
            </form>
        )
    })
}

#[page("/reviews")]
async fn show_reviews() -> topcoat::Result<impl View> {
    Ok(view! { reviews_page() })
}

/// Writes a review from the form: the form parsed by the input type's
/// rules, then the operation called as the signed-in shopper. A refusal of
/// either renders the page again, 422, with the form as sent.
#[page(POST "/reviews")]
async fn post_review(cx: &Cx, Form(form): Form<WriteReviewInputForm>) -> topcoat::Result<impl View> {
    let errors = match form.parse() {
        Ok(input) => {
            let args = ProductReviewsWriteReviewArgs { product_id: product(), input };
            match operations::product_reviews_write_review(cx, args).await {
                Ok(_) => return Err(see_other("/reviews").into()),
                Err(err) => FormErrors::from_api(&err),
            }
        }
        Err(errors) => errors,
    };
    Ok(view! {
        (StatusCode::UNPROCESSABLE_ENTITY)
        reviews_page(form: form, errors: errors)
    })
}

/// The shopper's orders, which need `orders.read`, a page at a time,
/// filtered by the GET form `OrderListOrdersArgsForm` reads from the query,
/// its controls showing the filters in effect. The query's `cursor` and
/// `limit` ask for the page, and no control holds them, so a submitted
/// filter starts at the first page. After the orders, "Next page" links
/// the page after this one, while one follows, and "First page" the first,
/// on a later one, each with the filter and the limit (`orders_href`).
/// `submit` keeps the route's status: a refused filter is 422 with the
/// message at its control, a cursor or a limit the route refuses its 400,
/// the message the form's own, and a refused caller the route's 401, with
/// the sign-in button, or 403, with no links, since no page is theirs.
#[page("/orders")]
async fn show_orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let unfiltered = filter.statuses.is_empty();
    let (status, page, errors) = match filter.submit(cx).await {
        Ok(page) => (StatusCode::OK, Some(page), FormErrors::default()),
        Err(refused) => (refused.status, None, refused.errors),
    };
    let signed_out = status == StatusCode::UNAUTHORIZED;
    let admitted = !signed_out && status != StatusCode::FORBIDDEN;
    let first = (admitted && filter.cursor.is_some()).then(|| orders_href(&filter, None));
    let next = page.as_ref().and_then(|page| page.next.as_ref()).map(|next| orders_href(&filter, Some(next.as_str())));
    Ok(view! {
        (status)
        shop_page(
            title: "Orders",
            if signed_out {
                sign_in_button(next: "/orders")
            }
            <form method=(OrderListOrdersArgsForm::METHOD) action="/orders">
                order_list_orders_args_fields(form: filter, errors: errors)
                <button type="submit">"Filter"</button>
            </form>
            if let Some(page) = page {
                order_list(orders: page.items, unfiltered: unfiltered)
            }
            pager(first: first, next: next)
        )
    })
}

/// The orders page's address for one of its pages: the query the filter
/// holds (`query_pairs`), its statuses and its limit, then, for a page
/// after the first, `cursor`, the `next` of the page before, each name and
/// value percent-encoded.
fn orders_href(filter: &OrderListOrdersArgsForm, cursor: Option<&str>) -> String {
    let mut query = form_urlencoded::Serializer::new(String::new());
    query.extend_pairs(filter.query_pairs());
    if let Some(cursor) = cursor {
        query.append_pair("cursor", cursor);
    }
    let query = query.finish();
    if query.is_empty() { "/orders".to_owned() } else { format!("/orders?{query}") }
}

/// The links between a list's pages: "First page" while the page is a
/// later one, and "Next page" while another follows. A cursor names no
/// page before it, so there is no previous page.
#[component]
async fn pager(first: Option<String>, next: Option<String>) -> topcoat::Result<impl View> {
    Ok(view! {
        if first.is_some() || next.is_some() {
            <nav class="pager" aria-label="Pages">
                if let Some(href) = first {
                    <a rel="first" href=(href)>"First page"</a>
                }
                if let Some(href) = next {
                    <a rel="next" href=(href)>"Next page"</a>
                }
            </nav>
        }
    })
}

/// Orders as `order_view_table` renders them, a column per summary field
/// `OrderView` declares, then a link to each one's page.
#[component]
async fn order_list(orders: Vec<types::OrderView>, unfiltered: bool) -> topcoat::Result<impl View> {
    let links: Vec<(String, String)> = orders
        .iter()
        .map(|order| (format!("/orders/{}", order.id), format!("Order {}", order.id)))
        .collect();
    let rows: Vec<OrderViewRecord> = orders.iter().map(OrderViewRecord::from).collect();
    Ok(view! {
        if rows.is_empty() {
            if unfiltered {
                <p>"No orders yet."</p>
            } else {
                <p>"No orders match the filter."</p>
            }
        } else {
            order_view_table(rows: rows)
            <ul class="order-links">
                for (href, label) in links {
                    <li><a href=(href)>(label)</a></li>
                }
            </ul>
        }
    })
}

path_param!(id: types::IdentityUUID, error = not_found);

/// One order, as `order_view_detail` renders it, which needs `orders.read`,
/// each line's product by its name, which `references::order_view_labels`
/// looks up through `listProducts`, with the route's status when the call
/// is refused (401, 403, 404). While the order is placed, the form that
/// cancels it: `cancel` as sent and its errors after a refused cancel,
/// which keeps the form, and its form-level message, on the page even when
/// the order is no longer placed.
#[component]
async fn order_page(
    cx: &Cx,
    id: types::IdentityUUID,
    #[default] cancel: Option<OrderCancelOrderArgsForm>,
    #[default] errors: FormErrors,
) -> topcoat::Result<impl View> {
    let path = format!("/orders/{id}");
    let order = operations::order_get_order(cx, OrderGetOrderArgs { id }).await;
    let cancellable =
        !errors.is_empty() || order.as_ref().is_ok_and(|order| order.status == types::OrderStatus::Placed);
    let cancel = cancel.unwrap_or_else(|| OrderCancelOrderArgsForm::new(id));
    let order = order.map(OrderViewRecord::from);
    let choices = match &order {
        Ok(record) => references::order_view_labels(cx, std::slice::from_ref(record), Choices::new()).await,
        Err(_) => Choices::new(),
    };
    Ok(view! {
        shop_page(
            title: "Order",
            match order {
                Ok(record) => {
                    order_view_detail(record: record, choices: choices)
                    if cancellable {
                        <form method=(OrderCancelOrderArgsForm::METHOD) action=(format!("{path}/cancel"))>
                            order_cancel_order_args_fields(form: cancel, errors: errors)
                            <button type="submit">"Cancel order"</button>
                        </form>
                    }
                },
                Err(err) => refusal(err: err, next: path),
            }
        )
    })
}

#[page("/orders/{id}")]
async fn show_order(cx: &Cx) -> topcoat::Result<impl View> {
    let id = *path_param::<Id>(cx)?;
    Ok(view! { order_page(id: id) })
}

/// Cancels the order from its form, by the id in the route rather than the
/// form's hidden input, then back to its page (303). A refusal renders the
/// order's page again with the form as sent and each error at its control,
/// and the status `submit` keeps: 422 for an over-long reason, at the
/// reason, and the operation's 409 for an order that is not placed, as the
/// form's own message.
#[page(POST "/orders/{id}/cancel")]
async fn cancel_order(cx: &Cx, Form(form): Form<OrderCancelOrderArgsForm>) -> topcoat::Result<impl View> {
    let id = *path_param::<Id>(cx)?;
    let form = OrderCancelOrderArgsForm { id: Some(id.to_string()), ..form };
    let refused = match form.submit(cx).await {
        Ok(order) => return Err(see_other(format!("/orders/{}", order.id)).into()),
        Err(refused) => refused,
    };
    Ok(view! {
        (refused.status)
        order_page(id: id, cancel: Some(form), errors: refused.errors)
    })
}

/// The form that places an order: a row per line, each line's product a
/// select of the catalog's, and the shipping address. The sign-in button
/// comes first for a shopper the operation would refuse. The products are
/// `listProducts`' first page, which `references::place_order_input_form_choices`
/// gives every line's `productId` (`lines.productId`), each by its name. A
/// choice's value is the product's id as the API sends it, the compact text
/// `to_string()` gives, which a record holds.
#[component]
async fn place_order_page(
    cx: &Cx,
    #[default(PlaceOrderInputForm::new())] form: PlaceOrderInputForm,
    #[default] errors: FormErrors,
) -> topcoat::Result<impl View> {
    let signed_out = needs_sign_in(operations::can_order_place_order(cx).await);
    let choices = references::place_order_input_form_choices(cx, Choices::new()).await;
    Ok(view! {
        shop_page(
            title: "Place an order",
            if signed_out {
                sign_in_button(next: "/orders/new")
            }
            <form method="post" action="/orders/new">
                place_order_input_fields(form: form, errors: errors, choices: choices)
                <button type="submit">"Place order"</button>
            </form>
        )
    })
}

#[page("/orders/new")]
async fn new_order() -> topcoat::Result<impl View> {
    Ok(view! { place_order_page() })
}

/// Places an order from the form. A row button (`_action`) adds or removes
/// a line and renders the form again, 200, without placing it. Otherwise
/// the form is parsed by `PlaceOrderInput`'s rules and the operation
/// called: the new order's page (303), or the form again as sent, each
/// error at its control (`lines[0].quantity`), with a `Refusal`'s status:
/// 422 for a form the rules refuse, the route's for the operation's
/// refusal, 401 for a shopper signed out.
#[page(POST "/orders/new")]
async fn place_order(cx: &Cx, Form(form): Form<PlaceOrderInputForm>) -> topcoat::Result<impl View> {
    let mut form = form;
    let (status, errors) = if form.apply_action() {
        (StatusCode::OK, FormErrors::default())
    } else {
        let refused = match form.parse() {
            Ok(input) => match operations::order_place_order(cx, OrderPlaceOrderArgs { input }).await {
                Ok(order) => return Err(see_other(format!("/orders/{}", order.id)).into()),
                Err(err) => Refusal::from_api(&err),
            },
            Err(errors) => Refusal::from(errors),
        };
        (refused.status, refused.errors)
    };
    Ok(view! {
        (status)
        place_order_page(form: form, errors: errors)
    })
}
