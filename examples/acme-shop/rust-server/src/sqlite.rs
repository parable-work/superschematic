//! shop-orders' implementations over a SQLite file of shop-db's tables,
//! with the `sqlite` feature. shop-db lists `sqlite` in
//! `outputs.sql.dialects`, so its build writes the tables' SQLite DDL,
//! `sql/shop-db/sqlite/create.sql`, and `superschematic migrate plan
//! --dialect sqlite` plans the file that `superschematic-migrate apply`
//! migrates. The shop keeps the rows as shop-db declares them, as the Go
//! server keeps them in Postgres: an id is a UUID as hyphenated text, a time
//! RFC 3339 UTC text and a shipping address its JSON. The tables' defaults
//! give a new row its id and its times. A list reads up to one row past
//! its page's limit after the cursor's key, the columns it orders by as
//! the table holds them, its id last, with a row-value comparison (D60);
//! the reviews' search is a `LIKE` per word over the review's
//! `search_text`, which SQLite computes as it reads each row (D62).
//!
//! One connection serves every request, behind a mutex, and a statement
//! blocks the executor while it runs, as the version graph's rusqlite
//! binding does (D32): one process writes the file, and every statement
//! here is short.

use std::fmt;
use std::sync::{Mutex, MutexGuard};

use acme_shop_orders_api::{
    types, OrderCancelOrderArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs,
    OrderPlaceOrderArgs, ProductCatalogGetProductArgs, ProductCatalogImplementation,
    ProductCatalogListProductsArgs, ProductReviewsImplementation, ProductReviewsListReviewsArgs,
    ProductReviewsWriteReviewArgs,
};
use async_trait::async_trait;
use rusqlite::types::Value;
use rusqlite::{ffi, params, params_from_iter, Connection, OpenFlags, OptionalExtension, Row};
use superschematic_http_runtime::page::{Cursor, Page, PageRequest};
use superschematic_http_runtime::{search, ApiError, RequestContext};

use crate::{caller, now};

/// The shop's catalog, orders and reviews in a SQLite file.
pub struct SqliteShop {
    conn: Mutex<Connection>,
}

/// Why `SqliteShop::open` refused a database URL: it names another kind of
/// database, SQLite could not open or read it, or it holds none of
/// shop-db's tables.
#[derive(Debug)]
pub struct OpenError(Refusal);

/// A refusal names a SQLite database by its URL, a path or a `file:` URI.
/// It names another kind of database by its scheme alone: that URL may
/// hold a server's password, so neither the message nor `Debug` shows it.
#[derive(Debug)]
enum Refusal {
    NotSqlite {
        scheme: String,
    },
    Unreadable {
        database: String,
        cause: rusqlite::Error,
    },
    NotMigrated {
        database: String,
    },
}

impl fmt::Display for OpenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.0 {
            Refusal::NotSqlite { scheme } => write!(
                f,
                "a {scheme}:// URL is not a SQLite database: give a sqlite: URL, a file: URI or a path"
            ),
            Refusal::Unreadable { database, cause } => write!(f, "open {database}: {cause}"),
            Refusal::NotMigrated { database } => write!(
                f,
                "{database} holds no shop-db tables: apply shop-db's SQLite plan to it with superschematic-migrate"
            ),
        }
    }
}

impl std::error::Error for OpenError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match &self.0 {
            Refusal::Unreadable { cause, .. } => Some(cause),
            _ => None,
        }
    }
}

/// shop-db's tables the shop reads and writes.
const TABLES: [&str; 5] = ["user", "product", "order", "order_line", "review"];

impl SqliteShop {
    /// Opens the database `url` names, as superschematic-migrate reads its
    /// `--database-url`: `sqlite:PATH` and `sqlite://PATH` are PATH, and so
    /// is a bare path; a `file:` URI opens as a URI. The file must exist and
    /// hold shop-db's tables. A URL with any other scheme, such as a
    /// `postgres://` one, is refused, its scheme alone in the error.
    pub fn open(url: &str) -> Result<Self, OpenError> {
        let path = database_path(url).map_err(|scheme| {
            OpenError(Refusal::NotSqlite {
                scheme: scheme.to_owned(),
            })
        })?;
        let unreadable = |cause| {
            OpenError(Refusal::Unreadable {
                database: url.to_owned(),
                cause,
            })
        };
        let flags = OpenFlags::SQLITE_OPEN_READ_WRITE
            | OpenFlags::SQLITE_OPEN_URI
            | OpenFlags::SQLITE_OPEN_NO_MUTEX;
        let conn = Connection::open_with_flags(path, flags).map_err(unreadable)?;
        match Self::with_connection(conn) {
            Ok(Some(shop)) => Ok(shop),
            Ok(None) => Err(OpenError(Refusal::NotMigrated {
                database: url.to_owned(),
            })),
            Err(err) => Err(unreadable(err)),
        }
    }

