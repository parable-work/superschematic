//! shop-orders' implementations over the generated Rust server, keeping
//! the catalog, orders and reviews in memory and the shop's users in
//! SQLite: the server in src/main.rs serves them over HTTP, and the Topcoat
//! app in ../topcoat calls them from its pages. With the `sqlite` feature,
//! `sqlite::SqliteShop` keeps them in a SQLite file of shop-db's tables
//! instead, the users' file (`users_at`). The lists of orders, reviews and
//! products answer a page at a time (D60), and the reviews' search matches
//! each review's title and body (D62). A shopper lists, reads and cancels
//! their own orders, and staff, who hold `orders.any`, every customer's.

use std::error::Error;
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use acme_shop_orders_api::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Role, Rusqlite, Service,
};
use acme_shop_orders_api::{
    identity, types, Implementations, OrderCancelOrderArgs, OrderGetOrderArgs, OrderImplementation,
    OrderListOrdersArgs, OrderPlaceOrderArgs, ProductCatalogGetProductArgs,
    ProductCatalogImplementation, ProductCatalogListProductsArgs, ProductReviewsImplementation,
    ProductReviewsListReviewsArgs, ProductReviewsWriteReviewArgs,
};
use async_trait::async_trait;
use superschematic_http_runtime::page::{Cursor, Page, PageRequest};
use superschematic_http_runtime::{covers, search, ApiError, RequestContext};

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

/// The permissions of the `shopper` role: to place orders, and to read and
/// cancel their own.
pub const SHOPPER_PERMISSIONS: [&str; 2] = ["orders.read", "orders.write"];

/// The permission of staff, who read and cancel every customer's orders.
/// It is nested under `orders`, so a role that grants `orders` grants it
/// too.
pub const EVERY_ORDER: &str = "orders.any";

/// The permissions of the `staff` role: a shopper's, and `EVERY_ORDER`.
pub const STAFF_PERMISSIONS: [&str; 3] = ["orders.read", "orders.write", EVERY_ORDER];

/// Adds a shopper who signs in with `login` and `password`, holding the
/// `shopper` role, which grants `SHOPPER_PERMISSIONS`, as staff would add
/// one through shop-api's administration routes. The first shopper creates
/// the role, and a `shopper` role that grants anything else is rewritten,
/// as `update_roles` rewrites it.
pub async fn add_shopper(
    users: &Service,
    login: &str,
    name: &str,
    password: &str,
) -> Result<(), Box<dyn Error>> {
    add_user(
        users,
        login,
        name,
        password,
        "shopper",
        &SHOPPER_PERMISSIONS,
    )
    .await
}

/// Adds a member of staff who signs in with `login` and `password`,
/// holding the `staff` role, which grants `STAFF_PERMISSIONS`. The first
/// of them creates the role, and a `staff` role that grants anything else
/// is rewritten.
pub async fn add_staff(
    users: &Service,
    login: &str,
    name: &str,
    password: &str,
) -> Result<(), Box<dyn Error>> {
    add_user(users, login, name, password, "staff", &STAFF_PERMISSIONS).await
}

/// Brings the `shopper` and `staff` roles to `SHOPPER_PERMISSIONS` and
/// `STAFF_PERMISSIONS`, creating either one no user holds yet. A database
/// made before staff held `EVERY_ORDER` has a `shopper` role that grants
/// `orders`, which covers `EVERY_ORDER`: its shoppers read and cancel
/// every customer's orders until the role is rewritten. The Topcoat app
/// calls it each time it opens a SQLite file, its users already there.
pub async fn update_roles(users: &Service) -> Result<(), Box<dyn Error>> {
    role(users, "shopper", &SHOPPER_PERMISSIONS).await?;
    role(users, "staff", &STAFF_PERMISSIONS).await?;
    Ok(())
}

