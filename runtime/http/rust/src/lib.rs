//! Shared runtime crate for generated Rust REST APIs.

mod context;
mod error;
mod response;

pub use context::RequestContext;
pub use error::ApiError;
pub use response::{error_response, json_response, request_id_from_headers, wrap_envelope};
