//! The implementation of the shop-stock API (D57): how many units of each
//! product the warehouse holds, in shop-db's `Inventory` table, whose
//! `Stock` behavior counts them (D70, D103). A row's `stock_on_hand` and
//! `stock_held` are columns the behavior owns, which only its operations
//! change; what is available, and whether it is low, is computed from them
//! here. It runs Stock's operations through the repositories shop-db's
//! Rust types carry, in a transaction of the pool its `Deps` holds, as the
//! member of staff a request authenticates: Stock's own permission,
//! `stock.write`, which the behaviors' core checks, lets a count or a
//! delivery through. An Inventory's key is its product's id. shop-stack's
//! generated server for shop-stock calls `create` with the server's
//! `Deps`, and its identity runtime and service authenticator admit each
//! caller before a method runs.
//!
//! A sale shop-orders reports that leaves a product low (its available
//! units at or below its `reorderAt`) enqueues `StockLow` on shop-db's
//! queue, through the runtime's queue client and the descriptor shop-db's
//! Rust types carry (D65). shop-stock's worker `ReorderStock`, a process of
//! its own whose generated entrypoint calls `create_workers`, files a
//! restock request in shop-db's `restock_request` table for each message;
//! its job `OrderRestocks`, whose entrypoint calls `create_jobs`, files one
//! for each Inventory it reads low with none open, and orders the open
//! ones.
//!
//! No statement here is written by hand: it reads shop-db's products and
//! inventory, runs Stock's operations, and reads and writes its restock
//! requests through the repositories.
//!
//! The build scaffolded this crate once, at the naming file's
//! `[implementation_paths] rust` template, `rust-impl/{service}`; it is
//! the shop's from then on.

use std::collections::BTreeMap;
use std::fmt::Display;
use std::sync::Arc;

use acme_shop_stock_api::database_types::orm::postgres::Repositories;
use acme_shop_stock_api::database_types::orm::{
    InventoryCreate, InventoryRow, InventoryStockCountStockParams,
    InventoryStockReceiveStockParams, InventoryUpdate, RestockRequestCreate,
    RestockRequestFilter, RestockRequestUpdate,
};
use acme_shop_stock_api::database_types::{self, RestockStatus, STOCK_LOW_QUEUE};
use acme_shop_stock_api::types::{self, IdentityUUID};
use acme_shop_stock_api::{
    BoxError, Deps, Handlers, Jobs, StockGetStockArgs, StockImplementation, StockReceiveStockArgs,
    StockSalesImplementation, StockSalesStockSoldArgs, StockSetStockArgs, Workers,
};
use async_trait::async_trait;
use http::StatusCode;
use serde_json::json;
use superschematic_http_runtime::orm::{
    self, Caller, ConstraintKind, Equals, FindOptions, OrderBy, PageOptions, Text, WriteOptions,
};
use superschematic_http_runtime::postgres::deadpool_postgres::Object;
use superschematic_http_runtime::queue::{self, QueueDefinition};
use superschematic_http_runtime::{ApiError, RequestContext, Shutdown};