/// Adds a user who holds the role `role_name`, which grants `permissions`,
/// as `role` finds, rewrites or creates it.
async fn add_user(
    users: &Service,
    login: &str,
    name: &str,
    password: &str,
    role_name: &str,
    permissions: &[&str],
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
    let role = role(users, role_name, permissions).await?;
    store
        .grant_role(&user.id, &role.id, SystemTime::now())
        .await?;
    Ok(())
}

/// The role named `name`, granting `permissions` and no others: the one a
/// user added before created, rewritten when it grants anything else, or
/// a new one.
async fn role(users: &Service, name: &str, permissions: &[&str]) -> Result<Role, Box<dyn Error>> {
    let store = users.store();
    let wanted: Vec<String> = permissions.iter().map(|&p| p.to_owned()).collect();
    let existing = store
        .list_roles()
        .await?
        .into_iter()
        .find(|role| role.name == name);
    Ok(match existing {
        Some(role) if same_permissions(&role.permissions, &wanted) => role,
        Some(role) => store.update_role(&role.id, name, &wanted).await?,
        None => store.create_role(name, &wanted).await?,
    })
}

/// Whether two lists of permissions grant the same ones, in any order.
fn same_permissions(a: &[String], b: &[String]) -> bool {
    fn set(list: &[String]) -> std::collections::BTreeSet<&str> {
        list.iter().map(String::as_str).collect()
    }
    set(a) == set(b)
}

/// The catalog's products, and the orders and reviews placed so far.
/// `Shop::default()` starts with an empty catalog, as the Go server's test
/// database does.
#[derive(Default)]
pub struct Shop {
    products: Vec<types::ProductView>,
    orders: Mutex<Vec<Order>>,
    reviews: Mutex<Vec<Review>>,
}

/// An order and its customer, the caller who placed it, by the subject of
/// their principal, which the view leaves out.
struct Order {
    customer: String,
    view: types::OrderView,
}

impl Order {
    /// Whether the caller may read and cancel the order: it is theirs, or
    /// they are staff.
    fn visible_to(&self, ctx: &RequestContext) -> bool {
        sees_every_order(ctx) || self.customer == caller(ctx)
    }
}

/// A review and what the list and the one-per-author rule read beside it.
struct Review {
    product: types::IdentityUUID,
    author: String,
    view: types::ReviewView,
}

impl Shop {
    /// A shop whose catalog holds these products.
    pub fn with_products(products: impl IntoIterator<Item = types::ProductView>) -> Self {
        Self {
            products: products.into_iter().collect(),
            ..Self::default()
        }
    }

    fn product(&self, id: &types::IdentityUUID) -> Option<&types::ProductView> {
        self.products.iter().find(|product| product.id == *id)
    }
}

/// A shop: what implements every namespace of shop-orders, `Shop` in
/// memory and `sqlite::SqliteShop` in SQLite.
pub trait ShopImplementation:
    OrderImplementation + ProductCatalogImplementation + ProductReviewsImplementation
{
}

impl<S> ShopImplementation for S where
    S: OrderImplementation + ProductCatalogImplementation + ProductReviewsImplementation
{
}

/// The implementations the generated router and the Topcoat app run: the
/// shop for every namespace, in memory or in SQLite, and the identity
/// runtime's authenticator over `users`, whose caller is the user a
/// request's session signs in.
pub fn implementations<S: ShopImplementation>(
    shop: Arc<S>,
    users: Arc<Service>,
) -> Implementations {
    Implementations {
        order: shop.clone(),
        product_catalog: shop.clone(),
        product_reviews: shop,
        authenticator: Arc::new(IdentityAuthenticator::new(users)),
    }
}

/// A row's place in a list's order, which a page's cursor holds: what the
/// list orders by, then the row's id, which makes the order total. Orders
/// and reviews are ordered by a time, products by their names.
trait ListKey: Sized {
    /// What two keys compare by.
    type Order: Ord;

    fn order(&self) -> Self::Order;

    /// The key a cursor names, or the 400 of one this list never returned.
    fn decode(cursor: &Cursor) -> Result<Self, ApiError>;
}

