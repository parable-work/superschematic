//! The implementation of the shop-stock API (D57): how many units of each
//! product the warehouse holds, in shop-db's `Inventory`, an engine type
//! whose `Stock` behavior counts them (D70). It calls the engine in-process,
//! through the `Background` its `Deps` holds, as the shop's stock service
//! for the member of staff a request authenticates, or as itself: shop-db's
//! engine policy lets shop-stock read and write Inventory, and Stock's own
//! permission, `stock.write`, lets a count or a delivery through. An
//! Inventory's id is its product's. shop-stack's generated server for
//! shop-stock calls `create` with the server's `Deps`, and its identity
//! runtime and service authenticator admit each caller before a method
//! runs.
//!
//! A sale shop-orders reports that leaves a product low (`Stock.low`, its
//! available units at or below its `reorderAt`) enqueues `StockLow` on
//! shop-db's queue, through the runtime's queue client and the descriptor
//! shop-db's Rust types carry (D65). shop-stock's worker `ReorderStock`, a
//! process of its own whose generated entrypoint calls `create_workers`,
//! files a restock request in shop-db's `restock_request` table for each
//! message; its job `OrderRestocks`, whose entrypoint calls `create_jobs`,
//! files one for each Inventory the engine lists low with none open, and
//! orders the open ones.
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
    BoxError, Deps, Handlers, Jobs, StockGetStockArgs, StockImplementation, StockReceiveStockArgs,
    StockSalesImplementation, StockSalesStockSoldArgs, StockSetStockArgs, Workers,
};
use async_trait::async_trait;
use http::StatusCode;
use serde_json::{json, Value};
use superschematic_engine::{calls, Principal, PrincipalService};
use superschematic_http_runtime::postgres::deadpool_postgres::Object;
use superschematic_http_runtime::postgres::tokio_postgres::error::SqlState;
use superschematic_http_runtime::queue::{self, QueueDefinition};
use superschematic_http_runtime::{ApiError, RequestContext, Shutdown};

/// The engine namespace shop-db's engine types live in, and the type that
/// counts a product's units.
const NAMESPACE: &str = "shop-db";
const INVENTORY: &str = "Inventory";

/// The deployable shop-stock's in-process calls name to the engine's
/// policy (D37), and the permission Stock's movements take.
const SERVICE: &str = "shop-stock";
const STOCK_WRITE: &str = "stock.write";

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

/// shop-stock's own principal: the shop's stock service, with Stock's
/// permission, for the calls no member of staff makes.
fn service_principal() -> Principal {
    Principal::new(SERVICE, [STOCK_WRITE]).with_service(service())
}

/// A member of staff's call through shop-stock: they are the actor each
/// movement records, with the permissions their roles grant.
fn staff_principal(ctx: &RequestContext) -> Result<Principal, ApiError> {
    let user = ctx
        .principal
        .as_ref()
        .ok_or_else(|| ApiError::unauthorized("sign in to change the stock"))?;
    Ok(Principal::new(user.subject.clone(), user.permissions.clone()).with_service(service()))
}

fn service() -> PrincipalService {
    PrincipalService {
        deployable: SERVICE.to_owned(),
        serves: vec![SERVICE.to_owned()],
        subject: SERVICE.to_owned(),
        stands_in: false,
    }
}

/// Makes an engine call off the async threads, as `principal`, with its
/// arguments after the principal, the namespace's options last. A refusal
/// is the API's answer: a behavior's veto 409 with its code and details
/// (`below_held`, ...), a missing instance 404; an engine not open yet is
/// 503.
async fn engine_call(
    deps: &Deps,
    principal: Principal,
    op: &'static str,
    args: Vec<Value>,
) -> Result<Option<Value>, ApiError> {
    let engine = deps.engine.wait().await.map_err(|err| {
        tracing::warn!(parent: &deps.span, error = %err, "shop-db's engine is not open");
        ApiError::service_unavailable("the stock is not open yet")
    })?;
    let span = deps.span.clone();
    tokio::task::spawn_blocking(move || calls::call(&engine, op, Some(&principal), &args))
        .await
        .map_err(|err| failed_in(&span, err))?
        .map_err(|err| {
            let Some(refused) = err.engine() else {
                return failed_in(&span, err);
            };
            let details = refused.details();
            let code = details
                .as_ref()
                .and_then(|details| details.get("vetoCode"))
                .and_then(Value::as_str)
                .unwrap_or(refused.code().as_str())
                .to_owned();
            let status =
                StatusCode::from_u16(refused.status()).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
            let error = ApiError::new(status, code, refused.to_string());
            match details {
                Some(details) => error.with_details(details),
                None => error,
            }
        })
}

/// A failure that is no fault of the caller's: logged, and answered 500
/// without its detail.
fn failed_in(span: &tracing::Span, err: impl Display) -> ApiError {
    tracing::error!(parent: span, error = %err, "stock call failed");
    ApiError::internal("the stock failed")
}

fn failed(deps: &Deps, err: impl Display) -> ApiError {
    failed_in(&deps.span, err)
}

