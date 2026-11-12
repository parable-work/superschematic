//! The MCP endpoint of a generated API (D81, D116): `POST /api/mcp` serves
//! the API's visible `@mcp` operations as MCP tools over the streamable
//! HTTP transport, statelessly, as the TypeScript runtime's `mcp.ts` serves
//! them and the engine serves its own (`runtime/engine/rust/engine/src/
//! api/mcp.rs`, whose conventions this module copies; the runtime does not
//! depend on the engine). The three are held to the same recorded
//! exchanges, `runtime/engine/testdata/http/mcp.json`.
//!
//! - The 2025 revisions (a request with no per-request `_meta` envelope):
//!   `initialize`, negotiating one of [`MCP_INITIALIZE_VERSIONS`], `ping`,
//!   `tools/list` and `tools/call`, alone or in a batch. A request answers
//!   200 with a `text/event-stream` body of one `message` event per
//!   request; notifications and posted responses alone answer 202.
//! - 2026-07-28 (a request whose `params._meta` carries the
//!   `io.modelcontextprotocol/protocolVersion` claim): `server/discover`,
//!   `ping`, `tools/list` and `tools/call`, each answered as one JSON body,
//!   every result marked complete and stamped with the server's identity.
//! - GET and DELETE answer 405: a stateless server keeps no session and
//!   opens no stream.
//!
//! [`serve`] is the transport over any [`ToolSet`]. It takes a request
//! whose body is read and answers a response whose body is bytes, so it
//! needs no HTTP server; it awaits its tool set and its confirmation store
//! and nothing else, so it needs no executor beyond one that polls it. A
//! generated router mounts it as a route of its own ([`Mount`], which its
//! [`crate::RouterOptions`]' [`EndpointOptions`] configure), with a tool
//! set whose call is a request to the tool's route as the caller the
//! endpoint's own request authenticated (D116): [`RouteTool`] is a tool of
//! that table, [`tool_request`] the request a call makes, and
//! [`outcome_of`] what the route's response comes to. A call the route
//! refuses returns a tool error (`isError`) whose structured content is the
//! problem document the route answered; a tool the caller has not got is a
//! JSON-RPC invalid-params error.
//!
//! A tool whose invocation policy is ask runs only once the person confirms
//! the call (D93; [`ToolSet::confirms`], [`Confirmations`]):
//!
//! - A 2026-07-28 client whose envelope's capabilities elicit forms is
//!   asked in band: the call answers `input_required` with one
//!   `elicitation/create`, whose message is the call's summary, and its
//!   `requestState` a confirmation the client sends back with the person's
//!   answer. `accept` runs the call; `decline` and `cancel` are a tool
//!   error, `confirmation_declined`, and run nothing.
//! - Any other client, every client of the 2025 revisions among them, gets
//!   a two-step confirmation: the call answers a tool error,
//!   `confirmation_required`, whose details hold the summary and a
//!   `confirmationToken`, and runs nothing; the same call with that token
//!   in its `confirmationToken` argument runs.
//!
//! A token binds the tool, the caller and the exact arguments, expires, is
//! claimed once, and is an HMAC under the endpoint's confirmation key; one
//! that does not redeem asks again, saying why.
//!
//! Only a call that would reach the tool's implementation is confirmed
//! (D98): before the endpoint mints a confirmation, asks a client to
//! elicit, or redeems a token or a `requestState`, it checks the call
//! ([`ToolSet::check`]), on a generated router through its route's own
//! checks. A call they refuse answers their refusal at once, a tool error
//! carrying the route's problem, and mints and claims nothing.

mod confirm;
mod json;
mod mount;
mod route;
mod transport;

use crate::BoxError;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use std::fmt;
use std::sync::Arc;

pub use confirm::{
    ConfirmationBinding, ConfirmationClock, ConfirmationMode, ConfirmationRefusal,
    ConfirmationStore, ConfirmationToken, Confirmations, ConfirmationsError,
    MemoryConfirmationStore, CONFIRMATION_ARGUMENT, CONFIRMATION_DECLINED, CONFIRMATION_INPUT,
    CONFIRMATION_REQUIRED, DEFAULT_CONFIRMATION_TTL_SECONDS, DEFAULT_MAX_CONFIRMATION_CLAIMS,
    MIN_CONFIRMATION_KEY_BYTES,
};
pub use json::canonical_json;
pub use mount::{reached, ConfirmationKey, EndpointOptions, Mount};
pub use route::{
    caller_of, check_route_tools, outcome_of, problem_of, tool_request, with_confirmation_argument,
    ArgumentLocation, RouteTool, ToolArgument,
};
pub use transport::serve;

