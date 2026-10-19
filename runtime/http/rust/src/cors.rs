//! The cross-origin requests a static site's browser makes to the APIs it
//! calls (D55, section 8.10 of `docs/stack-model.md`), the twin of the Go
//! runtime's `cors` package and the TypeScript runtime's `corsHandler`.
//! Each API a site calls has a CORS field in a stack, the origins of the
//! sites that call it, which [`crate::stackconfig::load_cors`] reads; its
//! server answers CORS for those origins, with credentials, the
//! `Authorization` header and the methods the API's operations use.
//!
//! It answers no other origin itself: a preflight from one reaches the
//! API's router, whose identity runtime (`identity::CorsLayer`, with the
//! `identity` feature) answers its config's trusted origins (D55,
//! amended). So an API that a site calls and that serves the user model
//! answers both the site and the origins its identity config trusts.
//!
//! [`Policy::decide`] is the decision, which the vectors in
//! `runtime/http/testdata/cors_parity.json` hold to Go's and TypeScript's.
//! [`SiteCorsLayer`] applies it around a server's router, for each API the
//! server serves ([`Api`]), the API a request is by the operations whose
//! routes take it:
//!
//! ```ignore
//! let shop = stackconfig::load_cors("SHOP_API_CORS")?;
//! let router = router.layer(cors::SiteCorsLayer::new(vec![cors::Api::new(
//!     &shop,
//!     shop_api::operations::ALL,
//! )]));
//! ```

use std::collections::BTreeMap;
use std::convert::Infallible;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};

use axum::extract::Request;
use axum::response::{IntoResponse, Response};
use http::header::{ACCESS_CONTROL_REQUEST_METHOD, ORIGIN};
use http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};

use crate::stackconfig;
use crate::OperationInfo;

/// The request headers a site's browser may send an API: what the generated
/// SDKs send, the end user's token in `Authorization` among them. A service
/// credential never travels from a browser, so `Service-Authorization` is
/// not one.
pub const ALLOWED_HEADERS: [&str; 3] = ["Accept", "Authorization", "Content-Type"];

/// How long, in seconds, a browser may keep a preflight's answer.
pub const MAX_AGE: u32 = 600;

/// The `Vary` of an answer to a request with an `Origin`.
pub const VARY: &str = "Origin";

/// The `Vary` of an answer to a preflight.
pub const PREFLIGHT_VARY: &str =
    "Origin, Access-Control-Request-Method, Access-Control-Request-Headers";

/// One API's CORS: the origins of the sites that call it, as its CORS field
/// lists them, and the methods its operations use.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Policy {
    /// The origins the API answers, `<scheme>://<host>[:<port>]` each,
    /// compared exactly. None answers no CORS at all.
    pub origins: Vec<String>,
    /// The HTTP methods of the API's operations, in upper case, which a
    /// preflight's answer allows.
    pub methods: Vec<String>,
}

/// What a request's CORS check decides.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Decision {
    /// An `OPTIONS` request with an `Origin` and an
    /// `Access-Control-Request-Method`.
    pub preflight: bool,
    /// A preflight from an origin the policy lists, which the server
    /// answers itself with 204 and the headers alone. Any other request
    /// reaches the API with the headers, a preflight from an origin the
    /// policy does not list included, so a CORS check of the API's own,
    /// the identity runtime's for its trusted origins, answers it.
    pub answer: bool,
    /// The headers the answer carries, by canonical name; none for a
    /// request without an `Origin`.
    pub headers: BTreeMap<&'static str, String>,
}

/// A request header's first value, empty when it is absent.
fn first<'a>(headers: &'a HeaderMap, name: &HeaderName) -> &'a [u8] {
    headers.get(name).map_or(&[], HeaderValue::as_bytes)
}

/// Whether a request with `method` and `headers` is a CORS preflight:
/// `OPTIONS` with an `Origin` and an `Access-Control-Request-Method`.
pub fn is_preflight(method: &Method, headers: &HeaderMap) -> bool {
    method == Method::OPTIONS
        && !first(headers, &ORIGIN).is_empty()
        && !first(headers, &ACCESS_CONTROL_REQUEST_METHOD).is_empty()
}

