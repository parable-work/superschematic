//! The acme shop as a Topcoat app over shop-orders. Its pages call the
//! operations in-process through the crate the Topcoat extension writes
//! (`acme_shop_orders_topcoat`), by each route's rules: a shopper signs in
//! with a Topcoat session, writes a review through the form the extension
//! builds from `WriteReviewInput`, and lists their orders. The JSON API is
//! mounted at `/api` beside the pages, on the same implementations, which
//! keep the shop in memory or in a SQLite file of shop-db's tables.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use acme_shop_orders_server::implementations;
use acme_shop_orders_topcoat::api::runtime::{ApiError, Principal};
use acme_shop_orders_topcoat::api::{
    OrderImplementation, OrderListOrdersArgs, ProductReviewsImplementation, ProductReviewsListReviewsArgs,
    ProductReviewsWriteReviewArgs, types,
};
use acme_shop_orders_topcoat::forms::{FormErrors, WriteReviewInputForm, write_review_input_fields};
use acme_shop_orders_topcoat::records::ReviewViewRecord;
use acme_shop_orders_topcoat::{PageAuthenticator, RouterBuilderShopOrdersExt, operations};
use async_trait::async_trait;
use topcoat::context::{Cx, app_context};
use topcoat::cookie::RouterBuilderCookieExt;
use topcoat::router::content::Form;
use topcoat::router::error::see_other;
use topcoat::router::{Router, RouterBuilderDiscoverExt, StatusCode, page};
use topcoat::runtime::shard;
use topcoat::session::{self, RouterBuilderSessionExt, SessionConfig};
use topcoat::view::{View, component, view};

/// The one product the shop sells, at 19.99.
pub const PRODUCT: &str = "6f1c2a3e-4b5d-4e6f-8a9b-0c1d2e3f4a5b";

pub fn product() -> types::IdentityUUID {
    PRODUCT.parse().expect("a UUID")
}

/// The demo shopper `POST /sign-in` signs in, who holds `orders`. In a
/// SQLite shop they are a user, since a review's author must be one.
pub const SHOPPER: &str = "00000000-0000-4000-8000-0000000000a1";

pub fn shopper() -> types::IdentityUUID {
    SHOPPER.parse().expect("a UUID")
}

/// The signed-in shoppers by session token hash. An app keeps them in its
/// database, with their expiry.
#[derive(Default)]
pub struct Sessions(Mutex<HashMap<[u8; 32], Principal>>);

/// A page's caller: the shopper its session signed in.
struct SessionCaller(Arc<Sessions>);

#[async_trait]
impl PageAuthenticator for SessionCaller {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        let hash = session::token_hash(cx)
            .await
            .map_err(|err| ApiError::internal(err.to_string()))?;
        Ok(hash.and_then(|hash| self.0.0.lock().unwrap().get(&*hash).cloned()))
    }
}

/// The app: its pages, the session they sign in with, and shop-orders'
/// JSON API and in-process operations over `shop`, in memory
/// (`acme_shop_orders_server::Shop`) or in SQLite
/// (`acme_shop_orders_server::sqlite::SqliteShop`).
pub fn app<S: OrderImplementation + ProductReviewsImplementation>(shop: Arc<S>) -> Router {
    let sessions = Arc::new(Sessions::default());
    Router::builder()
        .discover()
        .cookies()
        .sessions(SessionConfig::default())
        .app_context(Arc::clone(&sessions))
        .shop_orders(implementations(shop), SessionCaller(sessions))
        .build()
}

/// Signs the demo shopper in: a new session, whose caller holds `orders`.
/// A real app checks a password or a passkey first.
#[page(POST "/sign-in")]
async fn sign_in(cx: &Cx) -> topcoat::Result<impl View> {
    let started = session::start(cx).await?;
    let sessions: &Arc<Sessions> = app_context(cx);
    sessions.0.lock().unwrap().insert(*started.token_hash, Principal::new(SHOPPER, ["orders"]));
    Err::<(), _>(see_other("/reviews").into())
}

/// One review, as a shard: the browser can render it again from its record.
#[shard("/shards/review")]
async fn review_card(review: ReviewViewRecord) -> topcoat::Result<impl View> {
    Ok(view! {
        <article class="review">
            <h3>(review.title) " (" (review.rating.to_string()) "/5)"</h3>
            <p>(review.body)</p>
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

/// The shopper's orders, which need `orders.read`: a caller without it sees
/// the route's refusal, with its status.
#[page("/orders")]
async fn show_orders(cx: &Cx) -> topcoat::Result<impl View> {
    let args = OrderListOrdersArgs { statuses: None, limit: None };
    let (status, lines) = match operations::order_list_orders(cx, args).await {
        Ok(orders) if orders.is_empty() => (StatusCode::OK, vec!["No orders yet.".to_owned()]),
        Ok(orders) => (
            StatusCode::OK,
            orders.iter().map(|order| format!("{} {}", order.id, order.status.as_str())).collect(),
        ),
        Err(err) => (err.status, vec![err.message]),
    };
    Ok(view! {
        (status)
        <h1>"Orders"</h1>
        for line in lines {
            <p>(line)</p>
        }
    })
}
