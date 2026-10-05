//! A route's traffic controls and permission check, as the Go router's
//! route middlewares and the TypeScript runtime's `mountOperation` apply
//! them. A request meets them in this order, the cheap refusals first:
//!
//! 1. `@rateLimit`: 429 `too_many_requests` with `Retry-After`.
//! 2. `@bodyLimit`: 413 `payload_too_large`, before the body is read when
//!    `Content-Length` declares it too large, and once it passes the limit
//!    otherwise.
//! 3. The service step (D37), on every route of a server with a
//!    [`ServiceAuthenticator`]: a service credential that does not verify is
//!    401 `service_unauthorized`, an identity that is no caller 403
//!    `service_forbidden`. Then the route's rule: `@requireService` refuses
//!    a request without a caller (401 `service_unauthorized`) or with one
//!    its `from` does not list (403 `service_forbidden`); `@allowService`
//!    admits a listed caller and skips step 4. The caller reaches the
//!    handler as a [`ServiceCaller`] request extension.
//! 4. The permission check of a route that needs an end user (`@auth`,
//!    `@requirePermission`, `@requireOwnership`, an `Authenticated` set):
//!    401 `unauthorized` without a caller, 403 `forbidden` when the
//!    caller's permissions cover none of the route's. The caller reaches
//!    the handler as a [`Principal`] request extension.
//! 5. `@timeout`, around the handler and its extractors: 504
//!    `gateway_timeout`, and the handler's future is dropped.
//!
//! A webhook verifier (`@hmacVerified`) runs before all of them; the
//! generated `webhook_verified` wraps a route [`RouteControls::apply`]
//! returned. Each refusal is the error envelope of [`error_response`].

use crate::{
    error_response, ApiError, Authenticator, Principal, RateLimiter, ServiceAuthenticator,
    ServiceCaller,
};
use axum::body::Body;
use axum::extract::{DefaultBodyLimit, Request};
use axum::middleware::{from_fn, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::MethodRouter;
use http::header::{CONTENT_LENGTH, RETRY_AFTER};
use http::request::Parts;
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
    service: Option<ServiceStep>,
    authorization: Option<Authorization>,
    timeout: Option<Duration>,
}

#[derive(Clone)]
struct Authorization {
    authenticator: Arc<dyn Authenticator>,
    permissions: Arc<[String]>,
}

#[derive(Clone)]
struct ServiceStep {
    authenticator: Arc<dyn ServiceAuthenticator>,
    rule: Option<ServiceRule>,
}