    /// The shop over `conn`, or `None` when it holds none of shop-db's
    /// tables.
    pub fn with_connection(conn: Connection) -> rusqlite::Result<Option<Self>> {
        // SQLite enforces the foreign keys shop-db declares only on a
        // connection that turns them on.
        conn.pragma_update(None, "foreign_keys", true)?;
        let mut stmt =
            conn.prepare("SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?1")?;
        for table in TABLES {
            let found: i64 = stmt.query_row([table], |row| row.get(0))?;
            if found == 0 {
                return Ok(None);
            }
        }
        drop(stmt);
        Ok(Some(Self {
            conn: Mutex::new(conn),
        }))
    }

    /// Adds a user, unless one has the id or the email: a review's author
    /// and an order's customer must be one.
    pub fn add_user(
        &self,
        id: &types::IdentityUUID,
        email: &str,
        name: &str,
    ) -> rusqlite::Result<()> {
        self.conn().execute(
            r#"INSERT INTO "user" (id, email, name) VALUES (?1, ?2, ?3) ON CONFLICT DO NOTHING"#,
            params![uuid_text(id), email, name],
        )?;
        Ok(())
    }

    /// Adds a product to the catalog at its price in cents, in stock, unless
    /// one has the id or the SKU.
    pub fn add_product(
        &self,
        id: &types::IdentityUUID,
        sku: &str,
        name: &str,
        price_cents: i64,
    ) -> rusqlite::Result<()> {
        self.conn().execute(
            "INSERT INTO product (id, sku, name, price_cents, in_stock) VALUES (?1, ?2, ?3, ?4, 1) ON CONFLICT DO NOTHING",
            params![uuid_text(id), sku, name, price_cents],
        )?;
        Ok(())
    }

    fn conn(&self) -> MutexGuard<'_, Connection> {
        self.conn
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

/// The path, or `file:` URI, a database URL names, or else the scheme of
/// a URL that names another kind of database (`postgres` of
/// `postgres://...`). A string without a scheme is a path.
pub(crate) fn database_path(url: &str) -> Result<&str, &str> {
    let has_scheme = |scheme: &str| {
        url.get(..scheme.len())
            .is_some_and(|prefix| prefix.eq_ignore_ascii_case(scheme))
    };
    if has_scheme("sqlite:") {
        let path = &url["sqlite:".len()..];
        return Ok(path.strip_prefix("//").unwrap_or(path));
    }
    if has_scheme("file:") {
        return Ok(url);
    }
    match url.split_once("://") {
        Some((scheme, _)) if is_scheme(scheme) => Err(scheme),
        _ => Ok(url),
    }
}

/// Whether `name` is a URL scheme (RFC 3986): a letter, then letters,
/// digits, `+`, `-` and `.`.
fn is_scheme(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '+' | '-' | '.'))
}

/// A UUID as shop-db's SQLite tables hold it: hyphenated text.
fn uuid_text(id: &types::IdentityUUID) -> String {
    id.as_uuid().to_string()
}

/// The caller's user id, as shop-db's SQLite tables hold it.
fn shopper(ctx: &RequestContext) -> Result<String, ApiError> {
    caller(ctx)
        .parse::<types::IdentityUUID>()
        .map(|id| uuid_text(&id))
        .map_err(|_| ApiError::forbidden("the caller is not a user of the shop"))
}

fn internal(err: impl fmt::Display) -> ApiError {
    ApiError::internal(err.to_string())
}

/// The extended result code of a statement SQLite refused.
fn refusal(err: &rusqlite::Error) -> Option<i32> {
    err.sqlite_error().map(|err| err.extended_code)
}

/// Column `i` of `row`, read as text and parsed.
fn parsed<T, E: fmt::Display>(
    row: &Row,
    i: usize,
    parse: impl FnOnce(&str) -> Result<T, E>,
) -> rusqlite::Result<T> {
    let text: String = row.get(i)?;
    parse(&text).map_err(|err| {
        rusqlite::Error::FromSqlConversionFailure(
            i,
            rusqlite::types::Type::Text,
            err.to_string().into(),
        )
    })
}

fn order_status(text: &str) -> Result<types::OrderStatus, String> {
    types::OrderStatus::ALL
        .iter()
        .find(|status| status.as_str() == text)
        .cloned()
        .ok_or_else(|| format!("no order status is {text}"))
}