/// An order's key, its `placedAt` and id, or a review's, its `createdAt`
/// and id.
type TimeKey = (types::TemporalDateTime, types::IdentityUUID);

/// A product's key: its name and id.
type NameKey = (String, types::IdentityUUID);

impl ListKey for TimeKey {
    type Order = (types::TemporalDateTime, [u8; 16]);

    fn order(&self) -> Self::Order {
        (self.0, *self.1.as_uuid().as_bytes())
    }

    fn decode(cursor: &Cursor) -> Result<Self, ApiError> {
        cursor.decode()
    }
}

impl ListKey for NameKey {
    type Order = (String, [u8; 16]);

    fn order(&self) -> Self::Order {
        (self.0.clone(), *self.1.as_uuid().as_bytes())
    }

    fn decode(cursor: &Cursor) -> Result<Self, ApiError> {
        cursor.decode()
    }
}

/// The rows a page reads: of `rows`, those after the cursor's key, in the
/// order of their keys, up to one past the page's limit, from which
/// `Page::from_rows` keeps the page and takes the next page's cursor.
fn rows_after<T, K: ListKey>(
    rows: impl IntoIterator<Item = T>,
    page: &PageRequest,
    key: impl Fn(&T) -> K,
) -> Result<Vec<T>, ApiError> {
    let after = page
        .cursor
        .as_ref()
        .map(K::decode)
        .transpose()?
        .map(|after| after.order());
    let mut rows: Vec<(K::Order, T)> = rows
        .into_iter()
        .map(|row| (key(&row).order(), row))
        .filter(|(order, _)| after.as_ref().is_none_or(|after| order > after))
        .collect();
    rows.sort_by(|(a, _), (b, _)| a.cmp(b));
    let limit = usize::try_from(page.limit.max(1)).unwrap_or(usize::MAX);
    Ok(rows
        .into_iter()
        .take(limit.saturating_add(1))
        .map(|(_, row)| row)
        .collect())
}

fn order_key(order: &types::OrderView) -> TimeKey {
    (order.placed_at, order.id)
}

fn review_key(review: &types::ReviewView) -> TimeKey {
    (review.created_at, review.id)
}

