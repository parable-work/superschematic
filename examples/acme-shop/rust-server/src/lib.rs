//! shop-orders' implementations over the generated Rust server, keeping
//! orders and reviews in memory and the shop's users in SQLite: the server
//! in src/main.rs serves them over HTTP, and the Topcoat app in ../topcoat
//! calls them from its pages. With the `sqlite` feature,
//! `sqlite::SqliteShop` keeps the orders and reviews in a SQLite file of
//! shop-db's tables instead, the users' file (`users_at`).

use std::collections::HashMap;
use std::error::Error;
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use acme_shop_orders_api::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Rusqlite, Service,
};
use acme_shop_orders_api::{
    identity, types, Implementations, OrderCancelOrderArgs, OrderGetOrderArgs, OrderImplementation,
    OrderListOrdersArgs, OrderPlaceOrderArgs, ProductReviewsImplementation,
    ProductReviewsListReviewsArgs, ProductReviewsWriteReviewArgs,
};
use async_trait::async_trait;
use superschematic_http_runtime::{ApiError, RequestContext};

#[cfg(feature = "sqlite")]
pub mod sqlite;

/// shop-db's identity tables in SQLite (identity.sql): its User and Role
/// tables and the session, credential and role grant tables the build adds.
pub const IDENTITY_TABLES: &str = include_str!("../identity.sql");

/// The shop's users: shop-orders' identity service over the SQLite
/// database at `path`, which holds `IDENTITY_TABLES`, with `config`, the
/// identity config's JSON. The service reads the session a request
/// carries, as shop-api's login started it, and the roles of its user.
pub fn users(path: &str, config: &str) -> Result<Arc<Service>, Box<dyn Error>> {
    users_over(rusqlite::Connection::open(path)?, config)
}

/// The shop's users in the SQLite database `url` names, read as
/// `sqlite::SqliteShop::open` reads it: the file the shop keeps its orders
/// and reviews in, whose shop-db tables hold `IDENTITY_TABLES` too, so a
/// user the identity service adds is one a review's author or an order's
/// customer can be.
#[cfg(feature = "sqlite")]
pub fn users_at(url: &str, config: &str) -> Result<Arc<Service>, Box<dyn Error>> {
    let path = sqlite::database_path(url).map_err(|scheme| format!("a {scheme} URL, not a SQLite one"))?;
    let flags = rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE | rusqlite::OpenFlags::SQLITE_OPEN_URI;
    users_over(rusqlite::Connection::open_with_flags(path, flags)?, config)
}

/// The shop's users in a SQLite database in memory, its tables created and
/// no user in it yet: the Topcoat app's.
pub fn users_in_memory(config: &str) -> Result<Arc<Service>, Box<dyn Error>> {
    let connection = rusqlite::Connection::open_in_memory()?;
    connection.execute_batch(IDENTITY_TABLES)?;
    users_over(connection, config)
}

fn users_over(
    connection: rusqlite::Connection,
    config: &str,
) -> Result<Arc<Service>, Box<dyn Error>> {
    let store = identity::store(Rusqlite::new(connection)?)?;
    let config = Config::parse(config.as_bytes())?;
    Ok(Arc::new(identity::service(Arc::new(store), config)?))
}

/// Adds a shopper who signs in with `login` and `password`, holding the
/// `shopper` role, which grants `orders`, as staff would add one through
/// shop-api's administration routes. The first shopper creates the role.
pub async fn add_shopper(
    users: &Service,
    login: &str,
    name: &str,
    password: &str,
) -> Result<(), Box<dyn Error>> {
    let store = users.store();
    let user = store
        .create_user(NewUser {
            login: login.to_owned(),
            name: name.to_owned(),
            password_hash: hash_password(password, users.config().argon2_params())?,
            at: SystemTime::now(),
        })
        .await?;
    let role = match store
        .list_roles()
        .await?
        .into_iter()
        .find(|role| role.name == "shopper")
    {
        Some(role) => role,
        None => store.create_role("shopper", &["orders".to_owned()]).await?,
    };
    store
        .grant_role(&user.id, &role.id, SystemTime::now())
        .await?;
    Ok(())
}

/// The catalog's prices by product, and the orders and reviews placed so
/// far. `Shop::default()` starts with an empty catalog, as the Go server's
/// test database does.
#[derive(Default)]
pub struct Shop {
    prices: HashMap<types::IdentityUUID, i64>,
    orders: Mutex<Vec<types::OrderView>>,
    reviews: Mutex<Vec<(types::IdentityUUID, String, types::ReviewView)>>,
}

impl Shop {
    /// A shop whose catalog holds each product at its price in cents.
    pub fn with_prices(prices: impl IntoIterator<Item = (types::IdentityUUID, i64)>) -> Self {
        Self {
            prices: prices.into_iter().collect(),
            ..Self::default()
        }
    }
}

