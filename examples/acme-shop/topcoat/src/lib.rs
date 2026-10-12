//! The acme shop as a Topcoat app over shop-orders. Its pages call the
//! operations in-process through the crate the Topcoat extension writes
//! (`acme_shop_orders_topcoat`), by each route's rules, and take their input
//! through the forms it builds from the schema: a shopper signs in with
//! their email and password, writes a review (`WriteReviewInputForm`),
//! places an order with its lines as rows (`PlaceOrderInputForm`), filters
//! their orders (`OrderListOrdersArgsForm`, a GET form) and cancels one
//! (`OrderCancelOrderArgsForm`). The reviews and the orders render through
//! the display components the crate builds from their types. The JSON API
//! is mounted at `/api` beside the pages, on the same implementations,
//! which keep the shop in memory or in a SQLite file of shop-db's tables,
//! and the shop's users are the core user model's (D50): one session
//! cookie signs the shopper in on both.

use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{Shop, implementations};
use acme_shop_orders_topcoat::api::runtime::identity::{LoginInput, Service};
use acme_shop_orders_topcoat::api::runtime::{ApiError, Principal};
use acme_shop_orders_topcoat::api::{
    OrderGetOrderArgs, OrderImplementation, OrderPlaceOrderArgs, ProductReviewsImplementation,
    ProductReviewsListReviewsArgs, ProductReviewsWriteReviewArgs, types,
};
use acme_shop_orders_topcoat::forms::{
    Choices, FormErrors, OrderCancelOrderArgsForm, OrderListOrdersArgsForm, PlaceOrderInputForm, WriteReviewInputForm,
    order_cancel_order_args_fields, order_list_orders_args_fields, place_order_input_fields, write_review_input_fields,
};
use acme_shop_orders_topcoat::records::{OrderViewRecord, ReviewViewRecord};
use acme_shop_orders_topcoat::views::{order_view_detail, order_view_table, review_view_detail};
use acme_shop_orders_topcoat::{IdentityPageAuthenticator, RouterBuilderShopOrdersExt, operations};
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
}

/// The shop's catalog, which the place-order form offers. The shop holds
/// it too, in memory (`shop_in_memory`) or in SQLite (`stock_sqlite`), and
/// prices an order's lines from it.
pub const CATALOG: &[Product] = &[Product { id: PRODUCT, sku: "anvil", name: "Anvil", price_cents: 1999 }];

