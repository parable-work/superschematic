//! A route's traffic controls and permission check, as the Go router's
//! route middlewares and the TypeScript runtime's `mountOperation` apply
//! them. A request meets them in this order, the cheap refusals first:
//!
//! 1. `@rateLimit`: 429 `too_many_requests` with `Retry-After`.
//! 2. `@bodyLimit`: 413 `payload_too_large`, before the body is read when
//!    `Content-Length` declares it too large, and once it passes the limit
//!    otherwise.
//! 3. The permission check of a route that needs a caller (`@auth`,
//!    `@requirePermission`, `@requireOwnership`, an `Authenticated` set):
//!    401 `unauthorized` without a caller, 403 `forbidden` when the
//!    caller's permissions cover none of the route's. The caller reaches
//!    the handler as a [`Principal`] request extension.
//! 4. `@timeout`, around the handler and its extractors: 504
//!    `gateway_timeout`, and the handler's future is dropped.
//!
//! A webhook verifier (`@hmacVerified`) runs before all of them; the
//! generated `webhook_verified` wraps a route [`RouteControls::apply`]
//! returned. Each refusal is the error envelope of [`error_response`].

use crate::{error_response, ApiError, Authenticator, Principal, RateLimiter};
use axum::body::Body;
use axum::extract::{DefaultBodyLimit, Request};
use axum::middleware::{from_fn, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::MethodRouter;
use http::header::{CONTENT_LENGTH, RETRY_AFTER};
use std::sync::Arc;
use std::time::Duration;

const MEBIBYTE: usize = 1024 * 1024;

/// The controls of one route, applied with [`RouteControls::apply`]. The
/// generated `build_router` builds one for each route that declares any; a
/// service adds the controls of a `@manualRouteRegistration` route it
/// mounts itself the same way, as `build_router`'s doc lists them.
#[derive(Clone, Default)]
pub struct RouteControls {
    rate_limit: Option<Arc<RateLimiter>>,
    body_limit: Option<usize>,
    authorization: Option<Authorization>,
    timeout: Option<Duration>,
}

#[derive(Clone)]
struct Authorization {
    authenticator: Arc<dyn Authenticator>,
    permissions: Arc<[String]>,
}

impl RouteControls {
    pub fn new() -> Self {
        Self::default()
    }

    /// `@rateLimit({ requestsPerMinute })`: this route's own limiter.
    #[must_use]
    pub fn rate_limit(mut self, requests_per_minute: u32) -> Self {
        self.rate_limit = Some(Arc::new(RateLimiter::per_minute(requests_per_minute)));
        self
    }

    /// `@bodyLimit({ megabytes })`, in mebibytes as the Go and TypeScript
    /// servers count them. It replaces axum's default 2 MB limit on the
    /// route's body extractor.
    #[must_use]
    pub fn body_limit_megabytes(self, megabytes: usize) -> Self {
        self.body_limit_bytes(megabytes.saturating_mul(MEBIBYTE))
    }

    #[must_use]
    pub fn body_limit_bytes(mut self, bytes: usize) -> Self {
        self.body_limit = Some(bytes);
        self
    }

    /// The route needs a caller that `authenticator` establishes, holding
    /// one of `permissions` when there are any.
    #[must_use]
    pub fn authorize(
        mut self,
        authenticator: Arc<dyn Authenticator>,
        permissions: &[&str],
    ) -> Self {
        self.authorization = Some(Authorization {
            authenticator,
            permissions: permissions.iter().map(ToString::to_string).collect(),
        });
        self
    }

    /// `@timeout({ seconds })`.
    #[must_use]
    pub fn timeout_seconds(mut self, seconds: u64) -> Self {
        self.timeout = Some(Duration::from_secs(seconds));
        self
    }

    /// Adds the controls to `route` with `route_layer`, so they run only
    /// for a request the route matches, before its handler's extractors.
    pub fn apply<S>(self, route: MethodRouter<S>) -> MethodRouter<S>
    where
        S: Clone + Send + Sync + 'static,
    {
        // route_layer runs the layer added last first, so the controls are
        // added innermost first.
        let mut route = route;
        if let Some(timeout) = self.timeout {
            route = route.route_layer(from_fn(move |request: Request, next: Next| {
                time_out(timeout, request, next)
            }));
        }
        if let Some(authorization) = self.authorization {
            route = route.route_layer(from_fn(move |request: Request, next: Next| {
                authorize(authorization.clone(), request, next)
            }));
        }
        if let Some(limit) = self.body_limit {
            route = route
                .route_layer(DefaultBodyLimit::max(limit))
                .route_layer(from_fn(move |request: Request, next: Next| {
                    limit_body(limit, request, next)
                }));
        }
        if let Some(limiter) = self.rate_limit {
            route = route.route_layer(from_fn(move |request: Request, next: Next| {
                admit(Arc::clone(&limiter), request, next)
            }));
        }
        route
    }
}

async fn admit(limiter: Arc<RateLimiter>, request: Request, next: Next) -> Response {
    match limiter.take(&crate::client_key(request.extensions())) {
        Ok(()) => next.run(request).await,
        Err(retry_after) => {
            let mut response =
                error_response(ApiError::too_many_requests("Too Many Requests")).into_response();
            response
                .headers_mut()
                .insert(RETRY_AFTER, retry_after.into());
            response
        }
    }
}

async fn limit_body(limit: usize, request: Request, next: Next) -> Response {
    let declared = request
        .headers()
        .get(CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<u64>().ok());
    if declared.is_some_and(|length| length > limit as u64) {
        return too_large();
    }
    let (parts, body) = request.into_parts();
    match axum::body::to_bytes(body, limit).await {
        Ok(bytes) => {
            next.run(Request::from_parts(parts, Body::from(bytes)))
                .await
        }
        Err(err) if is_length_limit(&err) => too_large(),
        Err(_) => error_response(ApiError::bad_request("The request body could not be read"))
            .into_response(),
    }
}

fn is_length_limit(err: &axum::Error) -> bool {
    let mut source: Option<&(dyn std::error::Error + 'static)> = Some(err);
    while let Some(current) = source {
        if current.is::<http_body_util::LengthLimitError>() {
            return true;
        }
        source = current.source();
    }
    false
}

fn too_large() -> Response {
    error_response(ApiError::payload_too_large(
        "Request body exceeds the accepted size",
    ))
    .into_response()
}

async fn authorize(authorization: Authorization, request: Request, next: Next) -> Response {
    let Authorization {
        authenticator,
        permissions,
    } = authorization;
    let (mut parts, body) = request.into_parts();
    let principal: Principal = match authenticator.authenticate(&parts).await {
        Ok(Some(principal)) => principal,
        Ok(None) => {
            return error_response(ApiError::unauthorized("Authentication required"))
                .into_response()
        }
        Err(err) => return error_response(err).into_response(),
    };
    if !permissions.is_empty() && !authenticator.permits(&principal.permissions, &permissions) {
        return error_response(ApiError::forbidden("Insufficient permissions")).into_response();
    }
    parts.extensions.insert(principal);
    next.run(Request::from_parts(parts, body)).await
}

async fn time_out(timeout: Duration, request: Request, next: Next) -> Response {
    match tokio::time::timeout(timeout, next.run(request)).await {
        Ok(response) => response,
        Err(_) => error_response(ApiError::gateway_timeout("Gateway Timeout")).into_response(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use axum::extract::Extension;
    use axum::routing::post;
    use axum::Router;
    use http::request::Parts;
    use http::StatusCode;
    use serde_json::Value;
    use std::sync::Mutex;
    use tower::ServiceExt;

    type Log = Arc<Mutex<Vec<&'static str>>>;

    /// Accepts `Bearer <subject>:<permission>,<permission>`, and refuses
    /// `Bearer down` with a 503, recording that it ran.
    struct Tokens {
        log: Log,
    }

    #[async_trait]
    impl Authenticator for Tokens {
        async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
            self.log.lock().unwrap().push("authenticate");
            let Some(token) = crate::bearer_token(&request.headers) else {
                return Ok(None);
            };
            if token == "down" {
                return Err(ApiError::new(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "service_unavailable",
                    "The identity service is down",
                ));
            }
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Ok(Some(Principal::new(
                subject,
                permissions.split(',').filter(|p| !p.is_empty()),
            )))
        }
    }

    fn app(controls: RouteControls, log: &Log) -> Router {
        let handled = log.clone();
        let route = post(
            move |principal: Option<Extension<Principal>>, body: String| async move {
                handled.lock().unwrap().push("handler");
                if body == "slow" {
                    tokio::time::sleep(Duration::from_secs(5)).await;
                }
                principal.map(|Extension(p)| p.subject).unwrap_or_default()
            },
        );
        Router::new().route("/op", controls.apply(route))
    }

    fn tokens(log: &Log) -> Arc<dyn Authenticator> {
        Arc::new(Tokens { log: log.clone() })
    }

    async fn send(
        app: &Router,
        token: Option<&str>,
        body: &str,
    ) -> (StatusCode, Option<String>, Value) {
        let mut request = http::Request::post("/op");
        if let Some(token) = token {
            request = request.header("authorization", format!("Bearer {token}"));
        }
        let response = app
            .clone()
            .oneshot(request.body(Body::from(body.to_string())).unwrap())
            .await
            .unwrap();
        let status = response.status();
        let retry_after = response
            .headers()
            .get(RETRY_AFTER)
            .map(|v| v.to_str().unwrap().to_string());
        let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let body = serde_json::from_slice(&bytes)
            .unwrap_or_else(|_| Value::String(String::from_utf8_lossy(&bytes).into_owned()));
        (status, retry_after, body)
    }

    fn code(body: &Value) -> &str {
        body["error"]["code"].as_str().unwrap_or_default()
    }

    #[tokio::test]
    async fn the_caller_needs_a_principal_and_one_of_the_permissions() {
        let log = Log::default();
        let app = app(
            RouteControls::new().authorize(tokens(&log), &["orders.read", "orders.audit"]),
            &log,
        );

        let (status, _, body) = send(&app, None, "").await;
        assert_eq!(
            (status, code(&body)),
            (StatusCode::UNAUTHORIZED, "unauthorized")
        );
        assert_eq!(body["error"]["message"], "Authentication required");

        let (status, _, body) = send(&app, Some("ana:orders.write"), "").await;
        assert_eq!((status, code(&body)), (StatusCode::FORBIDDEN, "forbidden"));
        assert_eq!(body["error"]["message"], "Insufficient permissions");

        let (status, _, body) = send(&app, Some("down"), "").await;
        assert_eq!(
            (status, code(&body)),
            (StatusCode::SERVICE_UNAVAILABLE, "service_unavailable")
        );

        let (status, _, body) = send(&app, Some("ana:orders"), "").await;
        assert_eq!(
            (status, body),
            (StatusCode::OK, Value::String("ana".to_string()))
        );
        assert_eq!(
            log.lock()
                .unwrap()
                .iter()
                .filter(|e| **e == "handler")
                .count(),
            1
        );
    }

    #[tokio::test]
    async fn a_route_without_permissions_needs_only_a_caller() {
        let log = Log::default();
        let app = app(RouteControls::new().authorize(tokens(&log), &[]), &log);
        assert_eq!(send(&app, None, "").await.0, StatusCode::UNAUTHORIZED);
        let (status, _, body) = send(&app, Some("ben"), "").await;
        assert_eq!(
            (status, body),
            (StatusCode::OK, Value::String("ben".to_string()))
        );
    }

    #[tokio::test]
    async fn past_the_rate_limit_the_route_answers_429_with_retry_after() {
        let log = Log::default();
        let app = app(RouteControls::new().rate_limit(2), &log);
        assert_eq!(send(&app, None, "").await.0, StatusCode::OK);
        assert_eq!(send(&app, None, "").await.0, StatusCode::OK);
        let (status, retry_after, body) = send(&app, None, "").await;
        assert_eq!(
            (status, code(&body)),
            (StatusCode::TOO_MANY_REQUESTS, "too_many_requests")
        );
        assert_eq!(retry_after.as_deref(), Some("30"));
        assert_eq!(log.lock().unwrap().len(), 2);
    }

    #[tokio::test]
    async fn a_body_over_the_limit_answers_413_declared_or_read() {
        let log = Log::default();
        let app = app(RouteControls::new().body_limit_bytes(8), &log);
        // send sets no Content-Length: the body is cut once it passes the limit.
        assert_eq!(send(&app, None, "12345678").await.0, StatusCode::OK);
        let (status, _, body) = send(&app, None, "123456789").await;
        assert_eq!(
            (status, code(&body)),
            (StatusCode::PAYLOAD_TOO_LARGE, "payload_too_large")
        );

        // A declared length over the limit is refused before the body is read.
        let declared = http::Request::post("/op")
            .header(CONTENT_LENGTH, "1000")
            .body(Body::from("1"))
            .unwrap();
        let response = app.clone().oneshot(declared).await.unwrap();
        assert_eq!(response.status(), StatusCode::PAYLOAD_TOO_LARGE);
        assert_eq!(log.lock().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn a_body_limit_over_axums_default_lifts_it() {
        let log = Log::default();
        let app = app(RouteControls::new().body_limit_megabytes(3), &log);
        let body = "x".repeat(2 * MEBIBYTE + 1);
        assert_eq!(send(&app, None, &body).await.0, StatusCode::OK);
        let body = "x".repeat(3 * MEBIBYTE + 1);
        assert_eq!(
            send(&app, None, &body).await.0,
            StatusCode::PAYLOAD_TOO_LARGE
        );
    }

    #[tokio::test(start_paused = true)]
    async fn a_handler_past_the_timeout_answers_504() {
        let log = Log::default();
        let app = app(RouteControls::new().timeout_seconds(1), &log);
        assert_eq!(send(&app, None, "fast").await.0, StatusCode::OK);
        let (status, _, body) = send(&app, None, "slow").await;
        assert_eq!(
            (status, code(&body)),
            (StatusCode::GATEWAY_TIMEOUT, "gateway_timeout")
        );
    }

    // The cheap refusals come first: the rate limit before the body limit
    // and the permission check, the body limit before the permission check,
    // so neither costs an authentication.
    #[tokio::test]
    async fn the_rate_limit_and_the_body_limit_run_before_the_permission_check() {
        let log = Log::default();
        let controls = RouteControls::new()
            .rate_limit(1)
            .body_limit_bytes(4)
            .authorize(tokens(&log), &["orders.read"])
            .timeout_seconds(5);
        let app = app(controls, &log);

        let (status, _, _) = send(&app, Some("ana:orders.read"), "too long").await;
        assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
        let (status, _, _) = send(&app, None, "").await;
        assert_eq!(status, StatusCode::TOO_MANY_REQUESTS);
        assert!(log.lock().unwrap().is_empty(), "{:?}", log.lock().unwrap());

        let log = Log::default();
        let app = self::app(
            RouteControls::new()
                .rate_limit(1)
                .body_limit_bytes(4)
                .authorize(tokens(&log), &["orders.read"]),
            &log,
        );
        assert_eq!(send(&app, None, "").await.0, StatusCode::UNAUTHORIZED);
        assert_eq!(*log.lock().unwrap(), vec!["authenticate"]);
    }
}