/// The implementations the generated router and the Topcoat app run: the
/// shop for both namespaces, in memory or in SQLite, and the identity
/// runtime's authenticator over `users`, whose caller is the user a
/// request's session signs in.
pub fn implementations<S: OrderImplementation + ProductReviewsImplementation>(
    shop: Arc<S>,
    users: Arc<Service>,
) -> Implementations {
    Implementations {
        order: shop.clone(),
        product_reviews: shop,
        authenticator: Arc::new(IdentityAuthenticator::new(users)),
    }
}

fn now() -> types::TemporalDateTime {
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| since.as_millis());
    types::TemporalDateTime::from_timestamp_millis(i64::try_from(millis).unwrap_or(i64::MAX))
        .expect("a timestamp in range")
}

/// The caller's id: the principal's subject, the user's base62 UUID.
fn caller(ctx: &RequestContext) -> String {
    ctx.principal
        .as_ref()
        .map(|principal| principal.subject.clone())
        .unwrap_or_default()
}

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(
        &self,
        _ctx: RequestContext,
        args: OrderListOrdersArgs,
    ) -> Result<Vec<types::OrderView>, ApiError> {
        let orders = self.orders.lock().unwrap();
        let limit = args.limit.map_or(20, |limit| limit as usize);
        Ok(orders
            .iter()
            .filter(|order| {
                args.statuses
                    .as_ref()
                    .is_none_or(|statuses| statuses.contains(&order.status))
            })
            .take(limit)
            .cloned()
            .collect())
    }

    async fn place_order(
        &self,
        _ctx: RequestContext,
        args: OrderPlaceOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let mut lines = Vec::with_capacity(args.input.lines.len());
        for line in &args.input.lines {
            let Some(&unit_price_cents) = self.prices.get(&line.product_id) else {
                return Err(ApiError::bad_request(format!(
                    "no product has id {}",
                    line.product_id
                )));
            };
            lines.push(types::OrderLineView {
                id: types::IdentityUUID::new_v4(),
                quantity: line.quantity as i64,
                unit_price_cents,
                product_id: line.product_id,
            });
        }
        let order = types::OrderView {
            id: types::IdentityUUID::new_v4(),
            status: types::OrderStatus::Placed,
            placed_at: now(),
            shipping_address: args.input.shipping_address,
            cancel_reason: None,
            total_cents: lines
                .iter()
                .map(|line| line.quantity * line.unit_price_cents)
                .sum(),
            lines,
        };
        self.orders.lock().unwrap().push(order.clone());
        Ok(order)
    }

    async fn get_order(
        &self,
        _ctx: RequestContext,
        args: OrderGetOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let orders = self.orders.lock().unwrap();
        orders
            .iter()
            .find(|order| order.id == args.id)
            .cloned()
            .ok_or_else(|| ApiError::not_found("order not found"))
    }

    async fn cancel_order(
        &self,
        _ctx: RequestContext,
        args: OrderCancelOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let mut orders = self.orders.lock().unwrap();
        let order = orders
            .iter_mut()
            .find(|order| order.id == args.id)
            .ok_or_else(|| ApiError::not_found("order not found"))?;
        if order.status != types::OrderStatus::Placed {
            return Err(ApiError::conflict(format!(
                "order {} is {}",
                order.id,
                order.status.as_str()
            )));
        }
        order.status = types::OrderStatus::Cancelled;
        order.cancel_reason = args.reason;
        Ok(order.clone())
    }
}

#[async_trait]
impl ProductReviewsImplementation for Shop {
    async fn list_reviews(
        &self,
        _ctx: RequestContext,
        args: ProductReviewsListReviewsArgs,
    ) -> Result<Vec<types::ReviewView>, ApiError> {
        let reviews = self.reviews.lock().unwrap();
        Ok(reviews
            .iter()
            .filter(|(product, _, review)| {
                *product == args.product_id
                    && args.min_rating.is_none_or(|min| review.rating >= min)
            })
            .map(|(_, _, review)| review.clone())
            .collect())
    }

    async fn write_review(
        &self,
        ctx: RequestContext,
        args: ProductReviewsWriteReviewArgs,
    ) -> Result<types::ReviewView, ApiError> {
        if !self.prices.contains_key(&args.product_id) {
            return Err(ApiError::not_found("product not found"));
        }
        let author = caller(&ctx);
        let mut reviews = self.reviews.lock().unwrap();
        if reviews
            .iter()
            .any(|(product, by, _)| *product == args.product_id && *by == author)
        {
            return Err(ApiError::conflict("you have already reviewed this product"));
        }
        let review = types::ReviewView {
            id: types::IdentityUUID::new_v4(),
            rating: args.input.rating,
            title: args.input.title,
            body: args.input.body,
            created_at: now(),
        };
        reviews.push((args.product_id, author, review.clone()));
        Ok(review)
    }
}
