//! Shared runtime crate for generated Rust REST APIs.

mod auth;
mod context;
mod controls;
mod error;
#[cfg(feature = "http-client")]
mod http_client;
mod jws;
mod jwt;
mod path;
mod ratelimit;
mod response;
mod service_auth;
mod token_source;

pub use auth::{bearer_token, covers, has_any_permission, Authenticator, Principal};
pub use context::RequestContext;
pub use controls::RouteControls;
pub use error::ApiError;
#[cfg(feature = "http-client")]
pub use http_client::HttpFetcher;
pub use jws::JwsAlgorithm;
pub use jwt::{JwtServiceAuthenticator, KeyFetcher};
pub use path::path_is_percent_encoded;
pub use ratelimit::{client_key, ClientIp, RateLimiter};
pub use response::{error_response, json_response, request_id_from_headers, wrap_envelope};
pub use service_auth::{
    ServiceAuthConfig, ServiceAuthenticator, ServiceCaller, ServiceCallerConfig, ServiceIssuer,
    SERVICE_AUTHORIZATION,
};
pub use token_source::{
    GoogleIdTokenSource, HttpGetter, SignedKeySource, TokenFileSource, TokenFuture, TokenSource,
};