/// The options every call names the namespace in.
fn in_namespace() -> Value {
    json!({ "namespace": NAMESPACE })
}

/// A product's Inventory, read as `principal`; none for one never stocked.
async fn inventory(
    deps: &Deps,
    principal: Principal,
    product: &IdentityUUID,
) -> Result<Option<Value>, ApiError> {
    let found = engine_call(
        deps,
        principal,
        "instances.get",
        vec![json!(INVENTORY), json!(product.to_string()), in_namespace()],
    )
    .await?;
    Ok(found.filter(|found| !found.is_null()))
}

/// An Inventory as a caller sees it; none for a record whose id is no
/// product's.
fn view(record: &Value) -> Option<types::StockView> {
    let stock = &record["behaviors"]["Stock"];
    let count = |name: &str| stock[name].as_i64().unwrap_or(0);
    Some(types::StockView {
        product_id: record["id"].as_str()?.parse().ok()?,
        on_hand: count("onHand"),
        held: count("held"),
        available: count("available"),
        reorder_at: record["data"]["reorderAt"].as_i64(),
        low: stock["low"].as_bool().unwrap_or(false),
    })
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

/// A connection from shop-db's pool, or 503 when none can be opened.
async fn connection(deps: &Deps) -> Result<Object, ApiError> {
    deps.db.get().await.map_err(|err| {
        tracing::error!(parent: &deps.span, error = %err, "no connection to shop-db");
        ApiError::service_unavailable("the stock database is unavailable")
    })
}

/// Implements the stock namespace, which StockQueries and StockMutations
/// share.
pub struct Stock {
    deps: Arc<Deps>,
}

#[async_trait]
impl StockImplementation for Stock {
    /// Every product the warehouse stocks, a page of the engine's at a
    /// time.
    async fn list_stock(&self, _ctx: RequestContext) -> Result<Vec<types::StockView>, ApiError> {
        let mut levels = Vec::new();
        let mut cursor: Option<String> = None;
        loop {
            let mut options = json!({ "namespace": NAMESPACE, "limit": 500 });
            if let Some(cursor) = &cursor {
                options["cursor"] = json!(cursor);
            }
            let page = engine_call(
                &self.deps,
                service_principal(),
                "instances.list",
                vec![json!(INVENTORY), options],
            )
            .await?
            .unwrap_or(Value::Null);
            levels.extend(
                page["items"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(view),
            );
            match page["next"].as_str() {
                Some(next) if !next.is_empty() => cursor = Some(next.to_owned()),
                _ => break,
            }
        }
        levels.sort_by_key(|level| level.product_id.to_string());
        Ok(levels)
    }

    /// A product the warehouse has never stocked holds nothing.
    async fn get_stock(
        &self,
        _ctx: RequestContext,
        args: StockGetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let found = inventory(&self.deps, service_principal(), &args.product_id).await?;
        Ok(found
            .as_ref()
            .and_then(view)
            .unwrap_or_else(|| never_stocked(args.product_id)))
    }

    /// Counts the product's shelf: its Inventory's countStock, which
    /// records the count as a movement, and is refused below what
    /// checkouts hold (409 below_held). A product never stocked gets its
    /// Inventory first, at the price shop-db's product table gives it,
    /// which each count brings up to date, so a checkout holds a line at
    /// what the product sells for now. A reorderAt left out keeps the
    /// Inventory's; zero turns it off.
    async fn set_stock(
        &self,
        ctx: RequestContext,
        args: StockSetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let principal = staff_principal(&ctx)?;
        let product = args.product_id;
        let client = connection(&self.deps).await?;
        let row = client
            .query_opt(
                "SELECT sku, price_cents FROM product WHERE id = $1",
                &[product.as_uuid()],
            )
            .await
            .map_err(|err| failed(&self.deps, err))?
            .ok_or_else(|| ApiError::not_found(format!("no product has id {product}")))?;
        let sku: String = row.get("sku");
        let price: i64 = row.get("price_cents");
        let reorder_at = args
            .input
            .reorder_at
            .map(|at| if at == 0 { Value::Null } else { json!(at) });
        match inventory(&self.deps, principal.clone(), &product).await? {
            None => {
                let mut data = json!({ "sku": sku, "priceCents": price });
                if let Some(at) = &reorder_at {
                    data["reorderAt"] = at.clone();
                }
                engine_call(
                    &self.deps,
                    principal.clone(),
                    "instances.create",
                    vec![
                        json!(INVENTORY),
                        data,
                        json!({ "namespace": NAMESPACE, "id": product.to_string() }),
                    ],
                )
                .await?;
            }
            Some(_) => {
                let mut patch = json!({ "sku": sku, "priceCents": price });
                if let Some(at) = &reorder_at {
                    patch["reorderAt"] = at.clone();
                }
                engine_call(
                    &self.deps,
                    principal.clone(),
                    "instances.update",
                    vec![
                        json!(INVENTORY),
                        json!(product.to_string()),
                        patch,
                        in_namespace(),
                    ],
                )
                .await?;
            }
        }
        let reason = format!("counted by {}", principal.subject);
        engine_call(
            &self.deps,
            principal.clone(),
            "instances.invoke",
            vec![
                json!(INVENTORY),
                json!(product.to_string()),
                json!("countStock"),
                json!({ "onHand": args.input.quantity, "reason": reason }),
                in_namespace(),
            ],
        )
        .await?;
        inventory(&self.deps, principal, &product)
            .await?
            .as_ref()
            .and_then(view)
            .ok_or_else(|| ApiError::not_found(format!("no stock of product {product}")))
    }

    /// Books a delivery in: the Inventory's receiveStock, with its reason.
    /// A product never counted has no Inventory to receive into (404).
    async fn receive_stock(
        &self,
        ctx: RequestContext,
        args: StockReceiveStockArgs,
    ) -> Result<types::StockView, ApiError> {
        let principal = staff_principal(&ctx)?;
        let product = args.product_id;
        if inventory(&self.deps, principal.clone(), &product)
            .await?
            .is_none()
        {
            return Err(ApiError::not_found(format!(
                "the warehouse has never stocked product {product}: count it first"
            )));
        }
        engine_call(
            &self.deps,
            principal.clone(),
            "instances.invoke",
            vec![
                json!(INVENTORY),
                json!(product.to_string()),
                json!("receiveStock"),
                json!({ "quantity": args.input.quantity, "reason": args.input.reason }),
                in_namespace(),
            ],
        )
        .await?;
        inventory(&self.deps, principal, &product)
            .await?
            .as_ref()
            .and_then(view)
            .ok_or_else(|| ApiError::not_found(format!("no stock of product {product}")))
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
        let mut sold = BTreeMap::new();
        for line in &args.input.lines {
            *sold.entry(line.product_id.to_string()).or_insert(0) += line.quantity;
        }
        let mut levels = Vec::with_capacity(sold.len());
        for product in sold.keys() {
            let product: IdentityUUID = product
                .parse()
                .map_err(|_| ApiError::bad_request(format!("{product} is no product id")))?;
            if let Some(found) = inventory(&self.deps, service_principal(), &product).await? {
                if let Some(level) = view(&found) {
                    levels.push((level, found["data"]["reorderAt"].as_i64()));
                }
            }
        }
        let mut client = connection(&self.deps).await?;
        let tx = client
            .transaction()
            .await
            .map_err(|err| failed(&self.deps, err))?;
        for (level, reorder_at) in &levels {
            let (true, Some(threshold)) = (level.low, *reorder_at) else {
                continue;
            };
            let low = database_types::StockLow {
                product_id: level.product_id.clone(),
                quantity: level.available,
                threshold,
            };
            queue::enqueue(&tx, &self.stock_low, &low)
                .await
                .map_err(|err| failed(&self.deps, err))?;
        }
        tx.commit().await.map_err(|err| failed(&self.deps, err))?;
        tracing::info!(
            parent: &self.deps.span,
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
/// per product. Returns whether it filed one; none for a product deleted
/// since, which has no stock to restock.
async fn file_restock(
    deps: &Deps,
    client: &Object,
    product: &IdentityUUID,
    quantity: i64,
) -> Result<bool, BoxError> {
    let filed = client
        .execute(
            "INSERT INTO restock_request (product_id, open_product_id, status, quantity) \
             VALUES ($1, $1, 'open', $2) \
             ON CONFLICT (open_product_id) DO NOTHING",
            &[product.as_uuid(), &quantity],
        )
        .await;
    match filed {
        Ok(n) => Ok(n > 0),
        Err(err) if err.code() == Some(&SqlState::FOREIGN_KEY_VIOLATION) => {
            tracing::info!(parent: &deps.span, product = %product.as_uuid(), "no product to restock");
            Ok(false)
        }
        Err(err) => Err(err.into()),
    }
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
    /// The supplier's order run. It lists the Inventory the engine finds
    /// low (`Stock.low`), and files a restock request for each with none
    /// open, which catches a count or an adjustment that ran a product low
    /// with no sale; then it orders each request it finds open, oldest
    /// first, each in a statement of its own, so a run that stops part-way
    /// keeps the orders it placed, and the next run places the rest. Once
    /// `shutdown` is cancelled, at the job's timeout or on SIGTERM, it
    /// orders no more and fails the run.
    async fn order_restocks(&self, shutdown: Shutdown) -> Result<(), BoxError> {
        let client = self.deps.db.get().await?;
        let low = engine_call(
            &self.deps,
            service_principal(),
            "instances.list",
            vec![
                json!(INVENTORY),
                json!({ "namespace": NAMESPACE, "limit": 500, "where": { "Stock.low": true } }),
            ],
        )
        .await
        .map_err(|err| format!("list the low stock: {}", err.message))?
        .unwrap_or(Value::Null);
        let mut filed = 0;
        for level in low["items"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(view)
        {
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
            filed,
            orders = ordered,
            "placed the open restock orders"
        );
        Ok(())
    }
}
