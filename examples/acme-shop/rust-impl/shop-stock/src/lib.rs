//! The implementation of the shop-stock API (D57): how many units of each
//! product the warehouse holds, in shop-db's `stock_level` table. There is
//! no Rust ORM, so it reads and writes the table with tokio-postgres,
//! which the HTTP runtime re-exports, over the pool its `Deps` holds.
//! shop-stack's generated server for shop-stock calls `create` with the
//! server's `Deps`, and its identity runtime and service authenticator
//! admit each caller before a method runs.
//!
//! A reservation that takes a product's level below its threshold
//! enqueues `StockLow` on shop-db's queue in its transaction, through the
//! runtime's queue client and the descriptor shop-db's Rust types carry
//! (D65). shop-stock's worker `ReorderStock`, a process of its own whose
//! generated entrypoint calls `create_workers`, files a restock request in
//! shop-db's `restock_request` table for each message; its job
//! `OrderRestocks`, whose entrypoint calls `create_jobs`, orders the
//! open ones.
//!
//! The build scaffolded this crate once, at the naming file's
//! `[implementation_paths] rust` template, `rust-impl/{service}`; it is
//! the shop's from then on.

use std::collections::BTreeMap;
use std::fmt::Display;
use std::sync::Arc;

use acme_shop_stock_api::database_types::{self, STOCK_LOW_QUEUE};
use acme_shop_stock_api::types::{self, IdentityUUID};
use acme_shop_stock_api::{
    BoxError, Deps, Handlers, Jobs, StockGetStockArgs, StockImplementation,
    StockReservationsImplementation, StockReservationsReserveStockArgs, StockSetStockArgs, Workers,
};
use async_trait::async_trait;
use superschematic_http_runtime::postgres::deadpool_postgres::Object;
use superschematic_http_runtime::postgres::tokio_postgres::error::SqlState;
use superschematic_http_runtime::postgres::tokio_postgres::Row;
use superschematic_http_runtime::queue::{self, QueueDefinition};
use superschematic_http_runtime::{ApiError, RequestContext, Shutdown};

/// Builds the implementation of shop-stock from its dependencies.
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        stock: Arc::new(Stock {
            deps: Arc::clone(&deps),
        }),
        stock_reservations: Arc::new(StockReservations {
            deps,
            stock_low: QueueDefinition::parse(STOCK_LOW_QUEUE)?,
        }),
    })
}

/// Builds the workers of shop-stock from its dependencies: the generated
/// entrypoint of the worker shop-stock-reorder-stock calls it once, then
/// `reorder_stock` for each message it claims.
pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(StockWorkers {
        deps: Arc::new(deps),
    }))
}

/// Builds the jobs of shop-stock from its dependencies: the generated
/// entrypoint of the job shop-stock-order-restocks calls it, then
/// `order_restocks`, once per run.
pub async fn create_jobs(deps: Deps) -> Result<Arc<dyn Jobs>, BoxError> {
    Ok(Arc::new(StockJobs {
        deps: Arc::new(deps),
    }))
}

/// A row of `stock_level` as a caller sees it.
fn view(row: &Row) -> types::StockView {
    types::StockView {
        product_id: IdentityUUID::from_uuid(row.get("product_id")),
        quantity: row.get("quantity"),
        threshold: row.get("threshold"),
    }
}

/// The threshold a level fell below, when taking `reserved` units off it
/// left `quantity`: one the level was at or above before and is below now.
/// A level with no threshold, or with zero, never falls below it.
fn fell_below(quantity: i64, reserved: i64, threshold: Option<i64>) -> Option<i64> {
    threshold.filter(|&threshold| quantity < threshold && quantity + reserved >= threshold)
}

