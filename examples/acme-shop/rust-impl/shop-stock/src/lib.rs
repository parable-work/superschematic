//! The implementation of the shop-stock API (D57): how many units of each
//! product the warehouse holds, in shop-db's `stock_level` table. There is
//! no Rust ORM, so it reads and writes the table with tokio-postgres,
//! which the HTTP runtime re-exports, over the pool its `Deps` holds.
//! shop-stack's generated server for shop-stock calls `create` with the
//! server's `Deps`, and its identity runtime and service authenticator
//! admit each caller before a method runs.
//!
//! The build scaffolded this crate once, at the naming file's
//! `[implementation_paths] rust` template, `rust-impl/{service}`; it is
//! the shop's from then on.

use std::collections::BTreeMap;
use std::fmt::Display;
use std::sync::Arc;

use acme_shop_stock_api::types::{self, IdentityUUID};
use acme_shop_stock_api::{
    Deps, Handlers, StockGetStockArgs, StockImplementation, StockReservationsImplementation,
    StockReservationsReserveStockArgs, StockSetStockArgs,
};
use async_trait::async_trait;
use superschematic_http_runtime::postgres::deadpool_postgres::Object;
use superschematic_http_runtime::postgres::tokio_postgres::error::SqlState;
use superschematic_http_runtime::postgres::tokio_postgres::Row;
use superschematic_http_runtime::{ApiError, RequestContext};

/// Builds the implementation of shop-stock from its dependencies.
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        stock: Arc::new(Stock {
            deps: Arc::clone(&deps),
        }),
        stock_reservations: Arc::new(StockReservations { deps }),
    })
}

/// A row of `stock_level` as a caller sees it.
fn view(row: &Row) -> types::StockView {
    types::StockView {
        product_id: IdentityUUID::from_uuid(row.get("product_id")),
        quantity: row.get("quantity"),
    }
}

/// A connection from shop-db's pool, or 503 when none can be opened.
async fn connection(deps: &Deps) -> Result<Object, ApiError> {
    deps.db.get().await.map_err(|err| {
        tracing::error!(parent: &deps.span, error = %err, "no connection to shop-db");
        ApiError::service_unavailable("the stock database is unavailable")
    })
}

/// A query that failed for no fault of the caller's: logged, and answered
/// 500 without its detail.
fn failed(deps: &Deps, err: impl Display) -> ApiError {
    tracing::error!(parent: &deps.span, error = %err, "stock query failed");
    ApiError::internal("the stock database failed")
}

/// Implements the stock namespace, which StockQueries and StockMutations
/// share.
pub struct Stock {
    deps: Arc<Deps>,
}

#[async_trait]
impl StockImplementation for Stock {
    async fn list_stock(&self, _ctx: RequestContext) -> Result<Vec<types::StockView>, ApiError> {
        let client = connection(&self.deps).await?;
        let rows = client
            .query(
                "SELECT product_id, quantity FROM stock_level ORDER BY product_id",
                &[],
            )
            .await
            .map_err(|err| failed(&self.deps, err))?;
        Ok(rows.iter().map(view).collect())
    }

    /// A product the warehouse has never stocked has none.
    async fn get_stock(
        &self,
        _ctx: RequestContext,
        args: StockGetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let client = connection(&self.deps).await?;
        let row = client
            .query_opt(
                "SELECT product_id, quantity FROM stock_level WHERE product_id = $1",
                &[args.product_id.as_uuid()],
            )
            .await
            .map_err(|err| failed(&self.deps, err))?;
        Ok(row.as_ref().map(view).unwrap_or(types::StockView {
            product_id: args.product_id,
            quantity: 0,
        }))
    }

    /// Writes the product's one row, which the table's one_per_product
    /// index keeps to one.
    async fn set_stock(
        &self,
        _ctx: RequestContext,
        args: StockSetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let client = connection(&self.deps).await?;
        let row = client
            .query_one(
                "INSERT INTO stock_level (product_id, quantity) VALUES ($1, $2) \
                 ON CONFLICT (product_id) DO UPDATE \
                 SET quantity = EXCLUDED.quantity, updated_at = CURRENT_TIMESTAMP \
                 RETURNING product_id, quantity",
                &[args.product_id.as_uuid(), &args.input.quantity],
            )
            .await
            .map_err(|err| {
                if err.code() == Some(&SqlState::FOREIGN_KEY_VIOLATION) {
                    ApiError::not_found(format!("no product has id {}", args.product_id))
                } else {
                    failed(&self.deps, err)
                }
            })?;
        Ok(view(&row))
    }
}

/// Implements the stock-reservations namespace, which only shop-orders'
/// server reaches: the router has verified its service credential and the
/// shopper it forwards.
pub struct StockReservations {
    deps: Arc<Deps>,
}

#[async_trait]
impl StockReservationsImplementation for StockReservations {
    /// Takes each line's units off the shelf in one transaction, so a line
    /// the warehouse cannot fill reserves none of the others. The rows are
    /// locked in product order, so two reservations of the same products
    /// queue rather than deadlock; two lines for one product reserve as
    /// one.
    async fn reserve_stock(
        &self,
        ctx: RequestContext,
        args: StockReservationsReserveStockArgs,
    ) -> Result<Vec<types::StockView>, ApiError> {
        let mut wanted = BTreeMap::new();
        for line in &args.input.lines {
            *wanted.entry(line.product_id.to_uuid()).or_insert(0) += line.quantity;
        }
        let mut client = connection(&self.deps).await?;
        let tx = client
            .transaction()
            .await
            .map_err(|err| failed(&self.deps, err))?;
        let mut levels = Vec::with_capacity(wanted.len());
        for (product, quantity) in &wanted {
            let row = tx
                .query_opt(
                    "UPDATE stock_level \
                     SET quantity = quantity - $2, updated_at = CURRENT_TIMESTAMP \
                     WHERE product_id = $1 AND quantity >= $2 \
                     RETURNING product_id, quantity",
                    &[product, quantity],
                )
                .await
                .map_err(|err| failed(&self.deps, err))?;
            if let Some(row) = row {
                levels.push(view(&row));
                continue;
            }
            let held: i64 = tx
                .query_opt(
                    "SELECT quantity FROM stock_level WHERE product_id = $1",
                    &[product],
                )
                .await
                .map_err(|err| failed(&self.deps, err))?
                .map_or(0, |row| row.get(0));
            // Dropping the transaction rolls back the lines before this one.
            return Err(ApiError::conflict(format!(
                "the warehouse holds {held} of product {}, and the order asks for {quantity}",
                IdentityUUID::from_uuid(*product)
            )));
        }
        tx.commit().await.map_err(|err| failed(&self.deps, err))?;
        tracing::info!(
            parent: &self.deps.span,
            caller = ctx.service_caller.map(|caller| caller.deployable).unwrap_or_default(),
            user = ctx.principal.map(|user| user.subject).unwrap_or_default(),
            products = levels.len(),
            "reserved stock"
        );
        Ok(levels)
    }
}