impl Policy {
    /// The policy of an API whose CORS field is `cors` and whose operations
    /// are `operations`: the field's origins and [`methods`].
    pub fn new(cors: &stackconfig::Cors, operations: &[OperationInfo]) -> Self {
        Policy {
            origins: cors.origins.clone(),
            methods: methods(operations),
        }
    }

    /// Decides a request's CORS, as Go's `Policy.Decide` does. A request
    /// with no `Origin` gets no header. One with an `Origin` the policy
    /// lists gets `Access-Control-Allow-Origin` with that origin and
    /// `Access-Control-Allow-Credentials`; its preflight is the server's to
    /// answer, and gets the methods, the headers and the max age too. One
    /// with an `Origin` the policy does not list gets `Vary` alone and is
    /// not the server's to answer, a preflight included: the API answers
    /// it, and unless a CORS check of its own allows the origin, the
    /// browser refuses the answer.
    pub fn decide(&self, method: &Method, headers: &HeaderMap) -> Decision {
        let preflight = is_preflight(method, headers);
        let mut decision = Decision {
            preflight,
            ..Decision::default()
        };
        let origin = first(headers, &ORIGIN);
        if origin.is_empty() {
            return decision;
        }
        let vary = if preflight { PREFLIGHT_VARY } else { VARY };
        decision.headers.insert("Vary", vary.to_string());
        let Some(origin) = self.origins.iter().find(|o| o.as_bytes() == origin) else {
            return decision;
        };
        let headers = &mut decision.headers;
        headers.insert("Access-Control-Allow-Origin", origin.clone());
        headers.insert("Access-Control-Allow-Credentials", "true".to_string());
        if preflight {
            decision.answer = true;
            headers.insert("Access-Control-Allow-Methods", self.methods.join(", "));
            headers.insert("Access-Control-Allow-Headers", ALLOWED_HEADERS.join(", "));
            headers.insert("Access-Control-Max-Age", MAX_AGE.to_string());
        }
        decision
    }
}

/// The methods of `operations`, upper case, each once, in order: what an
/// API's [`Policy`] allows, as TypeScript's `corsMethods` lists them.
pub fn methods(operations: &[OperationInfo]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for operation in operations {
        let method = operation.method.to_ascii_uppercase();
        if !out.contains(&method) {
            out.push(method);
        }
    }
    out
}

/// One segment of an operation's path.
#[derive(Clone, Debug, PartialEq, Eq)]
enum Segment {
    Literal(String),
    /// A `{param}`, which takes any one segment that is not empty.
    Param,
}

/// An operation's route.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Route {
    method: String,
    segments: Vec<Segment>,
}

impl Route {
    fn of(operation: &OperationInfo) -> Self {
        let segments = operation
            .path
            .split('/')
            .map(|part| {
                if part.len() > 2 && part.starts_with('{') && part.ends_with('}') {
                    Segment::Param
                } else {
                    Segment::Literal(part.to_string())
                }
            })
            .collect();
        Route {
            method: operation.method.to_ascii_uppercase(),
            segments,
        }
    }

    fn takes(&self, method: &str, path: &str) -> bool {
        if !self.method.eq_ignore_ascii_case(method) {
            return false;
        }
        let mut parts = path.split('/');
        for segment in &self.segments {
            match (segment, parts.next()) {
                (Segment::Param, Some(part)) if !part.is_empty() => {}
                (Segment::Literal(literal), Some(part)) if literal == part => {}
                _ => return false,
            }
        }
        parts.next().is_none()
    }
}

/// Whether one of `operations` takes `method` on `path`, as TypeScript's
/// `matchOperations` decides: the method, in any case, and the path
/// segment by segment, a `{param}` segment taking any one segment.
pub fn match_operations(operations: &[OperationInfo], method: &str, path: &str) -> bool {
    operations
        .iter()
        .any(|operation| Route::of(operation).takes(method, path))
}

/// One API a server serves, with its policy and the routes its operations
/// take; [`Api::any`] takes every request.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Api {
    policy: Policy,
    routes: Option<Vec<Route>>,
}

impl Api {
    /// The API whose CORS field is `cors` and whose operations are
    /// `operations`, the generated `operations::ALL`: its policy is
    /// [`Policy::new`], and it takes the requests its operations' routes
    /// take.
    pub fn new(cors: &stackconfig::Cors, operations: &[OperationInfo]) -> Self {
        Api::with_policy(Policy::new(cors, operations), operations)
    }

