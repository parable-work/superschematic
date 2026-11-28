//! The implementation of the fixture-wired-api API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.
//!
//! The authenticator establishes no caller, so each route that needs one
//! answers 401 until it does.
//!
//! Each event handler fails every event until you implement it.

use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_wired_api_api::behaviors::EventRun;
use schemas_fixture_wired_api_api::types;
use schemas_fixture_wired_api_api::{
    BoxError, Deps, Handlers, Wire, WiringDeps, WorkImplementation, WorkReviewArgs, Workers,
};
use superschematic_http_runtime::{ApiError, Authenticator, Principal, RequestContext};

/// Builds the implementation of fixture-wired-api from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        work: Arc::new(Work {
            deps: Arc::clone(&deps),
        }),
        authenticator: Arc::new(NoCaller),
    })
}

/// Implements WorkImplementation.
pub struct Work {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl WorkImplementation for Work {
    /// Seeds the rows of a test: an approver, its roles and a task.
    async fn seed(&self, _ctx: RequestContext) -> Result<types::Seeded, ApiError> {
        Err(ApiError::not_implemented("work.seed is not implemented"))
    }

    /// Whether the latest run is done: the MCP tool work_status.
    async fn status(&self, _ctx: RequestContext) -> Result<types::Done, ApiError> {
        Err(ApiError::not_implemented("work.status is not implemented"))
    }

    /// Asks the reviewer agent to review a task, a run request.
    async fn review(
        &self,
        _ctx: RequestContext,
        _args: WorkReviewArgs,
    ) -> Result<types::Done, ApiError> {
        Err(ApiError::not_implemented("work.review is not implemented"))
    }

    /// Raises a budget warning, which the notifier routes to the approvers.
    async fn warn(&self, _ctx: RequestContext) -> Result<types::Done, ApiError> {
        Err(ApiError::not_implemented("work.warn is not implemented"))
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

/// Builds the workers of fixture-wired-api from its dependencies. A worker's
/// entrypoint calls one method per message it claims; the message is done
/// when the method returns `Ok`, and fails, to be retried, when it returns
/// an error. A message may come again, so a method is idempotent.
pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(FixtureWiredApiWorkers {
        deps: Arc::new(deps),
    }))
}

/// Implements Workers.
pub struct FixtureWiredApiWorkers {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl Workers for FixtureWiredApiWorkers {
    /// Handles an event run of Task's Schedules, which the behavior worker
    /// TaskBehaviors claims from TaskEvent (D104).
    async fn task_behaviors_schedules_run(
        &self,
        _event: types::TaskSchedulesRunEvent,
        _run: &EventRun<'_>,
    ) -> Result<(), BoxError> {
        Err("event handler TaskBehaviorsSchedulesRun is not implemented".into())
    }
}

/// Builds the Wire that shapes the runtime parts of fixture-agent-runs-db the server
/// builds (#620, #622, #617, #610): the notifier's reply actions, the agent
/// dispatcher's endpoint and tools. Each method of Wire is handed a part as the
/// server built it, and its default keeps it so.
pub async fn create_wiring(_deps: &WiringDeps) -> Result<Arc<dyn Wire>, BoxError> {
    Ok(Arc::new(FixtureWiredApiWiring))
}

/// Implements Wire, each part as the server builds it.
pub struct FixtureWiredApiWiring;

impl Wire for FixtureWiredApiWiring {}
