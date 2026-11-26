//! The implementation of the derived-rs-api API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.
//!
//! Each deriver fails every field until you implement it.

use std::sync::Arc;

use async_trait::async_trait;
use schemas_derived_rs_api_api::types;
use schemas_derived_rs_api_api::{
    database_types, ArticleSummaryInputs, BoxError, Deps, DerivedAnswer, DerivedItem, Derivers,
    FillAllDeriver, Handlers, TicketCategoryInputs, TicketPriorityInputs, TriageImplementation,
    TriageTicketsDeriver,
};
use superschematic_http_runtime::{ApiError, RequestContext};

/// Builds the implementation of derived-rs-api from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        triage: Arc::new(Triage {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements TriageImplementation.
pub struct Triage {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl TriageImplementation for Triage {
    async fn summary(&self, _ctx: RequestContext) -> Result<types::Summary, ApiError> {
        Err(ApiError::not_implemented(
            "triage.summary is not implemented",
        ))
    }
}

/// Builds the derivers of derived-rs-api from its dependencies. A deriver's
/// entrypoint calls the method of each stale derived field, which asks a
/// model; a value settles the field, and a failure or an error counts an
/// attempt.
pub async fn create_derivers(deps: Deps) -> Result<Derivers, BoxError> {
    let deps = Arc::new(deps);
    Ok(Derivers {
        fill_all: Arc::new(DerivedRsApiFillAll {
            deps: Arc::clone(&deps),
        }),
        triage_tickets: Arc::new(DerivedRsApiTriageTickets {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements FillAllDeriver.
pub struct DerivedRsApiFillAll {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl FillAllDeriver for DerivedRsApiFillAll {
    /// Derives Article.summary.
    async fn article_summary(
        &self,
        _item: DerivedItem<ArticleSummaryInputs>,
    ) -> Result<DerivedAnswer<String>, BoxError> {
        Err("deriver FillAll: Article.summary is not implemented".into())
    }

    /// Derives Ticket.category.
    async fn ticket_category(
        &self,
        _item: DerivedItem<TicketCategoryInputs>,
    ) -> Result<DerivedAnswer<database_types::Category>, BoxError> {
        Err("deriver FillAll: Ticket.category is not implemented".into())
    }

    /// Derives Ticket.priority.
    async fn ticket_priority(
        &self,
        _item: DerivedItem<TicketPriorityInputs>,
    ) -> Result<DerivedAnswer<String>, BoxError> {
        Err("deriver FillAll: Ticket.priority is not implemented".into())
    }
}

/// Implements TriageTicketsDeriver.
pub struct DerivedRsApiTriageTickets {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl TriageTicketsDeriver for DerivedRsApiTriageTickets {
    /// Derives Ticket.category.
    async fn ticket_category(
        &self,
        _item: DerivedItem<TicketCategoryInputs>,
    ) -> Result<DerivedAnswer<database_types::Category>, BoxError> {
        Err("deriver TriageTickets: Ticket.category is not implemented".into())
    }

    /// Derives Ticket.priority.
    async fn ticket_priority(
        &self,
        _item: DerivedItem<TicketPriorityInputs>,
    ) -> Result<DerivedAnswer<String>, BoxError> {
        Err("deriver TriageTickets: Ticket.priority is not implemented".into())
    }
}