#[derive(Clone)]
struct ServiceRule {
    mode: ServiceMode,
    from: Arc<[String]>,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum ServiceMode {
    Require,
    Allow,
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

    /// `@requireService({ from })`: only a service caller that
    /// `authenticator` verifies, and that serves one of the APIs in `from`
    /// when it lists any. With [`RouteControls::authorize`], the caller must
    /// also forward an end user who meets the route's user clause.
    #[must_use]
    pub fn require_service(
        self,
        authenticator: Arc<dyn ServiceAuthenticator>,
        from: &[&str],
    ) -> Self {
        self.service_rule(authenticator, ServiceMode::Require, from)
    }

    /// `@allowService({ from })`, beside [`RouteControls::authorize`]: a
    /// service caller that `authenticator` verifies and `from` lists (any,
    /// when it is empty) is admitted with no end user, and the permission
    /// check is skipped; any other request goes on to the permission check.
    /// Without `authorize`, the route reads as `require_service`, as the
    /// schema reads `@allowService` without a user clause.
    #[must_use]
    pub fn allow_service(
        self,
        authenticator: Arc<dyn ServiceAuthenticator>,
        from: &[&str],
    ) -> Self {
        self.service_rule(authenticator, ServiceMode::Allow, from)
    }

    /// A route with no service rule, on a server with a service
    /// authenticator: a service credential, when the request carries one,
    /// is verified and its caller put on the request, so the handler can
    /// tell a delegated call from a direct one. A request without one goes
    /// on as before.
    #[must_use]
    pub fn identify_service(mut self, authenticator: Arc<dyn ServiceAuthenticator>) -> Self {
        self.service = Some(ServiceStep {
            authenticator,
            rule: None,
        });
        self
    }

    fn service_rule(
        mut self,
        authenticator: Arc<dyn ServiceAuthenticator>,
        mode: ServiceMode,
        from: &[&str],
    ) -> Self {
        self.service = Some(ServiceStep {
            authenticator,
            rule: Some(ServiceRule {
                mode,
                from: from.iter().map(ToString::to_string).collect(),
            }),
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
        if self.service.is_some() || self.authorization.is_some() {
            let gate = Gate {
                service: self.service,
                authorization: self.authorization,
            };
            route = route.route_layer(from_fn(move |request: Request, next: Next| {
                gate.clone().run(request, next)
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

/// The service step, then the end-user step, as one layer: a listed caller
/// on an `@allowService` route skips the end-user step.
#[derive(Clone)]
struct Gate {
    service: Option<ServiceStep>,
    authorization: Option<Authorization>,
}

impl Gate {
    async fn run(self, request: Request, next: Next) -> Response {
        let (mut parts, body) = request.into_parts();
        let mut admitted = false;
        if let Some(step) = &self.service {
            let caller = match step.authenticator.authenticate(&parts).await {
                Ok(caller) => caller,
                Err(err) => return error_response(err).into_response(),
            };
            if let Some(rule) = &step.rule {
                match rule.admit(caller.as_ref(), self.authorization.is_some()) {
                    Ok(listed) => admitted = listed,
                    Err(err) => return error_response(err).into_response(),
                }
            }
            if let Some(caller) = caller {
                parts.extensions.insert(caller);
            }
        }
        if let (Some(authorization), false) = (&self.authorization, admitted) {
            match authorization.principal(&parts).await {
                Ok(principal) => {
                    parts.extensions.insert(principal);
                }
                Err(err) => return error_response(err).into_response(),
            }
        }
        next.run(Request::from_parts(parts, body)).await
    }
}

impl ServiceRule {
    /// Whether the rule admits the request on the caller alone (`Ok(true)`:
    /// skip the end-user step), sends it on to the end-user step
    /// (`Ok(false)`), or refuses it.
    fn admit(
        &self,
        caller: Option<&ServiceCaller>,
        has_user_clause: bool,
    ) -> Result<bool, ApiError> {
        let listed = caller.is_some_and(|caller| caller.is_listed(&self.from));
        match (self.mode, caller) {
            (ServiceMode::Allow, _) if listed => Ok(true),
            (ServiceMode::Allow, _) if has_user_clause => Ok(false),
            (_, None) => Err(ApiError::service_unauthorized(
                "Service credential required",
            )),
            (_, Some(_)) if !listed => Err(ApiError::service_forbidden("Service not permitted")),
            (_, Some(_)) => Ok(false),
        }
    }
}

impl Authorization {
    /// The end user, holding one of the route's permissions.
    async fn principal(&self, parts: &Parts) -> Result<Principal, ApiError> {
        let principal = self
            .authenticator
            .authenticate(parts)
            .await?
            .ok_or_else(|| ApiError::unauthorized("Authentication required"))?;
        if !self.permissions.is_empty()
            && !self
                .authenticator
                .permits(&principal.permissions, &self.permissions)
        {
            return Err(ApiError::forbidden("Insufficient permissions"));
        }
        Ok(principal)
    }
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

    /// Accepts `Bearer <deployable>:<api>,<api>` as a caller serving those
    /// APIs; refuses `Bearer bad` (401), `Bearer stranger` (403) and
    /// `Bearer down` (503), recording that it ran.
    struct Services {
        log: Log,
    }

    #[async_trait]
    impl ServiceAuthenticator for Services {
        async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
            self.log.lock().unwrap().push("service");
            let Some(value) = request.headers.get("service-authorization") else {
                return Ok(None);
            };
            let token = crate::auth::parse_bearer(value.to_str().unwrap()).unwrap_or_default();
            match token {
                "bad" => Err(ApiError::service_unauthorized("Invalid service credential")),
                "stranger" => Err(ApiError::service_forbidden("Service not permitted")),
                "down" => Err(ApiError::service_unavailable(
                    "Service credential could not be checked",
                )),
                _ => {
                    let (deployable, serves) = token.split_once(':').unwrap_or((token, ""));
                    Ok(Some(ServiceCaller::new(
                        deployable,
                        serves.split(',').filter(|api| !api.is_empty()),
                        format!("sa-{deployable}"),
                    )))
                }
            }
        }
    }

    fn services(log: &Log) -> Arc<dyn ServiceAuthenticator> {
        Arc::new(Services { log: log.clone() })
    }

    /// The handler answers `<caller deployable>/<user subject>`, `-` for
    /// none.
    fn service_app(controls: RouteControls, log: &Log) -> Router {
        let handled = log.clone();
        let route = post(
            move |caller: Option<Extension<ServiceCaller>>,
                  principal: Option<Extension<Principal>>| async move {
                handled.lock().unwrap().push("handler");
                let caller = caller.map_or("-".to_string(), |Extension(c)| c.deployable);
                let user = principal.map_or("-".to_string(), |Extension(p)| p.subject);
                format!("{caller}/{user}")
            },
        );
        Router::new().route("/op", controls.apply(route))
    }

    async fn call(app: &Router, service: Option<&str>, user: Option<&str>) -> (StatusCode, String) {
        let mut request = http::Request::post("/op");
        if let Some(token) = service {
            request = request.header("service-authorization", format!("Bearer {token}"));
        }
        if let Some(token) = user {
            request = request.header("authorization", format!("Bearer {token}"));
        }
        let response = app
            .clone()
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap();
        let status = response.status();
        let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let answer = match serde_json::from_slice::<Value>(&bytes) {
            Ok(body) => code(&body).to_string(),
            Err(_) => String::from_utf8_lossy(&bytes).into_owned(),
        };
        (status, answer)
    }

    fn answer(status: StatusCode, answer: &str) -> (StatusCode, String) {
        (status, answer.to_string())
    }

    #[tokio::test]
    async fn require_service_admits_only_a_listed_caller() {
        let log = Log::default();
        let app = service_app(
            RouteControls::new().require_service(services(&log), &["shop-orders"]),
            &log,
        );
        let (status, _, body) = send(&app, None, "").await;
        assert_eq!(
            (status, code(&body), body["error"]["message"].as_str()),
            (
                StatusCode::UNAUTHORIZED,
                "service_unauthorized",
                Some("Service credential required")
            )
        );
        assert_eq!(
            call(&app, Some("billing:shop-billing"), None).await,
            answer(StatusCode::FORBIDDEN, "service_forbidden")
        );
        assert_eq!(
            call(&app, Some("bad"), None).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );
        assert_eq!(
            call(&app, Some("stranger"), None).await,
            answer(StatusCode::FORBIDDEN, "service_forbidden")
        );
        assert_eq!(
            call(&app, Some("down"), None).await,
            answer(StatusCode::SERVICE_UNAVAILABLE, "service_unavailable")
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), None).await,
            answer(StatusCode::OK, "orders/-")
        );

        // An empty from lists every caller.
        let app = service_app(
            RouteControls::new().require_service(services(&log), &[]),
            &log,
        );
        assert_eq!(
            call(&app, Some("billing"), None).await,
            answer(StatusCode::OK, "billing/-")
        );
    }

    #[tokio::test]
    async fn require_service_with_a_user_clause_needs_both() {
        let log = Log::default();
        let app = service_app(
            RouteControls::new()
                .require_service(services(&log), &["shop-orders"])
                .authorize(tokens(&log), &["orders.create"]),
            &log,
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), None).await,
            answer(StatusCode::UNAUTHORIZED, "unauthorized")
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), Some("ana:orders.read")).await,
            answer(StatusCode::FORBIDDEN, "forbidden")
        );
        assert_eq!(
            call(&app, None, Some("ana:orders.create")).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), Some("ana:orders.create")).await,
            answer(StatusCode::OK, "orders/ana")
        );

        // Both credentials bad: the service refusal answers, and the end
        // user is never authenticated.
        log.lock().unwrap().clear();
        assert_eq!(
            call(&app, Some("bad"), Some("nobody")).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );
        assert_eq!(*log.lock().unwrap(), vec!["service"]);
    }

