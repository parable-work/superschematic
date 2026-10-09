//! The acme shop as a Topcoat app over shop-orders. Its pages call the
//! operations in-process through the crate the Topcoat extension writes
//! (`acme_shop_orders_topcoat`), by each route's rules: a shopper signs in
//! with their email and password, writes a review through the form the
//! extension builds from `WriteReviewInput`, and lists their orders. The
//! reviews and the orders render through the display components the crate
//! builds from their types. The JSON API is mounted at `/api` beside the
//! pages, on the same implementations, which keep the shop in memory or in
//! a SQLite file of shop-db's tables, and the shop's users are the core
//! user model's (D50): one session cookie signs the shopper in on both.

use std::sync::Arc;

use acme_shop_orders_server::implementations;
use acme_shop_orders_topcoat::api::runtime::identity::{LoginInput, Service};
use acme_shop_orders_topcoat::api::{
    OrderImplementation, OrderListOrdersArgs, ProductReviewsImplementation, ProductReviewsListReviewsArgs,
    ProductReviewsWriteReviewArgs, types,
};
use acme_shop_orders_topcoat::forms::{FormErrors, WriteReviewInputForm, write_review_input_fields};
use acme_shop_orders_topcoat::records::{OrderViewRecord, ReviewViewRecord};
use acme_shop_orders_topcoat::views::{order_view_table, review_view_detail};
use acme_shop_orders_topcoat::{IdentityPageAuthenticator, RouterBuilderShopOrdersExt, operations};
use serde::Deserialize;
use topcoat::context::{Cx, app_context};
use topcoat::router::content::Form;
use topcoat::router::error::see_other;
use topcoat::router::header::SET_COOKIE;
use topcoat::router::request::parts;
use topcoat::router::response::response_headers;
use topcoat::router::{HeaderValue, Router, RouterBuilderDiscoverExt, StatusCode, page};
use topcoat::runtime::shard;
use topcoat::view::{View, component, view};

/// The one product the shop sells, at 19.99.
pub const PRODUCT: &str = "6f1c2a3e-4b5d-4e6f-8a9b-0c1d2e3f4a5b";

pub fn product() -> types::IdentityUUID {
    PRODUCT.parse().expect("a UUID")
}

/// The identity config the app serves with: plain HTTP on a local port, so
/// the session cookie is not `Secure`.
pub const IDENTITY_CONFIG: &str = r#"{"cookie": {"secure": false}}"#;

/// The app: its pages, and shop-orders' JSON API and in-process operations
/// over `shop`, in memory (`acme_shop_orders_server::Shop`) or in SQLite
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

/// The sign-in form, as the browser sends it.
#[derive(Default, Deserialize)]
#[serde(default)]
pub struct SignInForm {
    login: String,
    password: String,
}

/// Signs a shopper in with their email and password: the identity service
/// checks them, after the cross-origin check a cookie login passes, and
/// starts a cookie session, which every page and the JSON API read. A
/// refusal renders the form again with its status, 401 for a wrong
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
            return Err(see_other("/reviews").into());
        }
        Err(err) => err,
    };
    Ok(view! {
        (refused.status)
        sign_in_page(login: form.login, refused: Some(refused.message))
    })
}

/// The sign-in page.
#[page("/sign-in")]
async fn show_sign_in() -> topcoat::Result<impl View> {
    Ok(view! { sign_in_page() })
}

/// The sign-in form, with the login as sent and why the sign-in was
/// refused.
#[component]
async fn sign_in_page(#[default] login: String, #[default] refused: Option<String>) -> topcoat::Result<impl View> {
    Ok(view! {
        <h1>"Sign in"</h1>
        if let Some(message) = refused {
            <p class="form-error">(message)</p>
        }
        <form method="post" action="/sign-in">
            <label for="login">"Email"</label>
            <input id="login" name="login" type="email" required=(true) value=(login)>
            <label for="password">"Password"</label>
            <input id="password" name="password" type="password" required=(true)>
            <button type="submit">"Sign in"</button>
        </form>
    })
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
/// and its errors after a refused post.
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
    Ok(view! {
        <h1>"Reviews"</h1>
        if empty {
            <p>"No reviews yet."</p>
        }
        #[key(review.id.clone())]
        for review in reviews {
            review_card(review: review)
        }
        <form method="post" action="/reviews">
            write_review_input_fields(form: form, errors: errors)
            <button type="submit">"Post review"</button>
        </form>
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

/// The shopper's orders, which need `orders.read`, as `order_view_table`
/// renders them: a column per summary field `OrderView` declares. A caller
/// without the permission sees the route's refusal, with its status.
#[page("/orders")]
async fn show_orders(cx: &Cx) -> topcoat::Result<impl View> {
    let args = OrderListOrdersArgs { statuses: None, limit: None };
    let (status, orders) = match operations::order_list_orders(cx, args).await {
        Ok(orders) => (StatusCode::OK, Ok(orders.iter().map(OrderViewRecord::from).collect::<Vec<_>>())),
        Err(err) => (err.status, Err(err.message)),
    };
    Ok(view! {
        (status)
        <h1>"Orders"</h1>
        match orders {
            Ok(orders) => {
                if orders.is_empty() {
                    <p>"No orders yet."</p>
                } else {
                    order_view_table(rows: orders)
                }
            },
            Err(message) => <p>(message)</p>,
        }
    })
}
