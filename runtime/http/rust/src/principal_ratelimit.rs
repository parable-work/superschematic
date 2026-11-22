//! A `@rateLimit({ key: 'principal' })` route's limit (D131): at most a
//! number of requests per minute for each principal the route's permission
//! check admitted, counted in a store every replica shares. It runs after
//! the permission check, so the principal is the caller's. A request with no
//! principal is refused 400 rather than counted under an empty key, and a
//! store failure refuses the request 503: the limit exists to cap spend, so
//! it fails closed. Past the limit the route answers 429 with `Retry-After`
//! in whole seconds, at least 1.

use crate::{error_response, ApiError, Principal};
use async_trait::async_trait;
use axum::extract::Request;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use http::header::RETRY_AFTER;
use http::StatusCode;
use std::sync::Arc;
use std::time::Duration;

/// A [`PrincipalRateLimitStore`]'s answer for one request.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RateLimitDecision {
    /// Whether the request fits in the current window.
    pub allowed: bool,
    /// How long until the window that refused the request ends; meaningful
    /// only when `allowed` is false.
    pub retry_after: Duration,
}

/// Holds the counters behind the routes whose `@rateLimit` counts per
/// principal. The service supplies one, so the counter lives wherever it
/// keeps shared state. `take` counts the request and decides in one atomic
/// step, so replicas sharing the store share one count.
#[async_trait]
pub trait PrincipalRateLimitStore: Send + Sync {
    /// Counts one request for `bucket` and `principal` in the window that
    /// contains now, and reports whether that count is within `limit`. An
    /// error is a store failure, which the route answers 503.
    async fn take(
        &self,
        bucket: &str,
        principal: &str,
        limit: u32,
        window: Duration,
    ) -> Result<RateLimitDecision, Box<dyn std::error::Error + Send + Sync>>;
}

/// One route's principal limit: its store, its bucket (the route's method
/// and path) and its requests per minute.
#[derive(Clone)]
pub(crate) struct PrincipalLimit {
    pub(crate) store: Arc<dyn PrincipalRateLimitStore>,
    pub(crate) bucket: Arc<str>,
    pub(crate) requests_per_minute: u32,
}

impl PrincipalLimit {
    pub(crate) async fn run(self, request: Request, next: Next) -> Response {
        let principal = request
            .extensions()
            .get::<Principal>()
            .map(|principal| principal.subject.clone())
            .filter(|subject| !subject.is_empty());
        let Some(principal) = principal else {
            return error_response(ApiError::new(
                StatusCode::BAD_REQUEST,
                "rate_limit_principal_missing",
                "This operation is limited per principal and the request names none",
            ))
            .into_response();
        };
        let window = Duration::from_secs(60);
        match self
            .store
            .take(&self.bucket, &principal, self.requests_per_minute, window)
            .await
        {
            Err(err) => {
                tracing::error!(bucket = %self.bucket, error = %err, "principal rate limit store failed");
                error_response(ApiError::new(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "rate_limit_unavailable",
                    "Rate limit check is unavailable; try again shortly",
                ))
                .into_response()
            }
            Ok(decision) if !decision.allowed => {
                let mut response = error_response(ApiError::too_many_requests(format!(
                    "This principal exceeded {} requests per minute",
                    self.requests_per_minute
                )))
                .into_response();
                response.headers_mut().insert(
                    RETRY_AFTER,
                    retry_after_seconds(decision.retry_after).into(),
                );
                response
            }
            Ok(_) => next.run(request).await,
        }
    }
}

/// Whole seconds, rounded up, and never 0, which a client would read as
/// "retry now".
fn retry_after_seconds(retry_after: Duration) -> u64 {
    let seconds = retry_after.as_secs() + u64::from(retry_after.subsec_nanos() > 0);
    seconds.max(1)
}