const ORDER_COLUMNS: &str = "id, status, placed_at, shipping_address, cancel_reason";

/// An order's row, its lines not read yet.
fn order_row(row: &Row) -> rusqlite::Result<types::OrderView> {
    Ok(types::OrderView {
        id: parsed(row, 0, str::parse)?,
        status: parsed(row, 1, order_status)?,
        placed_at: parsed(row, 2, types::TemporalDateTime::parse)?,
        shipping_address: parsed(row, 3, |text: &str| serde_json::from_str(text))?,
        cancel_reason: row.get(4)?,
        lines: Vec::new(),
        total_cents: 0,
    })
}

/// Reads an order's lines into it, in the order they were placed, and
/// totals them.
fn with_lines(
    conn: &Connection,
    mut order: types::OrderView,
) -> rusqlite::Result<types::OrderView> {
    let mut stmt = conn.prepare_cached(
        "SELECT id, quantity, unit_price_cents, product_id FROM order_line WHERE order_id = ?1 ORDER BY rowid",
    )?;
    order.lines = stmt
        .query_map([uuid_text(&order.id)], |row| {
            Ok(types::OrderLineView {
                id: parsed(row, 0, str::parse)?,
                quantity: row.get(1)?,
                unit_price_cents: row.get(2)?,
                product_id: parsed(row, 3, str::parse)?,
            })
        })?
        .collect::<rusqlite::Result<_>>()?;
    order.total_cents = order
        .lines
        .iter()
        .map(|line| line.quantity * line.unit_price_cents)
        .sum();
    Ok(order)
}

/// The order with this id, as hyphenated text.
fn read_order(conn: &Connection, id: &str) -> Result<types::OrderView, ApiError> {
    let order = conn
        .query_row(
            &format!(r#"SELECT {ORDER_COLUMNS} FROM "order" WHERE id = ?1"#),
            [id],
            order_row,
        )
        .optional()
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("order not found"))?;
    with_lines(conn, order).map_err(internal)
}

/// A row's place in a list's order: the columns the list orders by, as
/// the table holds them, the row's id last.
type RowKey = (String, String);

/// The query of a page of a list: the conditions its rows meet, each with
/// a `?` for each of its values, in turn.
#[derive(Default)]
struct ListQuery {
    conditions: Vec<String>,
    values: Vec<Value>,
}

impl ListQuery {
    fn condition(&mut self, condition: &str, values: impl IntoIterator<Item = Value>) {
        self.conditions.push(condition.to_owned());
        self.values.extend(values);
    }

    /// The rows after the key a page's cursor names, in the order of the
    /// key's columns, `key`, a pair such as `placed_at, id`. A cursor that
    /// does not decode to two texts is the 400 of one this list never
    /// returned.
    fn after(&mut self, cursor: Option<&Cursor>, key: &str) -> Result<(), ApiError> {
        if let Some(cursor) = cursor {
            let (first, id): RowKey = cursor.decode()?;
            self.condition(
                &format!("({key}) > (?, ?)"),
                [Value::Text(first), Value::Text(id)],
            );
        }
        Ok(())
    }

    /// Reads up to one row past the page's limit, `columns`, which `read`
    /// reads, then the key's, ordered by the key, and keeps the page with
    /// `Page::from_rows`, whose next page's cursor is its last row's key.
    fn page<T>(
        mut self,
        conn: &Connection,
        columns: &str,
        table: &str,
        key: &str,
        page: &PageRequest,
        read: impl Fn(&Row) -> rusqlite::Result<T>,
    ) -> Result<Page<T>, ApiError> {
        let mut sql = format!("SELECT {columns}, {key} FROM {table}");
        if !self.conditions.is_empty() {
            sql = format!("{sql} WHERE {}", self.conditions.join(" AND "));
        }
        sql = format!("{sql} ORDER BY {key} LIMIT ?");
        self.values
            .push(Value::Integer(page.limit.max(1).saturating_add(1)));
        let mut stmt = conn.prepare(&sql).map_err(internal)?;
        let rows = stmt
            .query_map(params_from_iter(&self.values), |row| {
                let last = row.as_ref().column_count() - 1;
                let key: RowKey = (row.get(last - 1)?, row.get(last)?);
                Ok((key, read(row)?))
            })
            .and_then(|rows| rows.collect::<rusqlite::Result<Vec<_>>>())
            .map_err(internal)?;
        Ok(Page::from_rows(rows, page.limit, |(key, _)| key.clone())?.map(|(_, row)| row))
    }
}

