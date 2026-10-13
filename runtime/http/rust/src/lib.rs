//! Shared runtime crate for generated Rust REST APIs.

/// The schema runtime the generated types crates validate with: its
/// `Pattern`, `ParseError` and `UnknownFields` are the ones a parameter
/// spec and an input parse share with them.
pub use superschematic_schema_runtime as schema;

mod auth;
mod body;
mod context;
mod controls;
// A site's CORS (D55), which a stack server answers for the APIs it serves.
pub mod cors;
mod error;
#[cfg(feature = "http-client")]
mod http_client;
/// The user model's runtime (D50), with the `identity` feature.
#[cfg(feature = "identity")]
pub mod identity;
mod jws;
mod jwt;
// A stack server's JSON-line log (D57), with the `logging` feature.
#[cfg(feature = "logging")]
pub mod logging;
mod openapi;
mod operation;
mod params;
mod path;
// A stack server's database (D57), with the `postgres` feature.
#[cfg(feature = "postgres")]
pub mod postgres;
mod ratelimit;
mod request_id;
mod response;
mod service_auth;
// The readers of the config fields an API's edges derive in a stack (D57).
pub mod stackconfig;
mod token_source;

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
pub use openapi::{
    openapi_document, openapi_router, rapidoc_html, RouterOptions, DEFAULT_OPENAPI_BASE_URL,
    DEFAULT_OPENAPI_VERSION,
};
pub use operation::{
    body_fields, check_input, input, input_refusal, operation_response, required_input, InputParse,
    OperationInfo,
};
pub use params::{
    decode_json_param, decode_param, ObjectPrepare, ParamKind, ParamLocation, ParamSpec,
    QueryValues, ScalarCheck, ScalarConstraints,
};
pub use path::path_is_percent_encoded;
pub use ratelimit::{client_key, ClientIp, RateLimiter};
pub use request_id::{request_id_of, request_ids};
pub use response::{
    error_response, json_response, problem_body, request_id_from_headers, status_title,
    wrap_envelope, PROBLEM_CONTENT_TYPE,
};
pub use service_auth::{
    ServiceAuthConfig, ServiceAuthenticator, ServiceCaller, ServiceCallerConfig, ServiceIssuer,
    SERVICE_AUTHORIZATION,
};
pub use token_source::{
    GoogleIdTokenSource, HttpGetter, SignedKeySource, TokenFileSource, TokenFuture, TokenSource,
};