/// How many units a restock request asks the supplier for: enough to bring
/// the level back to twice its threshold.
fn restock_quantity(quantity: i64, threshold: i64) -> i64 {
    threshold.saturating_mul(2).saturating_sub(quantity).max(1)
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
                "SELECT product_id, quantity, threshold FROM stock_level ORDER BY product_id",
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
                "SELECT product_id, quantity, threshold FROM stock_level WHERE product_id = $1",
                &[args.product_id.as_uuid()],
            )
            .await
            .map_err(|err| failed(&self.deps, err))?;
        Ok(row.as_ref().map(view).unwrap_or(types::StockView {
            product_id: args.product_id,
            quantity: 0,
            threshold: None,
        }))
    }

    /// Writes the product's one row, which the table's one_per_product
    /// index keeps to one. A threshold the input leaves out keeps the
    /// row's.
    async fn set_stock(
        &self,
        _ctx: RequestContext,
        args: StockSetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let client = connection(&self.deps).await?;
        let row = client
            .query_one(
                "INSERT INTO stock_level (product_id, quantity, threshold) VALUES ($1, $2, $3) \
                 ON CONFLICT (product_id) DO UPDATE \
                 SET quantity = EXCLUDED.quantity, \
                 threshold = COALESCE(EXCLUDED.threshold, stock_level.threshold), \
                 updated_at = CURRENT_TIMESTAMP \
                 RETURNING product_id, quantity, threshold",
                &[
                    args.product_id.as_uuid(),
                    &args.input.quantity,
                    &args.input.threshold,
                ],
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
    /// shop-db's StockLow queue, from the descriptor its Rust types hold.
    stock_low: QueueDefinition,
}

#[async_trait]
impl StockReservationsImplementation for StockReservations {
    /// Takes each line's units off the shelf in one transaction, so a line
    /// the warehouse cannot fill reserves none of the others. The rows are
    /// locked in product order, so two reservations of the same products
    /// queue rather than deadlock; two lines for one product reserve as
    /// one. A line that takes its product's level below its threshold
    /// enqueues StockLow in the same transaction, so the message commits
    /// with the reservation, or not at all.
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
                     RETURNING product_id, quantity, threshold",
                    &[product, quantity],
                )
                .await
                .map_err(|err| failed(&self.deps, err))?;
            if let Some(row) = row {
                let level = view(&row);
                if let Some(threshold) = fell_below(level.quantity, *quantity, level.threshold) {
                    let low = database_types::StockLow {
                        product_id: level.product_id,
                        quantity: level.quantity,
                        threshold,
                    };
                    queue::enqueue(&tx, &self.stock_low, &low)
                        .await
                        .map_err(|err| failed(&self.deps, err))?;
                }
                levels.push(level);
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

/// Implements the workers of shop-stock.
pub struct StockWorkers {
    deps: Arc<Deps>,
}

#[async_trait]
impl Workers for StockWorkers {
    /// Files an open restock request for the product a reservation ran
    /// low, for enough units to bring its level back to twice its
    /// threshold. restock_request's one_open_per_product index admits one
    /// open request per product, so a message that comes again, or one for
    /// a product whose request is still open, files none, and is done. So
    /// is one for a product deleted since, which has no stock to restock.
    async fn reorder_stock(&self, message: database_types::StockLow) -> Result<(), BoxError> {
        let product = message.product_id;
        let quantity = restock_quantity(message.quantity, message.threshold);
        let client = self.deps.db.get().await?;
        let filed = client
            .execute(
                "INSERT INTO restock_request (product_id, open_product_id, status, quantity) \
                 VALUES ($1, $1, 'open', $2) \
                 ON CONFLICT (open_product_id) DO NOTHING",
                &[product.as_uuid(), &quantity],
            )
            .await;
        match filed {
            Ok(0) => tracing::info!(
                parent: &self.deps.span,
                product = %product.as_uuid(),
                "a restock request is open already"
            ),
            Ok(_) => tracing::info!(
                parent: &self.deps.span,
                product = %product.as_uuid(),
                quantity,
                stock = message.quantity,
                threshold = message.threshold,
                "filed a restock request"
            ),
            Err(err) if err.code() == Some(&SqlState::FOREIGN_KEY_VIOLATION) => tracing::info!(
                parent: &self.deps.span,
                product = %product.as_uuid(),
                "no product to restock"
            ),
            Err(err) => return Err(err.into()),
        }
        Ok(())
    }
}

/// Implements the jobs of shop-stock.
pub struct StockJobs {
    deps: Arc<Deps>,
}

#[async_trait]
impl Jobs for StockJobs {
    /// The supplier's order run: it orders each restock request it finds
    /// open, oldest first, each in a statement of its own, so a run that
    /// stops part-way keeps the orders it placed, and the next run places
    /// the rest. Once `shutdown` is cancelled, at the job's timeout or on
    /// SIGTERM, it orders no more and fails the run.
    async fn order_restocks(&self, shutdown: Shutdown) -> Result<(), BoxError> {
        let client = self.deps.db.get().await?;
        let open = client
            .query(
                "SELECT id FROM restock_request WHERE status = 'open' ORDER BY created_at, id",
                &[],
            )
            .await?;
        let mut ordered = 0;
        for row in &open {
            if shutdown.is_cancelled() {
                return Err(format!(
                    "stopped after ordering {ordered} of {} open restock requests",
                    open.len()
                )
                .into());
            }
            let id = IdentityUUID::from_uuid(row.get("id"));
            // A shop would send the order to its supplier here.
            ordered += client
                .execute(
                    "UPDATE restock_request \
                     SET status = 'ordered', open_product_id = NULL, \
                     ordered_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP \
                     WHERE id = $1 AND status = 'open'",
                    &[id.as_uuid()],
                )
                .await?;
        }
        tracing::info!(
            parent: &self.deps.span,
            orders = ordered,
            "placed the open restock orders"
        );
        Ok(())
    }
}