fn review_row(row: &Row) -> rusqlite::Result<types::ReviewView> {
    Ok(types::ReviewView {
        id: parsed(row, 0, str::parse)?,
        rating: row.get(1)?,
        title: row.get(2)?,
        body: row.get(3)?,
        created_at: parsed(row, 4, types::TemporalDateTime::parse)?,
    })
}

#[async_trait]
impl OrderImplementation for SqliteShop {
    /// The orders, oldest first, with the given statuses, or of every
    /// status when there are none.
    async fn list_orders(
        &self,
        _ctx: RequestContext,
        args: OrderListOrdersArgs,
    ) -> Result<Page<types::OrderView>, ApiError> {
        let mut query = ListQuery::default();
        if let Some(statuses) = args.statuses.filter(|statuses| !statuses.is_empty()) {
            // The statuses go in as one JSON array, which json_each reads.
            let statuses: Vec<&str> = statuses.iter().map(|status| status.as_str()).collect();
            let statuses = serde_json::to_string(&statuses).map_err(internal)?;
            query.condition(
                "status IN (SELECT value FROM json_each(?))",
                [Value::Text(statuses)],
            );
        }
        query.after(args.page.cursor.as_ref(), "placed_at, id")?;
        let conn = self.conn();
        let orders = query.page(
            &conn,
            ORDER_COLUMNS,
            r#""order""#,
            "placed_at, id",
            &args.page,
            order_row,
        )?;
        let next = orders.next.clone();
        let orders = orders
            .items
            .into_iter()
            .map(|order| with_lines(&conn, order).map_err(internal))
            .collect::<Result<Vec<_>, _>>()?;
        Ok(Page {
            items: orders,
            next,
        })
    }

    async fn place_order(
        &self,
        ctx: RequestContext,
        args: OrderPlaceOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let customer = shopper(&ctx)?;
        let address = serde_json::to_string(&args.input.shipping_address).map_err(internal)?;
        let mut conn = self.conn();
        // Dropping the transaction before its commit rolls it back.
        let tx = conn.transaction().map_err(internal)?;
        let id: String = tx
            .query_row(
                r#"INSERT INTO "order" (customer_id, status, shipping_address) VALUES (?1, ?2, ?3) RETURNING id"#,
                params![customer, types::OrderStatus::Placed.as_str(), address],
                |row| row.get(0),
            )
            .map_err(|err| match refusal(&err) {
                Some(ffi::SQLITE_CONSTRAINT_FOREIGNKEY) => ApiError::forbidden("the caller is not a user of the shop"),
                _ => internal(err),
            })?;
        for line in &args.input.lines {
            // Each line takes the product's price as it is now.
            let added = tx
                .query_row(
                    "INSERT INTO order_line (order_id, product_id, quantity, unit_price_cents)
                    SELECT ?1, id, ?2, price_cents FROM product WHERE id = ?3 RETURNING id",
                    params![id, line.quantity as i64, uuid_text(&line.product_id)],
                    |_| Ok(()),
                )
                .optional()
                .map_err(internal)?;
            if added.is_none() {
                return Err(ApiError::bad_request(format!(
                    "no product has id {}",
                    line.product_id
                )));
            }
        }
        let order = read_order(&tx, &id)?;
        tx.commit().map_err(internal)?;
        Ok(order)
    }

    async fn get_order(
        &self,
        _ctx: RequestContext,
        args: OrderGetOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        read_order(&self.conn(), &uuid_text(&args.id))
    }

    async fn cancel_order(
        &self,
        _ctx: RequestContext,
        args: OrderCancelOrderArgs,
    ) -> Result<types::OrderView, ApiError> {
        let id = uuid_text(&args.id);
        let mut conn = self.conn();
        let tx = conn.transaction().map_err(internal)?;
        let status: Option<String> = tx
            .query_row(
                r#"SELECT status FROM "order" WHERE id = ?1"#,
                [&id],
                |row| row.get(0),
            )
            .optional()
            .map_err(internal)?;
        match status {
            None => return Err(ApiError::not_found("order not found")),
            Some(status) if status != types::OrderStatus::Placed.as_str() => {
                return Err(ApiError::conflict(format!("order {} is {status}", args.id)));
            }
            Some(_) => {}
        }
        tx.execute(
            r#"UPDATE "order" SET status = ?2, cancel_reason = ?3, updated_at = ?4 WHERE id = ?1"#,
            params![
                id,
                types::OrderStatus::Cancelled.as_str(),
                args.reason,
                now().to_string()
            ],
        )
        .map_err(internal)?;
        let order = read_order(&tx, &id)?;
        tx.commit().map_err(internal)?;
        Ok(order)
    }
}

