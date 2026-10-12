//! The implementation of the deps-rust-shop API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.

use std::sync::Arc;

use async_trait::async_trait;
use schemas_deps_rust_shop_api::types;
use schemas_deps_rust_shop_api::{
    CartsGetCartArgs, CartsImplementation, Deps, Handlers, ProductsImplementation,
    StockImplementation,
};
use superschematic_http_runtime::{ApiError, RequestContext};

/// Builds the implementation of deps-rust-shop from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        carts: Arc::new(Carts {
            deps: Arc::clone(&deps),
        }),
        products: Arc::new(Products {
            deps: Arc::clone(&deps),
        }),
        stock: Arc::new(Stock {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements CartsImplementation.
pub struct Carts {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl CartsImplementation for Carts {
    async fn get_cart(
        &self,
        _ctx: RequestContext,
        _args: CartsGetCartArgs,
    ) -> Result<types::CartView, ApiError> {
        Err(ApiError::not_implemented(
            "carts.get_cart is not implemented",
        ))
    }
}

/// Implements ProductsImplementation.
pub struct Products {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl ProductsImplementation for Products {
    async fn list_products(&self, _ctx: RequestContext) -> Result<Vec<types::CartView>, ApiError> {
        Err(ApiError::not_implemented(
            "products.list_products is not implemented",
        ))
    }
}

/// Implements StockImplementation.
pub struct Stock {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl StockImplementation for Stock {
    async fn reserve_stock(&self, _ctx: RequestContext) -> Result<types::CartView, ApiError> {
        Err(ApiError::not_implemented(
            "stock.reserve_stock is not implemented",
        ))
    }
}