/// Builds the implementation of shop-stock from its dependencies.
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        stock: Arc::new(Stock {
            deps: Arc::clone(&deps),
        }),
        stock_sales: Arc::new(StockSales {
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

/// A member of staff's call through shop-stock: the caller Stock's
/// operations run as, whom each movement records, with the permissions
/// their roles grant, which the core checks against Stock's permission.
fn staff_caller(ctx: &RequestContext) -> Result<Caller, ApiError> {
    if ctx.principal.is_none() {
        return Err(ApiError::unauthorized("sign in to change the stock"));
    }
    Ok(Caller::of(ctx.principal.as_ref()))
}

/// A repository's error as the API's answer: a behavior's refusal is its
/// status with its code, a veto's code (`below_held`, ...) for a veto, and
/// its details; a missing row 404; anything else the server's own failure.
fn refused(deps: &Deps, err: orm::Error) -> ApiError {
    match err {
        orm::Error::Refused(refusal) => {
            let code = refusal
                .details
                .get("vetoCode")
                .and_then(|code| code.as_str())
                .unwrap_or(&refusal.code)
                .to_owned();
            let status =
                StatusCode::from_u16(refusal.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
            let error = ApiError::new(status, code, refusal.message.clone());
            if refusal.details.is_null() {
                error
            } else {
                error.with_details(refusal.details)
            }
        }
        orm::Error::NotFound(what) => ApiError::not_found(what),
        err => failed(deps, err),
    }
}

/// A failure that is no fault of the caller's: logged, and answered 500
/// without its detail.
fn failed(deps: &Deps, err: impl Display) -> ApiError {
    tracing::error!(parent: &deps.span, error = %err, "stock call failed");
    ApiError::internal("the stock failed")
}

/// A connection from shop-db's pool, or 503 when none can be opened.
async fn connection(deps: &Deps) -> Result<Object, ApiError> {
    deps.db.get().await.map_err(|err| {
        tracing::error!(parent: &deps.span, error = %err, "no connection to shop-db");
        ApiError::service_unavailable("the stock database is unavailable")
    })
}

/// A product's Inventory row; none for one never stocked.
async fn inventory(deps: &Deps, client: &Object, product: &IdentityUUID) -> Result<Option<InventoryRow>, ApiError> {
    let repositories = Repositories::new(client).map_err(|err| failed(deps, err))?;
    match repositories.inventory().map_err(|err| failed(deps, err))?.get_one(product).await {
        Ok(row) => Ok(Some(row)),
        Err(orm::Error::NotFound(_)) => Ok(None),
        Err(err) => Err(failed(deps, err)),
    }
}

/// An Inventory as a caller sees it: Stock's counts, what is left to sell,
/// and whether that is at or below its reorder level.
fn view(row: &InventoryRow) -> types::StockView {
    let on_hand = row.stock_on_hand.unwrap_or(0);
    let held = row.stock_held.unwrap_or(0);
    let available = on_hand - held;
    types::StockView {
        product_id: row.id,
        on_hand,
        held,
        available,
        reorder_at: row.reorder_at,
        low: row.reorder_at.is_some_and(|at| available <= at),
    }
}

/// What a product the warehouse has never stocked holds: nothing.
fn never_stocked(product: IdentityUUID) -> types::StockView {
    types::StockView {
        product_id: product,
        on_hand: 0,
        held: 0,
        available: 0,
        reorder_at: None,
        low: false,
    }
}

/// Every Inventory row, a page at a time, in key order.
async fn every_inventory(deps: &Deps, client: &Object) -> Result<Vec<InventoryRow>, ApiError> {
    let repositories = Repositories::new(client).map_err(|err| failed(deps, err))?;
    let rows = repositories.inventory().map_err(|err| failed(deps, err))?;
    let mut all = Vec::new();
    let mut after = None;
    loop {
        let page = rows
            .find_page(
                None,
                &PageOptions {
                    after: after.take(),
                    limit: Some(500),
                    ..Default::default()
                },
            )
            .await
            .map_err(|err| failed(deps, err))?;
        all.extend(page.rows);
        match page.next {
            Some(next) => after = Some(next),
            None => return Ok(all),
        }
    }
}

/// Implements the stock namespace, which StockQueries and StockMutations
/// share.
pub struct Stock {
    deps: Arc<Deps>,
}

#[async_trait]
impl StockImplementation for Stock {
    /// Every product the warehouse stocks.
    async fn list_stock(&self, _ctx: RequestContext) -> Result<Vec<types::StockView>, ApiError> {
        let client = connection(&self.deps).await?;
        let mut levels: Vec<_> = every_inventory(&self.deps, &client)
            .await?
            .iter()
            .map(view)
            .collect();
        levels.sort_by_key(|level| level.product_id.to_string());
        Ok(levels)
    }

    /// A product the warehouse has never stocked holds nothing.
    async fn get_stock(
        &self,
        _ctx: RequestContext,
        args: StockGetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let client = connection(&self.deps).await?;
        Ok(inventory(&self.deps, &client, &args.product_id)
            .await?
            .as_ref()
            .map(view)
            .unwrap_or_else(|| never_stocked(args.product_id)))
    }

    /// Counts the product's shelf: its Inventory's countStock, which
    /// records the count as a movement, and is refused below what
    /// checkouts hold (409 below_held). A product never stocked gets its
    /// Inventory row first, at the price shop-db's product table gives it,
    /// which each count brings up to date, so a checkout holds a line at
    /// what the product sells for now. A reorderAt left out keeps the
    /// Inventory's; zero turns it off. All of it in one transaction.
    async fn set_stock(
        &self,
        ctx: RequestContext,
        args: StockSetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let caller = staff_caller(&ctx)?;
        let deps = &self.deps;
        let product = args.product_id;
        let mut client = connection(deps).await?;
        let tx = client.transaction().await.map_err(|err| failed(deps, err))?;
        let repositories = Repositories::new(&tx)
            .map_err(|err| failed(deps, err))?
            .acting(orm::routes::acting(ctx.principal.as_ref()))
            .with_caller(caller.clone());
        let found = repositories
            .product()
            .map_err(|err| failed(deps, err))?
            .get_one(&product)
            .await;
        let (sku, price) = match found {
            Ok(row) => (row.sku, row.price_cents),
            Err(orm::Error::NotFound(_)) => {
                return Err(ApiError::not_found(format!("no product has id {product}")))
            }
            Err(err) => return Err(failed(deps, err)),
        };
        let reorder_at = args
            .input
            .reorder_at
            .map(|at| if at == 0 { None } else { Some(at) });
        let inventories = repositories.inventory().map_err(|err| failed(deps, err))?;
        match inventories.get_one(&product).await {
            Err(orm::Error::NotFound(_)) => {
                inventories
                    .create_one(&InventoryCreate {
                        id: Some(product),
                        sku: sku.to_string(),
                        price_cents: price,
                        reorder_at: reorder_at.flatten(),
                    })
                    .await
                    .map_err(|err| refused(deps, err))?;
            }
            Ok(_) => {
                inventories
                    .update_one(
                        &product,
                        &InventoryUpdate {
                            sku: Some(sku.to_string()),
                            price_cents: Some(price),
                            reorder_at,
                        },
                        &WriteOptions::default(),
                    )
                    .await
                    .map_err(|err| refused(deps, err))?;
            }
            Err(err) => return Err(failed(deps, err)),
        }
        repositories
            .inventory_stock()
            .map_err(|err| failed(deps, err))?
            .count_stock(
                &product,
                &InventoryStockCountStockParams {
                    on_hand: args.input.quantity,
                    reason: format!("counted by {}", caller.subject),
                },
                &WriteOptions::default(),
            )
            .await
            .map_err(|err| refused(deps, err))?;
        let row = inventories
            .get_one(&product)
            .await
            .map_err(|err| refused(deps, err))?;
        tx.commit().await.map_err(|err| failed(deps, err))?;
        Ok(view(&row))
    }

    /// Books a delivery in: the Inventory's receiveStock, with its reason.
    /// A product never counted has no Inventory to receive into (404).
    async fn receive_stock(
        &self,
        ctx: RequestContext,
        args: StockReceiveStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let caller = staff_caller(&ctx)?;
        let deps = &self.deps;
        let product = args.product_id;
        let mut client = connection(deps).await?;
        let tx = client.transaction().await.map_err(|err| failed(deps, err))?;
        let repositories = Repositories::new(&tx)
            .map_err(|err| failed(deps, err))?
            .acting(orm::routes::acting(ctx.principal.as_ref()))
            .with_caller(caller);
        let received = repositories
            .inventory_stock()
            .map_err(|err| failed(deps, err))?
            .receive_stock(
                &product,
                &InventoryStockReceiveStockParams {
                    quantity: args.input.quantity,
                    reason: args.input.reason,
                    reference: None,
                },
                &WriteOptions::default(),
            )
            .await;
        match received {
            Err(orm::Error::NotFound(_)) => {
                return Err(ApiError::not_found(format!(
                    "the warehouse has never stocked product {product}: count it first"
                )))
            }
            Err(err) => return Err(refused(deps, err)),
            Ok(_) => {}
        }
        let row = repositories
            .inventory()
            .map_err(|err| failed(deps, err))?
            .get_one(&product)
            .await
            .map_err(|err| refused(deps, err))?;
        tx.commit().await.map_err(|err| failed(deps, err))?;
        Ok(view(&row))
    }
}

/// Implements the stock-sales namespace, which only shop-orders reaches:
/// the router has verified its service credential.
pub struct StockSales {
    deps: Arc<Deps>,
    /// shop-db's StockLow queue, from the descriptor its Rust types hold.
    stock_low: QueueDefinition,
}

#[async_trait]
impl StockSalesImplementation for StockSales {
    /// Reads each product a placed checkout sold, whose Holds committed its
    /// units already, and enqueues StockLow for each the sale left low, in
    /// one transaction, so the messages commit together. ReorderStock files
    /// one open request per product, so a sale reported twice files one.
    async fn stock_sold(
        &self,
        ctx: RequestContext,
        args: StockSalesStockSoldArgs,
    ) -> Result<Vec<types::StockView>, ApiError> {
        let deps = &self.deps;
        let mut sold = BTreeMap::new();
        for line in &args.input.lines {
            *sold.entry(line.product_id.to_string()).or_insert(0) += line.quantity;
        }
        let mut client = connection(deps).await?;
        let mut levels = Vec::with_capacity(sold.len());
        for product in sold.keys() {
            let product: IdentityUUID = product
                .parse()
                .map_err(|_| ApiError::bad_request(format!("{product} is no product id")))?;
            if let Some(row) = inventory(deps, &client, &product).await? {
                levels.push((view(&row), row.reorder_at));
            }
        }
        let tx = client.transaction().await.map_err(|err| failed(deps, err))?;
        for (level, reorder_at) in &levels {
            let (true, Some(threshold)) = (level.low, *reorder_at) else {
                continue;
            };
            let low = database_types::StockLow {
                product_id: level.product_id,
                quantity: level.available,
                threshold,
            };
            queue::enqueue(&tx, &self.stock_low, &low)
                .await
                .map_err(|err| failed(deps, err))?;
        }
        tx.commit().await.map_err(|err| failed(deps, err))?;
        tracing::info!(
            parent: &deps.span,
            caller = ctx.service_caller.map(|caller| caller.deployable).unwrap_or_default(),
            products = levels.len(),
            low = levels.iter().filter(|(level, _)| level.low).count(),
            "took a sale"
        );
        Ok(levels.into_iter().map(|(level, _)| level).collect())
    }
}

/// How many units a restock request asks the supplier for: enough to bring
/// the level back to twice its reorder level.
fn restock_quantity(quantity: i64, threshold: i64) -> i64 {
    threshold.saturating_mul(2).saturating_sub(quantity).max(1)
}

/// Files an open restock request for a product, unless one is open:
/// restock_request's one_open_per_product index admits one open request
/// per product, so the create of a second is refused by that key. Returns
/// whether it filed one; none for a product deleted since, which has no
/// stock to restock.
async fn file_restock(
    deps: &Deps,
    client: &Object,
    product: &IdentityUUID,
    quantity: i64,
) -> Result<bool, BoxError> {
    let filed = Repositories::new(client)?
        .restock_request()?
        .create_one(&RestockRequestCreate {
            id: None,
            product_id: *product,
            status: None,
            quantity,
            open_product_id: Some(*product),
            ordered_at: None,
        })
        .await;
    match filed {
        Ok(_) => Ok(true),
        Err(orm::Error::Constraint(refused)) if refused.kind == ConstraintKind::Unique => Ok(false),
        Err(orm::Error::Constraint(refused)) if refused.kind == ConstraintKind::ForeignKey => {
            tracing::info!(parent: &deps.span, product = %product.as_uuid(), "no product to restock");
            Ok(false)
        }
        Err(err) => Err(err.into()),
    }
}

/// How many open restock requests one run of OrderRestocks reads: a run
/// that leaves some open leaves them to the next.
const ORDER_BATCH: u32 = 500;

/// The time now, as a row holds one.
fn now() -> Result<database_types::TemporalDateTime, BoxError> {
    Ok(serde_json::from_value(json!(orm::rfc3339_millis(
        std::time::SystemTime::now()
    )))?)
}

/// Implements the workers of shop-stock.
pub struct StockWorkers {
    deps: Arc<Deps>,
}

#[async_trait]
impl Workers for StockWorkers {
    /// Files an open restock request for the product a sale ran low, for
    /// enough units to bring its level back to twice its reorder level. A
    /// product has one open request at most, so a message that comes
    /// again, or one for a product whose request is still open, files
    /// none, and is done.
    async fn reorder_stock(&self, message: database_types::StockLow) -> Result<(), BoxError> {
        let product = message.product_id;
        let quantity = restock_quantity(message.quantity, message.threshold);
        let client = self.deps.db.get().await?;
        if file_restock(&self.deps, &client, &product, quantity).await? {
            tracing::info!(
                parent: &self.deps.span,
                product = %product.as_uuid(),
                quantity,
                stock = message.quantity,
                threshold = message.threshold,
                "filed a restock request"
            );
        } else {
            tracing::info!(
                parent: &self.deps.span,
                product = %product.as_uuid(),
                "a restock request is open already"
            );
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
    /// The supplier's order run. It reads every Inventory and files a
    /// restock request for each that is low with none open, which catches
    /// a count or an adjustment that ran a product low with no sale; then
    /// it orders each request it finds open, oldest first, each in a
    /// statement of its own, so a run that stops part-way keeps the orders
    /// it placed, and the next run places the rest. Once `shutdown` is
    /// cancelled, at the job's timeout or on SIGTERM, it orders no more and
    /// fails the run.
    async fn order_restocks(&self, shutdown: Shutdown) -> Result<(), BoxError> {
        let client = self.deps.db.get().await?;
        let low: Vec<_> = every_inventory(&self.deps, &client)
            .await
            .map_err(|err| format!("read the stock: {}", err.message))?
            .iter()
            .map(view)
            .filter(|level| level.low)
            .collect();
        let mut filed = 0;
        for level in low {
            let threshold = level.reorder_at.unwrap_or(0);
            if file_restock(
                &self.deps,
                &client,
                &level.product_id,
                restock_quantity(level.available, threshold),
            )
            .await?
            {
                filed += 1;
            }
        }
        let repositories = Repositories::new(&client)?;
        let requests = repositories.restock_request()?;
        let is_open = || Some(Text::eq(RestockStatus::Open));
        let open = requests
            .find_many(
                Some(&RestockRequestFilter {
                    status: is_open(),
                    ..Default::default()
                }),
                &FindOptions {
                    order_by: vec![OrderBy::asc("createdAt"), OrderBy::asc("id")],
                    limit: Some(ORDER_BATCH),
                    skip_count: true,
                    ..Default::default()
                },
            )
            .await?
            .rows;
        let mut ordered = 0;
        for request in &open {
            if shutdown.is_cancelled() {
                return Err(format!(
                    "stopped after ordering {ordered} of {} open restock requests",
                    open.len()
                )
                .into());
            }
            // A shop would send the order to its supplier here. The write
            // finds the request only while it is open, so a request
            // another run ordered since is ordered once.
            ordered += requests
                .update_many(
                    Some(&RestockRequestFilter {
                        id: Some(Equals::eq(request.id)),
                        status: is_open(),
                        ..Default::default()
                    }),
                    &RestockRequestUpdate {
                        status: Some(RestockStatus::Ordered),
                        open_product_id: Some(None),
                        ordered_at: Some(Some(now()?)),
                        ..Default::default()
                    },
                )
                .await?;
        }
        tracing::info!(
            parent: &self.deps.span,
            filed,
            orders = ordered,
            "placed the open restock orders"
        );
        Ok(())
    }
}