/// The shop in memory, its catalog the app's.
pub fn shop_in_memory() -> Shop {
    Shop::with_prices(CATALOG.iter().map(|product| (product.id(), product.price_cents)))
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
/// trusts none.
pub fn app<S: OrderImplementation + ProductReviewsImplementation>(shop: Arc<S>, users: Arc<Service>) -> Router {
    let implementations = implementations(shop, Arc::clone(&users));
    let pages = IdentityPageAuthenticator::of(&implementations);
    Router::builder()
        .discover()
        .app_context(users)
        .shop_orders(implementations, pages)
        .build()
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

/// The product's reviews and the form to write one, with the form as sent
/// and its errors after a refused post, and the sign-in button for a
/// shopper the operation would refuse.
#[component]
async fn reviews_page(
    cx: &Cx,
    #[default] form: WriteReviewInputForm,
    #[default] errors: FormErrors,
) -> topcoat::Result<impl View> {
    let args = ProductReviewsListReviewsArgs { product_id: product(), min_rating: None };
    let reviews: Vec<ReviewViewRecord> = operations::product_reviews_list_reviews(cx, args)
        .await?
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

/// The shopper's orders, which need `orders.read`, filtered by the GET form
/// `OrderListOrdersArgsForm` reads from the query, its controls showing the
/// filters in effect. The page parses the filter and makes the call itself,
/// so a refused filter is 422 at its control and a refused caller the
/// route's 401 or 403.
#[page("/orders")]
async fn show_orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let unfiltered = filter == OrderListOrdersArgsForm::new();
    let (errors, listed) = match filter.parse() {
        Ok(args) => (FormErrors::default(), Some(operations::order_list_orders(cx, args).await)),
        Err(errors) => (errors, None),
    };
    Ok(view! {
        shop_page(
            title: "Orders",
            <form method=(OrderListOrdersArgsForm::METHOD) action="/orders">
                order_list_orders_args_fields(form: filter, errors: errors)
                <button type="submit">"Filter"</button>
            </form>
            match listed {
                Some(Ok(orders)) => order_list(orders: orders, unfiltered: unfiltered),
                Some(Err(err)) => refusal(err: err, next: "/orders"),
                None => (StatusCode::UNPROCESSABLE_ENTITY),
            }
        )
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
/// with the route's status when the call is refused (401, 403, 404). While
/// the order is placed, the form that cancels it: `cancel` as sent and its
/// errors after a refused cancel, which keeps the form, and its form-level
/// message, on the page even when the order is no longer placed.
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
    Ok(view! {
        shop_page(
            title: "Order",
            match order {
                Ok(order) => {
                    order_view_detail(record: OrderViewRecord::from(order))
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
/// form's hidden input, then back to its page (303). A refusal, the form's
/// or the operation's, renders the order's page again, 422, with the form
/// as sent and each error at its control: an over-long reason at the
/// reason, and an order that is not placed (the operation's 409) as the
/// form's own.
#[page(POST "/orders/{id}/cancel")]
async fn cancel_order(cx: &Cx, Form(form): Form<OrderCancelOrderArgsForm>) -> topcoat::Result<impl View> {
    let id = *path_param::<Id>(cx)?;
    let form = OrderCancelOrderArgsForm { id: Some(id.to_string()), ..form };
    let errors = match form.submit(cx).await {
        Ok(order) => return Err(see_other(format!("/orders/{}", order.id)).into()),
        Err(errors) => errors,
    };
    Ok(view! {
        (StatusCode::UNPROCESSABLE_ENTITY)
        order_page(id: id, cancel: Some(form), errors: errors)
    })
}

/// Each line's product, from the catalog: `place_order_input_fields`
/// renders the `productId` of every line (`lines.productId`) as a select
/// of these, each product's id as the API sends it and its name.
fn product_choices() -> Choices {
    Choices::new().with("lines.productId", CATALOG.iter().map(|product| (product.id().to_string(), product.name)))
}

/// The form that places an order: a row per line, each line's product a
/// select of the catalog's, and the shipping address. The sign-in button
/// comes first for a shopper the operation would refuse.
#[component]
async fn place_order_page(
    cx: &Cx,
    #[default(PlaceOrderInputForm::new())] form: PlaceOrderInputForm,
    #[default] errors: FormErrors,
) -> topcoat::Result<impl View> {
    let signed_out = needs_sign_in(operations::can_order_place_order(cx).await);
    Ok(view! {
        shop_page(
            title: "Place an order",
            if signed_out {
                sign_in_button(next: "/orders/new")
            }
            <form method="post" action="/orders/new">
                place_order_input_fields(form: form, errors: errors, choices: product_choices())
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
/// called: the new order's page (303), or the form again, 422, as sent,
/// each error at its control (`lines[0].quantity`).
#[page(POST "/orders/new")]
async fn place_order(cx: &Cx, Form(form): Form<PlaceOrderInputForm>) -> topcoat::Result<impl View> {
    let mut form = form;
    let (status, errors) = if form.apply_action() {
        (StatusCode::OK, FormErrors::default())
    } else {
        match form.parse() {
            Ok(input) => match operations::order_place_order(cx, OrderPlaceOrderArgs { input }).await {
                Ok(order) => return Err(see_other(format!("/orders/{}", order.id)).into()),
                Err(err) => (StatusCode::UNPROCESSABLE_ENTITY, FormErrors::from_api(&err)),
            },
            Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, errors),
        }
    };
    Ok(view! {
        (status)
        place_order_page(form: form, errors: errors)
    })
}