fn product_key(product: &types::ProductView) -> NameKey {
    (product.name.clone(), product.id)
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

/// Whether the caller is staff: a permission of theirs covers
/// `EVERY_ORDER`. getOrder and cancelOrder are `@requireOwnership`, which
/// leaves the check to the implementation (D29): a shopper reads and
/// cancels their own orders, and another customer's is the 404 of an
/// order no one placed, so its id tells them nothing.
fn sees_every_order(ctx: &RequestContext) -> bool {
    ctx.principal.as_ref().is_some_and(|principal| {
        principal
            .permissions
            .iter()
            .any(|held| covers(held, EVERY_ORDER))
    })
}

#[async_trait]
impl OrderImplementation for Shop {
    /// The caller's orders, or every customer's for staff, oldest first,
    /// with the given statuses, or of every status when there are none.
    async fn list_orders(
        &self,
        ctx: RequestContext,
        args: OrderListOrdersArgs,
    ) -> Result<Page<types::OrderView>, ApiError> {
        let orders = self.orders.lock().unwrap();
        let listed = orders
            .iter()
            .filter(|order| order.visible_to(&ctx))
            .map(|order| &order.view)
            .filter(|order| {
                args.statuses
                    .as_ref()
                    .is_none_or(|statuses| statuses.is_empty() || statuses.contains(&order.status))
            });
        let rows = rows_after(listed.cloned(), &args.page, order_key)?;
        Page::from_rows(rows, args.page.limit, order_key)
    }

    async fn place_order(
        &self,
        ctx: RequestContext,
        args: OrderPlaceOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let mut lines = Vec::with_capacity(args.input.lines.len());
        for line in &args.input.lines {
            let Some(product) = self.product(&line.product_id) else {
                return Err(ApiError::bad_request(format!(
                    "no product has id {}",
                    line.product_id
                )));
            };
            lines.push(types::OrderLineView {
                id: types::IdentityUUID::new_v4(),
                quantity: line.quantity as i64,
                unit_price_cents: product.price_cents,
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
        self.orders.lock().unwrap().push(Order {
            customer: caller(&ctx),
            view: order.clone(),
        });
        Ok(order)
    }

    /// The caller's order, or any for staff.
    async fn get_order(
        &self,
        ctx: RequestContext,
        args: OrderGetOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let orders = self.orders.lock().unwrap();
        orders
            .iter()
            .find(|order| order.view.id == args.id && order.visible_to(&ctx))
            .map(|order| order.view.clone())
            .ok_or_else(|| ApiError::not_found("order not found"))
    }

    /// Cancels the caller's order, or any for staff, while it is placed.
    async fn cancel_order(
        &self,
        ctx: RequestContext,
        args: OrderCancelOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let mut orders = self.orders.lock().unwrap();
        let order = orders
            .iter_mut()
            .find(|order| order.view.id == args.id && order.visible_to(&ctx))
            .map(|order| &mut order.view)
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
impl ProductCatalogImplementation for Shop {
    /// The products by name, those with the given ids when there are any:
    /// the lookup a page that holds products' ids names them through
    /// (D61).
    async fn list_products(
        &self,
        _ctx: RequestContext,
        args: ProductCatalogListProductsArgs,
    ) -> Result<Page<types::ProductView>, ApiError> {
        let listed = self.products.iter().filter(|product| {
            args.ids
                .as_ref()
                .is_none_or(|ids| ids.is_empty() || ids.contains(&product.id))
        });
        let rows = rows_after(listed.cloned(), &args.page, product_key)?;
        Page::from_rows(rows, args.page.limit, product_key)
    }

    async fn get_product(
        &self,
        _ctx: RequestContext,
        args: ProductCatalogGetProductArgs,
    ) -> Result<types::ProductView, ApiError> {
        self.product(&args.product_id)
            .cloned()
            .ok_or_else(|| ApiError::not_found("product not found"))
    }
}

#[async_trait]
impl ProductReviewsImplementation for Shop {
    /// A product's reviews, oldest first, those rated `min_rating` or more
    /// when it is given. A search, `q`, keeps the reviews whose title or
    /// body holds each of its words, in any case (D62), as shop-db's
    /// `search_text` joins them.
    async fn list_reviews(
        &self,
        _ctx: RequestContext,
        args: ProductReviewsListReviewsArgs,
    ) -> Result<Page<types::ReviewView>, ApiError> {
        let terms = args.q.as_deref().map(search::terms).unwrap_or_default();
        let reviews = self.reviews.lock().unwrap();
        let listed = reviews
            .iter()
            .filter(|review| {
                review.product == args.product_id
                    && args.min_rating.is_none_or(|min| review.view.rating >= min)
                    && search::matches(
                        &format!("{} {}", review.view.title, review.view.body),
                        &terms,
                    )
            })
            .map(|review| review.view.clone());
        let rows = rows_after(listed, &args.page, review_key)?;
        Page::from_rows(rows, args.page.limit, review_key)
    }

    async fn write_review(
        &self,
        ctx: RequestContext,
        args: ProductReviewsWriteReviewArgs,
    ) -> Result<types::ReviewView, ApiError> {
        if self.product(&args.product_id).is_none() {
            return Err(ApiError::not_found("product not found"));
        }
        let author = caller(&ctx);
        let mut reviews = self.reviews.lock().unwrap();
        if reviews
            .iter()
            .any(|review| review.product == args.product_id && review.author == author)
        {
            return Err(ApiError::conflict("you have already reviewed this product"));
        }
        let view = types::ReviewView {
            id: types::IdentityUUID::new_v4(),
            rating: args.input.rating,
            title: args.input.title,
            body: args.input.body,
            created_at: now(),
        };
        reviews.push(Review {
            product: args.product_id,
            author,
            view: view.clone(),
        });
        Ok(view)
    }
}