/// The 2025 revisions `initialize` negotiates, the newest first: the one a
/// client asks for when it is here, else the first.
pub const MCP_INITIALIZE_VERSIONS: [&str; 5] = [
    "2025-11-25",
    "2025-06-18",
    "2025-03-26",
    "2024-11-05",
    "2024-10-07",
];

/// The revisions `server/discover` offers: those a request names in its
/// `_meta` envelope.
pub const MCP_DISCOVER_VERSIONS: [&str; 1] = ["2026-07-28"];

/// The largest body the endpoint reads, the official SDK's limit.
pub const MCP_MAX_BODY_BYTES: usize = 4 * 1024 * 1024;

/// The most messages a batch holds, the official SDK's limit.
pub const MCP_MAX_BATCH: usize = 100;

/// Where a generated router serves its MCP endpoint.
pub const MCP_PATH: &str = "/api/mcp";

/// A problem document (RFC 9457) as JSON: what [`crate::problem_body`]
/// writes, or the object a route answered a refusal with.
pub type Problem = Value;

/// The name and version `initialize` and every 2026-07-28 result report.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ServerInfo {
    /// The server's name: the API's by default.
    pub name: String,
    /// The server's version: the API's served OpenAPI version by default.
    pub version: String,
}

impl ServerInfo {
    /// The server info of `name` at `version`.
    pub fn new(name: impl Into<String>, version: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            version: version.into(),
        }
    }
}

/// What the transport says about the server, and what one request brings
/// to it: its caller and its id.
#[derive(Clone, Debug)]
pub struct Options {
    /// The name and version the server reports.
    pub server_info: ServerInfo,
    /// Instructions `initialize` gives the client; none when absent or
    /// empty.
    pub instructions: Option<String>,
    /// Mints and redeems the confirmations of the tools the tool set's
    /// [`ToolSet::confirms`] names (D93). Without it a call of such a tool
    /// is refused and runs nothing.
    pub confirmations: Option<Arc<Confirmations>>,
    /// Who is calling, which a confirmation is bound to ([`caller_of`]);
    /// empty for no one.
    pub caller: String,
    /// The request id a confirmation's problem carries; a fresh one unless
    /// given.
    pub request_id: Option<String>,
}

impl Options {
    /// The options of a server that reports `server_info`, with no
    /// instructions, no confirmations, no caller and no request id.
    pub fn new(server_info: ServerInfo) -> Self {
        Self {
            server_info,
            instructions: None,
            confirmations: None,
            caller: String::new(),
            request_id: None,
        }
    }

    /// The options with the instructions `initialize` gives the client.
    #[must_use]
    pub fn with_instructions(mut self, instructions: impl Into<String>) -> Self {
        self.instructions = Some(instructions.into());
        self
    }

    /// The options with the confirmations of the tools that ask.
    #[must_use]
    pub fn with_confirmations(mut self, confirmations: Arc<Confirmations>) -> Self {
        self.confirmations = Some(confirmations);
        self
    }

    /// The options of a request from `caller`.
    #[must_use]
    pub fn with_caller(mut self, caller: impl Into<String>) -> Self {
        self.caller = caller.into();
        self
    }

    /// The options of the request whose id is `request_id`.
    #[must_use]
    pub fn with_request_id(mut self, request_id: impl Into<String>) -> Self {
        self.request_id = Some(request_id.into());
        self
    }
}

/// A tool as `tools/list` answers it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Tool {
    /// The tool's handle, its wire identifier.
    pub name: String,
    /// The tool's title, which a confirmation's summary leads with.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// What the tool does.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// The JSON Schema of its arguments.
    #[serde(rename = "inputSchema")]
    pub input_schema: Value,
    /// Its MCP annotations, `readOnlyHint` among them.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub annotations: Option<Map<String, Value>>,
    /// Its `_meta`: the guidance and the invocation policy.
    #[serde(rename = "_meta", default, skip_serializing_if = "Option::is_none")]
    pub meta: Option<Map<String, Value>>,
}

