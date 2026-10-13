//! The MCP endpoint (`src/mcp/app.ts`): the next commits write it. Until
//! then it answers 501.

use super::problems::Problem;
use super::{ApiOptions, Matched, Request, Response};
use crate::principal::Principal;
use crate::Engine;

/// The MCP protocol revisions the endpoint negotiates.
pub const MCP_PROTOCOL_VERSIONS: [&str; 0] = [];

/// The MCP endpoint's server info and instructions.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct McpOptions {
    pub server_name: Option<String>,
    pub server_version: Option<String>,
    pub instructions: Option<String>,
}

pub(crate) fn serve(
    _engine: &Engine,
    _options: &ApiOptions,
    _request: &Request,
    _matched: &Matched,
    _principal: &Principal,
) -> Result<Response, Problem> {
    Err(Problem::new(
        501,
        "The MCP endpoint is not served yet",
        "not_implemented",
    ))
}
