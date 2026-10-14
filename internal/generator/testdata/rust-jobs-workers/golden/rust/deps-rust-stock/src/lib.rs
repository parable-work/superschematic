//! The implementation of the deps-rust-stock API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.
//!
//! Each job fails until you implement it.
//!
//! Each worker fails every message until you implement it.

use std::sync::Arc;

use async_trait::async_trait;
use schemas_deps_rust_stock_api::types;
use schemas_deps_rust_stock_api::{
    database_types, BoxError, Deps, Handlers, Jobs, StockGetStockArgs, StockImplementation, Workers,
};
use superschematic_http_runtime::{ApiError, RequestContext, Shutdown};

/// Builds the implementation of deps-rust-stock from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        stock: Arc::new(Stock {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements StockImplementation.
pub struct Stock {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl StockImplementation for Stock {
    async fn get_stock(
        &self,
        _ctx: RequestContext,
        _args: StockGetStockArgs,
    ) -> Result<types::StockView, ApiError> {
        Err(ApiError::not_implemented(
            "stock.get_stock is not implemented",
        ))
    }
}

/// Builds the jobs of deps-rust-stock from its dependencies. A job's entrypoint
/// calls one method per run, with a `Shutdown` that SIGTERM triggers; the run
/// fails when the method returns an error.
pub async fn create_jobs(deps: Deps) -> Result<Arc<dyn Jobs>, BoxError> {
    Ok(Arc::new(DepsRustStockJobs {
        deps: Arc::new(deps),
    }))
}

/// Implements Jobs.
pub struct DepsRustStockJobs {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl Jobs for DepsRustStockJobs {
    /// Runs the job PlaceRestockOrders.
    async fn place_restock_orders(&self, _shutdown: Shutdown) -> Result<(), BoxError> {
        Err("job PlaceRestockOrders is not implemented".into())
    }

    /// Runs the job ReconcileStock.
    async fn reconcile_stock(&self, _shutdown: Shutdown) -> Result<(), BoxError> {
        Err("job ReconcileStock is not implemented".into())
    }
}

/// Builds the workers of deps-rust-stock from its dependencies. A worker's
/// entrypoint calls one method per message it claims; the message is done
/// when the method returns `Ok`, and fails, to be retried, when it returns
/// an error. A message may come again, so a method is idempotent.
pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(DepsRustStockWorkers {
        deps: Arc::new(deps),
    }))
}

/// Implements Workers.
pub struct DepsRustStockWorkers {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl Workers for DepsRustStockWorkers {
    /// Handles a message of the queue Recount.
    async fn recount_stock(&self, _message: database_types::Recount) -> Result<(), BoxError> {
        Err("worker RecountStock is not implemented".into())
    }

    /// Handles a message of the queue StockLow.
    async fn reorder_stock(&self, _message: database_types::StockLow) -> Result<(), BoxError> {
        Err("worker ReorderStock is not implemented".into())
    }
}