/// One item of a tool result's content.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Content {
    /// Text.
    Text {
        /// The text.
        text: String,
    },
    /// A body that is neither JSON nor text, embedded.
    Resource {
        /// Where the resource is: the route's path.
        uri: String,
        /// Its media type.
        mime_type: String,
        /// Its bytes, in base64.
        blob: String,
    },
}

impl Content {
    /// The item as a tool result's `content` holds it.
    pub fn to_value(&self) -> Value {
        match self {
            Self::Text { text } => json!({"type": "text", "text": text}),
            Self::Resource {
                uri,
                mime_type,
                blob,
            } => json!({
                "type": "resource",
                "resource": {"uri": uri, "mimeType": mime_type, "blob": blob},
            }),
        }
    }
}

/// What a tool call came to.
#[derive(Clone, Debug, PartialEq)]
pub enum CallOutcome {
    /// A JSON result, sent as text and, for an object, as structured
    /// content.
    Result(Value),
    /// Content of another kind, sent as it is.
    Content(Vec<Content>),
    /// A refusal, sent as a tool error whose structured content is the
    /// problem document.
    Problem(Problem),
}

/// Why a tool set did not answer.
#[derive(Debug)]
pub enum ToolError {
    /// A tool the set has not got, or does not offer the caller: the
    /// JSON-RPC invalid-params error, with this message.
    UnknownTool(String),
    /// Any other failure: the JSON-RPC internal error, whose message stays
    /// off the wire and goes to the log.
    Failed(BoxError),
}

impl ToolError {
    /// The error of a tool the set has not got, as `message` says it: on a
    /// generated router, `<api> has no tool "<name>"`.
    pub fn unknown(message: impl Into<String>) -> Self {
        Self::UnknownTool(message.into())
    }
}

impl fmt::Display for ToolError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::UnknownTool(message) => f.write_str(message),
            Self::Failed(error) => error.fmt(f),
        }
    }
}

impl std::error::Error for ToolError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::UnknownTool(_) => None,
            Self::Failed(error) => Some(error.as_ref()),
        }
    }
}

impl From<BoxError> for ToolError {
    fn from(error: BoxError) -> Self {
        Self::Failed(error)
    }
}

/// The tools one request is served: those its caller is offered, and how a
/// call runs. Its methods are asynchronous because a call is a request to
/// a route, whose implementation is (D116); [`serve`] awaits them in turn.
#[async_trait]
pub trait ToolSet: Send + Sync {
    /// The tools the caller is offered, as `tools/list` answers them.
    async fn list(&self) -> Result<Vec<Tool>, ToolError>;

    /// Runs the tool `name` with `arguments`. [`ToolError::UnknownTool`]
    /// for a tool the set has not got.
    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError>;

    /// The tool `name` names when a call of it runs only once the person
    /// confirms it, its invocation policy being ask (D93); `None` for a
    /// tool that runs as called, or one the set has not got, whose call
    /// refuses it. The endpoint confirms the call before it calls
    /// [`ToolSet::call`], which never sees the `confirmationToken`
    /// argument. Without it no tool confirms.
    async fn confirms(&self, _name: &str) -> Result<Option<Tool>, ToolError> {
        Ok(None)
    }

    /// The refusal a call of the tool `name` with `arguments` would meet
    /// before its implementation runs, its problem document; `None` for a
    /// call that would reach the implementation (D98). The endpoint checks
    /// a call of a tool [`ToolSet::confirms`] names with the arguments the
    /// confirmation binds, the `confirmationToken`'s taken out, before it
    /// asks the person or redeems a confirmation, and answers a refusal at
    /// once, so a call that cannot run asks no one and spends nothing.
    /// [`ToolError::UnknownTool`] for a tool the set has not got, as
    /// [`ToolSet::call`]. Without it every such call is confirmed, and
    /// refused, if at all, once it runs.
    async fn check(
        &self,
        _name: &str,
        _arguments: &Map<String, Value>,
    ) -> Result<Option<Problem>, ToolError> {
        Ok(None)
    }
}
