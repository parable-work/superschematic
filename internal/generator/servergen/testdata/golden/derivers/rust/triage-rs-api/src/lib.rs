//! The implementation of the triage-rs-api API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.
//!
//! Each deriver fails every field until you implement it.

use std::sync::Arc;

use async_trait::async_trait;
use schemas_triage_rs_api_api::types;
use schemas_triage_rs_api_api::{
    database_types, BoxError, Deps, DerivedAnswer, DerivedItem, Derivers, FillAllDeriver, Handlers,
    TicketCategoryInputs, TicketSummaryInputs, TriageImplementation, TriageTicketsDeriver,
};
use superschematic_http_runtime::{ApiError, RequestContext};

/// Builds the implementation of triage-rs-api from its dependencies: the
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

/// Builds the derivers of triage-rs-api from its dependencies. A deriver's
/// entrypoint calls the method of each stale derived field, which asks a
/// model; a value settles the field, and a failure or an error counts an
/// attempt.
pub async fn create_derivers(deps: Deps) -> Result<Derivers, BoxError> {
    let deps = Arc::new(deps);
    Ok(Derivers {
        fill_all: Arc::new(TriageRsApiFillAll {
            deps: Arc::clone(&deps),
        }),
        triage_tickets: Arc::new(TriageRsApiTriageTickets {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements FillAllDeriver.
pub struct TriageRsApiFillAll {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl FillAllDeriver for TriageRsApiFillAll {
    /// Derives Ticket.category.
    async fn ticket_category(
        &self,
        _item: DerivedItem<TicketCategoryInputs>,
    ) -> Result<DerivedAnswer<database_types::Category>, BoxError> {
        Err("deriver FillAll: Ticket.category is not implemented".into())
    }

    /// Derives Ticket.summary.
    async fn ticket_summary(
        &self,
        _item: DerivedItem<TicketSummaryInputs>,
    ) -> Result<DerivedAnswer<String>, BoxError> {
        Err("deriver FillAll: Ticket.summary is not implemented".into())
    }
}

/// Implements TriageTicketsDeriver.
pub struct TriageRsApiTriageTickets {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl TriageTicketsDeriver for TriageRsApiTriageTickets {
    /// Derives Ticket.category.
    async fn ticket_category(
        &self,
        _item: DerivedItem<TicketCategoryInputs>,
    ) -> Result<DerivedAnswer<database_types::Category>, BoxError> {
        Err("deriver TriageTickets: Ticket.category is not implemented".into())
    }

    /// Derives Ticket.summary.
    async fn ticket_summary(
        &self,
        _item: DerivedItem<TicketSummaryInputs>,
    ) -> Result<DerivedAnswer<String>, BoxError> {
        Err("deriver TriageTickets: Ticket.summary is not implemented".into())
    }
}
