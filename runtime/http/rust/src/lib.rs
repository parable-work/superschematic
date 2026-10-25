//! Shared runtime crate for generated Rust REST APIs.

/// The schema runtime the generated types crates validate with: its
/// `Pattern`, `ParseError` and `UnknownFields` are the ones a parameter
/// spec and an input parse share with them.
pub use superschematic_schema_runtime as schema;

mod auth;
mod body;
// The provider-neutral interface of a bucket an API lists (D54).
pub mod bucket;
#[cfg(any(
    feature = "logging",
    feature = "postgres",
    feature = "gcs",
    feature = "sqlite"
))]
mod civil;
mod context;
mod controls;
// A site's CORS (D55), which a stack server answers for the APIs it serves.
pub mod cors;
mod error;
// A bucket on Google Cloud Storage (D54), with the `gcs` feature.
#[cfg(feature = "gcs")]
pub mod gcs;
#[cfg(feature = "http-client")]
mod http_client;
/// The user model's runtime (D50), with the `identity` feature.
#[cfg(feature = "identity")]
pub mod identity;
mod jws;
mod jwt;
mod loopback;
// A stack server's JSON-line log (D57), with the `logging` feature.
#[cfg(feature = "logging")]
pub mod logging;
mod openapi;
mod operation;
// The page of a @paginated operation (D60): the router's reader, the
// cursor, and the page an implementation returns.
pub mod page;
mod params;
mod path;
// A stack server's database (D57), with the `postgres` feature.
#[cfg(feature = "postgres")]
pub mod postgres;
// A queue's client (D53, D65), with the `postgres` feature, and on SQLite
// with the `sqlite` feature (D53, amended).
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod queue;
mod ratelimit;
mod request_id;
mod response;
// The words, LIKE patterns and case folding of a SearchQuery (D62).
pub mod search;
mod service_auth;
// The cancellation a stack's job and worker stop on (D65).
mod shutdown;
// The readers of the config fields an API's edges derive in a stack (D57).
pub mod stackconfig;
// A stack deployable's trace export over OTLP (D67), with the `otel`
// feature.
#[cfg(feature = "otel")]
pub mod telemetry;
mod token_source;
#[cfg(any(feature = "identity", feature = "postgres", feature = "sqlite"))]
mod uuid62;
// A webhook verifier over a JWT carrying the body's SHA-256 (D81).
mod webhook;
// A worker's claim loop (D53, D65), with the `postgres` or `sqlite`
// feature.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod worker;

/// The error a job's or a worker's method returns (D65), and any other
/// error a generated crate passes on without naming its type.
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

pub use auth::{admit, bearer_token, covers, has_any_permission, Authenticator, Principal};
pub use body::{json_body, query_values};
pub use context::RequestContext;
pub use controls::RouteControls;
pub use error::ApiError;
#[cfg(feature = "http-client")]
pub use http_client::HttpFetcher;
#[cfg(feature = "identity")]
pub use identity::IdentityAuthenticator;
pub use jws::{ed25519_public_jwk_from_pem, is_pem, JwsAlgorithm};
pub use jwt::{JwtServiceAuthenticator, KeyFetcher};
pub use loopback::{is_loopback_url, Loopback, LOOPBACK_LIFETIME_SECONDS};
pub use openapi::{
    openapi_document, openapi_router, rapidoc_html, RouterOptions, DEFAULT_OPENAPI_BASE_URL,
    DEFAULT_OPENAPI_VERSION,
};
pub use operation::{
    body_fields, check_input, input, input_refusal, operation_response, required_input, InputParse,
    OperationInfo, ServiceClause, ServiceMode,
};
pub use params::{
    decode_json_param, decode_param, ObjectPrepare, ParamKind, ParamLocation, ParamSpec,
    QueryValues, ScalarCheck, ScalarConstraints,
};
pub use path::path_is_percent_encoded;
pub use ratelimit::{client_key, ClientIp, RateLimiter, RateLimits};
pub use request_id::{request_id_of, request_ids};
pub use response::{
    error_response, json_response, problem_body, request_id_from_headers, status_title,
    wrap_envelope, PROBLEM_CONTENT_TYPE,
};
pub use service_auth::{
    ServiceAuthConfig, ServiceAuthenticator, ServiceCaller, ServiceCallerConfig, ServiceIssuer,
    SERVICE_AUTHORIZATION,
};
pub use shutdown::{CancellationToken, Shutdown};
pub use token_source::{
    GoogleIdTokenSource, HttpGetter, SignedKeySource, TokenFileSource, TokenFuture, TokenSource,
};
pub use webhook::{
    JwtBodyHashOptions, JwtBodyHashVerifier, WebhookKeyLookup, WebhookKeys, INVALID_SIGNATURE_CODE,
};