    /// The API of `policy`, taking the requests the routes of `operations`
    /// take.
    pub fn with_policy(policy: Policy, operations: &[OperationInfo]) -> Self {
        Api {
            policy,
            routes: Some(operations.iter().map(Route::of).collect()),
        }
    }

    /// The API of `policy`, taking every request: a server that serves one
    /// API, or a test.
    pub fn any(policy: Policy) -> Self {
        Api {
            policy,
            routes: None,
        }
    }

    /// Its policy.
    pub fn policy(&self) -> &Policy {
        &self.policy
    }

    /// Whether the API's routes take `method` on `path`.
    pub fn takes(&self, method: &str, path: &str) -> bool {
        self.routes
            .as_ref()
            .is_none_or(|routes| routes.iter().any(|route| route.takes(method, path)))
    }
}

/// Answers CORS for the APIs a server serves, around its router, as Go's
/// `cors.Handler` does. A request with an `Origin` is the first API's whose
/// routes take it, by the method a preflight asks for in place of
/// `OPTIONS`: a preflight from an origin its policy lists is answered here,
/// 204 with the policy's headers, and any other request it takes reaches
/// the router, whose answer gets the policy's headers where it set none
/// itself. A preflight from an origin the policy does not list is one of
/// those, which the router answers, through the identity runtime's CORS for
/// a trusted origin. A request no API takes reaches the router as it is.
#[derive(Clone, Debug)]
pub struct SiteCorsLayer {
    apis: Arc<[Api]>,
}

impl SiteCorsLayer {
    pub fn new(apis: Vec<Api>) -> Self {
        SiteCorsLayer { apis: apis.into() }
    }
}

impl<S> tower_layer::Layer<S> for SiteCorsLayer {
    type Service = SiteCors<S>;

    fn layer(&self, inner: S) -> Self::Service {
        SiteCors {
            inner,
            apis: Arc::clone(&self.apis),
        }
    }
}

/// The service [`SiteCorsLayer`] wraps a router's in.
#[derive(Clone, Debug)]
pub struct SiteCors<S> {
    inner: S,
    apis: Arc<[Api]>,
}

impl<S> tower_service::Service<Request> for SiteCors<S>
where
    S: tower_service::Service<Request, Response = Response, Error = Infallible>
        + Clone
        + Send
        + 'static,
    S::Future: Send + 'static,
{
    type Response = Response;
    type Error = Infallible;
    type Future = Pin<Box<dyn Future<Output = Result<Response, Infallible>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, request: Request) -> Self::Future {
        let clone = self.inner.clone();
        let mut inner = std::mem::replace(&mut self.inner, clone);
        let decision = self.decide(&request);
        let Some(decision) = decision else {
            return Box::pin(inner.call(request));
        };
        if decision.answer {
            let mut response = StatusCode::NO_CONTENT.into_response();
            set_headers(response.headers_mut(), &decision.headers, true);
            return Box::pin(async move { Ok(response) });
        }
        Box::pin(async move {
            let mut response = inner.call(request).await?;
            set_headers(response.headers_mut(), &decision.headers, false);
            Ok(response)
        })
    }
}

impl<S> SiteCors<S> {
    /// The decision of the API that takes `request`, or `None` for a
    /// request without an `Origin` or one no API takes.
    fn decide(&self, request: &Request) -> Option<Decision> {
        let headers = request.headers();
        if first(headers, &ORIGIN).is_empty() {
            return None;
        }
        let method = if is_preflight(request.method(), headers) {
            std::str::from_utf8(first(headers, &ACCESS_CONTROL_REQUEST_METHOD)).ok()?
        } else {
            request.method().as_str()
        };
        let path = request.uri().path();
        let api = self.apis.iter().find(|api| api.takes(method, path))?;
        Some(api.policy.decide(request.method(), headers))
    }
}

