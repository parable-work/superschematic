//! The implementation of the deps-rust-pricing API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.
//!
//! The authenticator establishes no caller, so each route that needs one
//! answers 401 until it does.
//!
//! The server does not start until each @hmacVerified provider has a
//! verifier in `webhook_verifiers`.

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_deps_rust_pricing_api::types;
use schemas_deps_rust_pricing_api::{Deps, Handlers, QuotesGetQuoteArgs, QuotesImplementation};
use superschematic_http_runtime::{ApiError, Authenticator, Principal, RequestContext};

/// Builds the implementation of deps-rust-pricing from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        quotes: Arc::new(Quotes {
            deps: Arc::clone(&deps),
        }),
        authenticator: Arc::new(NoCaller),
        // The verifier of each @hmacVerified provider, which checks the
        // signature of its requests: github.
        webhook_verifiers: HashMap::new(),
    })
}

/// Implements QuotesImplementation.
pub struct Quotes {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl QuotesImplementation for Quotes {
    async fn price_feed(&self, _ctx: RequestContext) -> Result<bool, ApiError> {
        Err(ApiError::not_implemented(
            "quotes.price_feed is not implemented",
        ))
    }

    async fn get_quote(
        &self,
        _ctx: RequestContext,
        _args: QuotesGetQuoteArgs,
    ) -> Result<types::Quote, ApiError> {
        Err(ApiError::not_implemented(
            "quotes.get_quote is not implemented",
        ))
    }
}

/// Establishes the end user on the routes that need one. The scaffold's
/// establishes none, so each such route answers 401: verify the request's
/// credential (`runtime::bearer_token(&request.headers)`, or a header) with
/// your identity provider and return its `Principal`.
pub struct NoCaller;

#[async_trait]
impl Authenticator for NoCaller {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}
