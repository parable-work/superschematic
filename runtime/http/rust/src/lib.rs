//! Shared runtime crate for generated Rust REST APIs.

mod auth;
mod context;
mod controls;
mod error;
mod openapi;
mod path;
mod ratelimit;
mod response;

pub use auth::{bearer_token, covers, has_any_permission, Authenticator, Principal};
pub use context::RequestContext;
pub use controls::RouteControls;
pub use error::ApiError;
pub use openapi::{
    openapi_document, openapi_router, rapidoc_html, RouterOptions, DEFAULT_OPENAPI_BASE_URL,
    DEFAULT_OPENAPI_VERSION,
};
pub use path::path_is_percent_encoded;
pub use ratelimit::{client_key, ClientIp, RateLimiter};
pub use response::{error_response, json_response, request_id_from_headers, wrap_envelope};