/// Sets a decision's headers on an answer: each in place of what is there
/// for the answer the layer makes, and only where the router set none on
/// one it made, so a CORS check of the API's own keeps its answer.
fn set_headers(headers: &mut HeaderMap, decided: &BTreeMap<&'static str, String>, replace: bool) {
    for (name, value) in decided {
        let (Ok(name), Ok(value)) = (
            HeaderName::from_bytes(name.as_bytes()),
            HeaderValue::from_str(value),
        ) else {
            continue;
        };
        if replace {
            headers.insert(name, value);
        } else {
            headers.entry(name).or_insert(value);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::routing::{get, post};
    use axum::Router;
    use tower::ServiceExt;

    fn operation(method: &'static str, path: &'static str) -> OperationInfo {
        OperationInfo {
            namespace: "Shop",
            name: "op",
            method,
            path,
            requires_auth: false,
            permissions: &[],
            require_ownership: false,
            manual: false,
            rate_limit: None,
            service: None,
        }
    }

    #[test]
    fn a_path_parameter_takes_one_segment() {
        let operations = [
            operation("GET", "/api/products"),
            operation("POST", "/api/carts/{cartId}/lines"),
        ];
        assert!(match_operations(&operations, "POST", "/api/carts/42/lines"));
        assert!(match_operations(&operations, "post", "/api/carts/42/lines"));
        assert!(!match_operations(
            &operations,
            "POST",
            "/api/carts/42/43/lines"
        ));
        assert!(!match_operations(&operations, "POST", "/api/carts//lines"));
        assert!(!match_operations(&operations, "GET", "/api/carts/42/lines"));
        assert!(match_operations(&operations, "GET", "/api/products"));
        assert!(!match_operations(&operations, "GET", "/api/products/"));
        assert!(!match_operations(&operations, "GET", "/api"));
    }

    #[test]
    fn the_methods_of_the_operations_each_once() {
        let operations = [
            operation("GET", "/api/products"),
            operation("POST", "/api/carts/{cartId}/lines"),
            operation("get", "/api/products/{id}"),
        ];
        assert_eq!(methods(&operations), ["GET", "POST"]);
        let policy = Policy::new(
            &stackconfig::Cors {
                origins: vec!["https://a.dev".to_string()],
            },
            &operations,
        );
        assert_eq!(policy.methods, ["GET", "POST"]);
        assert_eq!(policy.origins, ["https://a.dev"]);
    }

    async fn send(
        app: &Router,
        method: &str,
        path: &str,
        origin: &str,
        ask: Option<&str>,
    ) -> Response {
        let mut request = http::Request::builder()
            .method(method)
            .uri(path)
            .header("Origin", origin);
        if let Some(ask) = ask {
            request = request.header("Access-Control-Request-Method", ask);
        }
        app.clone()
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap()
    }

    fn allowed(response: &Response) -> Option<&str> {
        response
            .headers()
            .get("access-control-allow-origin")
            .map(|value| value.to_str().unwrap())
    }

    /// Go's `TestHandlerPicksTheAPIThatTakesTheRequest`.
    #[tokio::test]
    async fn a_request_is_the_first_api_whose_routes_take_it() {
        let policy = |origin: &str, method: &str| Policy {
            origins: vec![origin.to_string()],
            methods: vec![method.to_string()],
        };
        let app = Router::new()
            .route("/api/a", get(|| async { "a" }))
            .route("/api/b", post(|| async { "b" }))
            .route("/api/c", get(|| async { "c" }).options(|| async { "c" }))
            .layer(SiteCorsLayer::new(vec![
                Api::with_policy(
                    policy("https://a.dev", "GET"),
                    &[operation("GET", "/api/a")],
                ),
                Api::with_policy(
                    policy("https://b.dev", "POST"),
                    &[operation("POST", "/api/b")],
                ),
            ]));
        let cases = [
            (
                "a's preflight from a",
                "OPTIONS",
                "/api/a",
                "https://a.dev",
                Some("GET"),
                StatusCode::NO_CONTENT,
                Some("https://a.dev"),
            ),
            (
                "b's preflight from a",
                "OPTIONS",
                "/api/b",
                "https://a.dev",
                Some("POST"),
                StatusCode::METHOD_NOT_ALLOWED,
                None,
            ),
            (
                "b's preflight from b",
                "OPTIONS",
                "/api/b",
                "https://b.dev",
                Some("POST"),
                StatusCode::NO_CONTENT,
                Some("https://b.dev"),
            ),
            (
                "b's request from b",
                "POST",
                "/api/b",
                "https://b.dev",
                None,
                StatusCode::OK,
                Some("https://b.dev"),
            ),
            (
                "a preflight neither takes",
                "OPTIONS",
                "/api/c",
                "https://a.dev",
                Some("GET"),
                StatusCode::OK,
                None,
            ),
        ];
        for (name, method, path, origin, ask, status, want) in cases {
            let response = send(&app, method, path, origin, ask).await;
            assert_eq!(response.status(), status, "{name}");
            assert_eq!(allowed(&response), want, "{name}");
        }
        let unlisted = send(&app, "OPTIONS", "/api/b", "https://a.dev", Some("POST")).await;
        assert_eq!(unlisted.headers()["vary"], PREFLIGHT_VARY);
        let neither = send(&app, "OPTIONS", "/api/c", "https://a.dev", Some("GET")).await;
        assert!(neither.headers().get("vary").is_none());
    }

    /// Go's `TestHandlerLeavesTheIdentityRuntimeItsTrustedOrigins`: an API
    /// that a site calls and whose identity config trusts another origin
    /// answers both, the site layer around the router and the identity
    /// runtime's CORS on it.
    #[cfg(feature = "identity")]
    #[tokio::test]
    async fn the_identity_runtime_answers_its_trusted_origins() {
        const SITE: &str = "https://shop-web.acme.dev";
        const TRUSTED: &str = "https://admin.acme.dev";
        const OTHER: &str = "https://evil.example";
        let operations = [
            operation("GET", "/api/products"),
            operation("POST", "/api/auth/login"),
        ];
        let site = stackconfig::Cors {
            origins: vec![SITE.to_string()],
        };
        let app = Router::new()
            .route("/api/products", get(|| async { "ok" }))
            .route("/api/auth/login", post(|| async { "ok" }))
            .layer(crate::identity::CorsLayer::new(vec![TRUSTED.to_string()]))
            .layer(SiteCorsLayer::new(vec![Api::new(&site, &operations)]));
        let preflight = |origin: &'static str| {
            let app = app.clone();
            async move {
                let request = http::Request::builder()
                    .method("OPTIONS")
                    .uri("/api/auth/login")
                    .header("Origin", origin)
                    .header("Access-Control-Request-Method", "POST")
                    .header("Access-Control-Request-Headers", "content-type")
                    .body(Body::empty())
                    .unwrap();
                app.oneshot(request).await.unwrap()
            }
        };
        let header = |response: &Response, name: &str| {
            response
                .headers()
                .get(name)
                .map(|value| value.to_str().unwrap().to_string())
        };

        let response = preflight(SITE).await;
        assert_eq!(response.status(), StatusCode::NO_CONTENT);
        assert_eq!(allowed(&response), Some(SITE));
        assert_eq!(
            header(&response, "access-control-allow-methods").as_deref(),
            Some("GET, POST")
        );
        assert_eq!(
            header(&response, "access-control-allow-headers").as_deref(),
            Some("Accept, Authorization, Content-Type")
        );

        let response = preflight(TRUSTED).await;
        assert_eq!(response.status(), StatusCode::NO_CONTENT);
        assert_eq!(allowed(&response), Some(TRUSTED));
        assert_eq!(
            header(&response, "access-control-allow-credentials").as_deref(),
            Some("true")
        );
        assert_eq!(
            header(&response, "access-control-allow-methods").as_deref(),
            Some("POST")
        );
        assert_eq!(
            header(&response, "access-control-allow-headers").as_deref(),
            Some("content-type")
        );

        for origin in [SITE, TRUSTED] {
            let response = send(&app, "GET", "/api/products", origin, None).await;
            assert_eq!(response.status(), StatusCode::OK, "{origin}");
            assert_eq!(allowed(&response), Some(origin));
            assert_eq!(
                header(&response, "access-control-allow-credentials").as_deref(),
                Some("true")
            );
        }

        let response = preflight(OTHER).await;
        assert_eq!(response.status(), StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(allowed(&response), None);
        let response = send(&app, "GET", "/api/products", OTHER, None).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(allowed(&response), None);
        assert_eq!(header(&response, "vary").as_deref(), Some("Origin"));
    }
}