    #[tokio::test]
    async fn allow_service_admits_a_listed_caller_without_an_end_user() {
        let log = Log::default();
        let app = service_app(
            RouteControls::new()
                .allow_service(services(&log), &["shop-orders"])
                .authorize(tokens(&log), &["stock.write"]),
            &log,
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), None).await,
            answer(StatusCode::OK, "orders/-")
        );
        assert_eq!(
            *log.lock().unwrap(),
            vec!["service", "handler"],
            "a listed caller skips the end-user step"
        );

        // A caller from does not list goes to the end-user step.
        assert_eq!(
            call(&app, Some("billing:shop-billing"), None).await,
            answer(StatusCode::UNAUTHORIZED, "unauthorized")
        );
        assert_eq!(
            call(&app, Some("billing:shop-billing"), Some("ana:stock")).await,
            answer(StatusCode::OK, "billing/ana")
        );
        // So does a request without one.
        assert_eq!(
            call(&app, None, Some("ana:stock.read")).await,
            answer(StatusCode::FORBIDDEN, "forbidden")
        );
        assert_eq!(
            call(&app, None, Some("ana:stock.write")).await,
            answer(StatusCode::OK, "-/ana")
        );
        // A credential that does not verify is refused, not ignored.
        assert_eq!(
            call(&app, Some("bad"), Some("ana:stock.write")).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );
    }

    #[tokio::test]
    async fn allow_service_without_a_user_clause_reads_as_require() {
        let log = Log::default();
        let app = service_app(
            RouteControls::new().allow_service(services(&log), &["shop-orders"]),
            &log,
        );
        assert_eq!(
            call(&app, None, None).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );
        assert_eq!(
            call(&app, Some("billing:shop-billing"), None).await,
            answer(StatusCode::FORBIDDEN, "service_forbidden")
        );
        assert_eq!(
            call(&app, Some("orders:shop-orders"), None).await,
            answer(StatusCode::OK, "orders/-")
        );
    }

    #[tokio::test]
    async fn a_route_without_a_rule_identifies_a_caller_when_there_is_one() {
        let log = Log::default();
        let app = service_app(RouteControls::new().identify_service(services(&log)), &log);
        assert_eq!(call(&app, None, None).await, answer(StatusCode::OK, "-/-"));
        assert_eq!(
            call(&app, Some("billing"), None).await,
            answer(StatusCode::OK, "billing/-")
        );
        assert_eq!(
            call(&app, Some("bad"), None).await,
            answer(StatusCode::UNAUTHORIZED, "service_unauthorized")
        );

        // With a user clause, the end-user step runs as before.
        let app = service_app(
            RouteControls::new()
                .identify_service(services(&log))
                .authorize(tokens(&log), &[]),
            &log,
        );
        assert_eq!(
            call(&app, Some("billing"), None).await,
            answer(StatusCode::UNAUTHORIZED, "unauthorized")
        );
        assert_eq!(
            call(&app, Some("billing"), Some("ana")).await,
            answer(StatusCode::OK, "billing/ana")
        );
    }

    // The service step runs after the rate limit and the body limit, and
    // before the end-user step and the timeout.
    #[tokio::test]
    async fn the_service_step_runs_between_the_body_limit_and_the_end_user_step() {
        let log = Log::default();
        let controls = RouteControls::new()
            .rate_limit(1)
            .body_limit_bytes(4)
            .require_service(services(&log), &[])
            .authorize(tokens(&log), &[])
            .timeout_seconds(5);
        let app = app(controls, &log);
        assert_eq!(
            send(&app, None, "too long").await.0,
            StatusCode::PAYLOAD_TOO_LARGE
        );
        assert_eq!(send(&app, None, "").await.0, StatusCode::TOO_MANY_REQUESTS);
        assert!(log.lock().unwrap().is_empty(), "{:?}", log.lock().unwrap());

        let log = Log::default();
        let app = service_app(
            RouteControls::new()
                .body_limit_bytes(4)
                .require_service(services(&log), &[])
                .authorize(tokens(&log), &[])
                .timeout_seconds(5),
            &log,
        );
        assert_eq!(
            call(&app, Some("orders"), Some("ana")).await,
            answer(StatusCode::OK, "orders/ana")
        );
        assert_eq!(
            *log.lock().unwrap(),
            vec!["service", "authenticate", "handler"]
        );
    }
}