const PRODUCT_COLUMNS: &str = "id, sku, name, price_cents, in_stock";

fn product_row(row: &Row) -> rusqlite::Result<types::ProductView> {
    Ok(types::ProductView {
        id: parsed(row, 0, str::parse)?,
        sku: row.get(1)?,
        name: row.get(2)?,
        price_cents: row.get(3)?,
        in_stock: row.get(4)?,
    })
}

#[async_trait]
impl ProductCatalogImplementation for SqliteShop {
    /// The products by name, those with the given ids when there are any:
    /// the lookup a page that holds products' ids names them through
    /// (D61).
    async fn list_products(
        &self,
        _ctx: RequestContext,
        args: ProductCatalogListProductsArgs,
    ) -> Result<Page<types::ProductView>, ApiError> {
        let mut query = ListQuery::default();
        if let Some(ids) = args.ids.filter(|ids| !ids.is_empty()) {
            let ids: Vec<String> = ids.iter().map(uuid_text).collect();
            let ids = serde_json::to_string(&ids).map_err(internal)?;
            query.condition("id IN (SELECT value FROM json_each(?))", [Value::Text(ids)]);
        }
        query.after(args.page.cursor.as_ref(), "name, id")?;
        query.page(
            &self.conn(),
            PRODUCT_COLUMNS,
            "product",
            "name, id",
            &args.page,
            product_row,
        )
    }

    async fn get_product(
        &self,
        _ctx: RequestContext,
        args: ProductCatalogGetProductArgs,
    ) -> Result<types::ProductView, ApiError> {
        self.conn()
            .query_row(
                &format!("SELECT {PRODUCT_COLUMNS} FROM product WHERE id = ?1"),
                [uuid_text(&args.product_id)],
                product_row,
            )
            .optional()
            .map_err(internal)?
            .ok_or_else(|| ApiError::not_found("product not found"))
    }
}

#[async_trait]
impl ProductReviewsImplementation for SqliteShop {
    /// A product's reviews, oldest first, those rated `min_rating` or more
    /// when it is given. A search, `q`, keeps the reviews whose
    /// `search_text`, the title and body shop-db's `@searchField`s join,
    /// holds each of its words: SQLite's `LIKE` folds ASCII letters alone,
    /// and reads every review of the product, as D62 says.
    async fn list_reviews(
        &self,
        _ctx: RequestContext,
        args: ProductReviewsListReviewsArgs,
    ) -> Result<Page<types::ReviewView>, ApiError> {
        let mut query = ListQuery::default();
        query.condition("product_id = ?", [Value::Text(uuid_text(&args.product_id))]);
        query.condition("deleted_at IS NULL", []);
        if let Some(min) = args.min_rating {
            query.condition("rating >= ?", [Value::Real(min)]);
        }
        for term in args.q.as_deref().map(search::terms).unwrap_or_default() {
            query.condition(
                r"search_text LIKE ? ESCAPE '\'",
                [Value::Text(search::like_pattern(&term))],
            );
        }
        query.after(args.page.cursor.as_ref(), "created_at, id")?;
        query.page(
            &self.conn(),
            "id, rating, title, body, created_at",
            "review",
            "created_at, id",
            &args.page,
            review_row,
        )
    }

    async fn write_review(
        &self,
        ctx: RequestContext,
        args: ProductReviewsWriteReviewArgs,
    ) -> Result<types::ReviewView, ApiError> {
        let author = shopper(&ctx)?;
        let product = uuid_text(&args.product_id);
        let conn = self.conn();
        let listed = conn
            .query_row(
                "SELECT 1 FROM product WHERE id = ?1",
                [&product],
                |_| Ok(()),
            )
            .optional()
            .map_err(internal)?;
        if listed.is_none() {
            return Err(ApiError::not_found("product not found"));
        }
        // shop-db's one_per_author index refuses a shopper's second review
        // of a product, among the reviews not deleted.
        conn.query_row(
            "INSERT INTO review (product_id, author_id, rating, title, body) VALUES (?1, ?2, ?3, ?4, ?5)
            RETURNING id, rating, title, body, created_at",
            params![product, author, args.input.rating, args.input.title, args.input.body],
            review_row,
        )
        .map_err(|err| match refusal(&err) {
            Some(ffi::SQLITE_CONSTRAINT_UNIQUE) => ApiError::conflict("you have already reviewed this product"),
            Some(ffi::SQLITE_CONSTRAINT_FOREIGNKEY) => ApiError::forbidden("the caller is not a user of the shop"),
            _ => internal(err),
        })
    }
}
