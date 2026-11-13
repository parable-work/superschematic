//! An API's MCP endpoint on its generated router (D81, D116): the port of
//! the TypeScript runtime's `mountMcp` and `routerTools`. [`Mount::router`]
//! is the router of `POST`, `GET` and `DELETE /api/mcp`, which a generated
//! `build_router_with` merges into its own, so the endpoint is a route of
//! the router and of the generated `Api::handle` alike.
//!
//! The endpoint goes through the runtime like every route: its controls,
//! with the router's authenticators, establish the caller, and the list
//! holds the tools whose routes that caller may call. A `tools/call` is a
//! request to its operation's route, answered through [`crate::handle`] by
//! the router's own routes, carrying the endpoint request's credentials and
//! what its host said of the connection, so the route's service step,
//! permission check, row rule and validation apply unchanged. A call of a
//! tool that asks is first a request to the route's check (D98), on a
//! router of the checks that no client reaches.

use super::{
    caller_of, check_route_tools, outcome_of, problem_of, serve, tool_request, CallOutcome,
    ConfirmationStore, Confirmations, Options, Problem, RouteTool, ServerInfo, Tool, ToolError,
    ToolSet, MCP_MAX_BODY_BYTES, MCP_PATH,
};
use crate::request_id::{request_id_of, request_ids};
use crate::response::problem_body;
use crate::{
    error_response, handle, ApiError, Authenticator, OperationInfo, Principal, RouteControls,
    ServiceAuthenticator, ServiceCaller, DEFAULT_OPENAPI_VERSION,
};
use async_trait::async_trait;
use axum::body::Body;
use axum::extract::{MatchedPath, OriginalUri, Request, State};
use axum::middleware::from_fn;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::Router;
use bytes::{Bytes, BytesMut};
use http::Method;
use http_body_util::BodyExt;
use serde_json::{Map, Value};
use std::fmt;
use std::sync::Arc;

/// The key an endpoint signs its confirmations with (D93): at least
/// [`super::MIN_CONFIRMATION_KEY_BYTES`] bytes, random, the same on every
/// instance of the server. A generated entrypoint passes the `@envVars`
/// secret `outputs.api.mcpConfirmationKey` names.
#[derive(Clone, PartialEq, Eq)]
pub struct ConfirmationKey(Vec<u8>);

impl ConfirmationKey {
    /// The key's bytes.
    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }
}

// Debug leaves the key out.
impl fmt::Debug for ConfirmationKey {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("ConfirmationKey(..)")
    }
}

impl From<Vec<u8>> for ConfirmationKey {
    fn from(key: Vec<u8>) -> Self {
        Self(key)
    }
}

impl From<&[u8]> for ConfirmationKey {
    fn from(key: &[u8]) -> Self {
        Self(key.to_vec())
    }
}

impl From<String> for ConfirmationKey {
    fn from(key: String) -> Self {
        Self(key.into_bytes())
    }
}

impl From<&str> for ConfirmationKey {
    fn from(key: &str) -> Self {
        Self(key.as_bytes().to_vec())
    }
}

/// The router options of an API's MCP endpoint: [`crate::RouterOptions`]'s
/// `mcp`, the TypeScript runtime's `McpOptions`. A router whose API has
/// `outputs.api.mcp` off reads none of it.
///
/// Two options are equal when their fields are and they share one
/// confirmation store, which compares by identity.
#[derive(Clone, Default)]
pub struct EndpointOptions {
    /// The name and version `initialize` reports; the API's name and its
    /// served OpenAPI version by default.
    pub server_info: Option<ServerInfo>,
    /// Instructions `initialize` gives the client.
    pub instructions: Option<String>,
    /// Requests per minute per client on the endpoint itself; none or 0
    /// for no limit. Each call's route keeps its own `@rateLimit`.
    pub rate_limit_per_minute: Option<u32>,
    /// Seconds a request to the endpoint may take before it answers 504;
    /// none or 0 for no limit. Each call's route keeps its own `@timeout`.
    pub timeout_seconds: Option<u64>,
    /// The key the endpoint signs its confirmations with (D93). Required
    /// when a tool confirms.
    pub confirmation_key: Option<ConfirmationKey>,
    /// Seconds a confirmation is good for;
    /// [`super::DEFAULT_CONFIRMATION_TTL_SECONDS`] unless set.
    pub confirmation_ttl_seconds: Option<u64>,
    /// Where a confirmation is claimed so it redeems once: a store in the
    /// process's memory unless given, so a token is single-use per
    /// instance. A server of several instances passes one they share.
    pub confirmation_store: Option<Arc<dyn ConfirmationStore>>,
}

impl EndpointOptions {
    /// The options with the key the endpoint signs its confirmations with.
    #[must_use]
    pub fn with_confirmation_key(mut self, key: impl Into<ConfirmationKey>) -> Self {
        self.confirmation_key = Some(key.into());
        self
    }

    /// The options with the store the server's instances share.
    #[must_use]
    pub fn with_confirmation_store(mut self, store: Arc<dyn ConfirmationStore>) -> Self {
        self.confirmation_store = Some(store);
        self
    }
}

// Debug leaves the key out, and says only whether a store is given.
impl fmt::Debug for EndpointOptions {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("EndpointOptions")
            .field("server_info", &self.server_info)
            .field("instructions", &self.instructions)
            .field("rate_limit_per_minute", &self.rate_limit_per_minute)
            .field("timeout_seconds", &self.timeout_seconds)
            .field("confirmation_key", &self.confirmation_key)
            .field("confirmation_ttl_seconds", &self.confirmation_ttl_seconds)
            .field(
                "confirmation_store",
                &self.confirmation_store.as_ref().map(|_| ".."),
            )
            .finish()
    }
}

impl PartialEq for EndpointOptions {
    fn eq(&self, other: &Self) -> bool {
        let same_store = match (&self.confirmation_store, &other.confirmation_store) {
            (None, None) => true,
            (Some(one), Some(other)) => std::ptr::addr_eq(Arc::as_ptr(one), Arc::as_ptr(other)),
            _ => false,
        };
        same_store
            && self.server_info == other.server_info
            && self.instructions == other.instructions
            && self.rate_limit_per_minute == other.rate_limit_per_minute
            && self.timeout_seconds == other.timeout_seconds
            && self.confirmation_key == other.confirmation_key
            && self.confirmation_ttl_seconds == other.confirmation_ttl_seconds
    }
}

impl Eq for EndpointOptions {}

/// What a generated router says about its MCP endpoint, and what it hands
/// it: the TypeScript runtime's `McpMount` with the router's parts beside
/// it.
pub struct Mount<'a> {
    /// The API's name: the server's default name, the scope a confirmation
    /// is bound to, and the name in an unknown tool's error.
    pub api: &'a str,
    /// The server's default version: the router's served OpenAPI version.
    pub version: &'a str,
    /// Whether the endpoint needs a caller: true when a route of the API
    /// does, so the endpoint authenticates as those routes do and answers
    /// 401 without a credential.
    pub requires_auth: bool,
    /// The build's tool table, as JSON: a list of [`RouteTool`], each
    /// naming its operation as `<namespace>.<name>`.
    pub tools: &'a str,
    /// The router's operation table, the generated `operations::ALL`.
    pub operations: &'a [OperationInfo],
    /// The router's routes, which a call runs through.
    pub routes: Router,
    /// The routes' checks (D98): a router that answers a request with what
    /// its route would refuse it with before the implementation runs, and
    /// with a success otherwise. None checks no call, and each is confirmed
    /// and refused, if at all, once it runs.
    pub checks: Option<Router>,
    /// Whether `routes` holds the `@manualRouteRegistration` operations'
    /// routes. A tool of one answers 501 where it does not.
    pub manual_routes: bool,
    /// The router's authenticator: it establishes the endpoint's caller,
    /// and says which tools' routes that caller may call. None for a router
    /// no route of which needs a caller.
    pub authenticator: Option<Arc<dyn Authenticator>>,
    /// The router's service authenticator, which verifies a service
    /// credential the endpoint's request carries (D37). None for a router
    /// with no service step.
    pub service_authenticator: Option<Arc<dyn ServiceAuthenticator>>,
    /// Whether the router serves with a transport under it
    /// ([`crate::RouterOptions::transport`]): without one the endpoint's
    /// rate limit and timeout are its host's (D116).
    pub transport: bool,
}

impl Mount<'_> {
    /// The router of the API's MCP endpoint: `POST /api/mcp`, and `GET` and
    /// `DELETE`, which answer 405, each behind the endpoint's controls.
    /// Each request lists the tools whose routes its caller may call, by
    /// the route's auth requirement and the authenticator's `permits`, and
    /// runs a call through the routes, a call of a tool that confirms once
    /// the person confirms it, bound to the caller (D93), and only once its
    /// route's check admits it (D98).
    ///
    /// The `Err` says what is wrong with the mount: a tool table that does
    /// not read, two tools of one name, a tool that names an operation the
    /// router does not have, one that asks and takes the confirmation's
    /// argument, a tool that confirms with no confirmation key or a short
    /// one, and an endpoint that needs a caller with no authenticator.
    pub fn router(self, options: &EndpointOptions) -> Result<Router, String> {
        let api = self.api;
        let tools: Vec<RouteTool> = serde_json::from_str(self.tools)
            .map_err(|error| format!("the MCP tool table of {api} does not read: {error}"))?;
        check_route_tools(&tools)?;
        let mut entries = Vec::with_capacity(tools.len());
        for tool in tools {
            let operation = self
                .operations
                .iter()
                .find(|operation| {
                    tool.operation
                        .strip_prefix(operation.namespace)
                        .and_then(|rest| rest.strip_prefix('.'))
                        == Some(operation.name)
                })
                .copied()
                .ok_or_else(|| {
                    format!(
                        "MCP tool {} names operation {}, which the router of {api} does not have",
                        tool.tool.name, tool.operation
                    )
                })?;
            let method = Method::from_bytes(operation.method.as_bytes()).map_err(|_| {
                format!(
                    "MCP tool {}'s operation {} has the method {}, which is none",
                    tool.tool.name, tool.operation, operation.method
                )
            })?;
            entries.push(Entry {
                tool,
                operation,
                method,
            });
        }
        let confirmations = match entries.iter().find(|entry| entry.tool.confirm) {
            None => None,
            Some(confirming) => {
                let name = &confirming.tool.tool.name;
                let key = options.confirmation_key.as_ref().ok_or_else(|| {
                    format!(
                        "MCP tool {name} of {api} runs only once the person confirms it, so RouterOptions.mcp.confirmation_key is required: a random secret of at least 32 bytes, the same on every instance (D93)"
                    )
                })?;
                let refused = |error| format!("the MCP endpoint of {api}: {error}");
                let mut confirmations = Confirmations::new(key.as_bytes())
                    .map_err(refused)?
                    .with_scope(api);
                if let Some(seconds) = options.confirmation_ttl_seconds {
                    confirmations = confirmations.with_ttl_seconds(seconds).map_err(refused)?;
                }
                if let Some(store) = &options.confirmation_store {
                    confirmations = confirmations.with_store(Arc::clone(store));
                }
                Some(Arc::new(confirmations))
            }
        };
        let version = match self.version {
            "" => DEFAULT_OPENAPI_VERSION,
            version => version,
        };
        let mut server = Options::new(
            options
                .server_info
                .clone()
                .unwrap_or_else(|| ServerInfo::new(api, version)),
        );
        if let Some(instructions) = options
            .instructions
            .as_deref()
            .filter(|text| !text.is_empty())
        {
            server = server.with_instructions(instructions);
        }
        if let Some(confirmations) = confirmations {
            server = server.with_confirmations(confirmations);
        }

        // The endpoint's own controls, in a route's order: its rate limit,
        // the service step, the caller a route of the API needs, then its
        // timeout. It reads its body itself, up to the transport's limit.
        let mut controls = RouteControls::under(self.transport);
        if let Some(per_minute) = options.rate_limit_per_minute.filter(|limit| *limit > 0) {
            controls = controls.rate_limit(per_minute);
        }
        if let Some(authenticator) = &self.service_authenticator {
            controls = controls.identify_service(Arc::clone(authenticator));
        }
        if self.requires_auth {
            let authenticator = self.authenticator.as_ref().ok_or_else(|| {
                format!(
                    "the MCP endpoint of {api} needs a caller, and its router has no authenticator"
                )
            })?;
            controls = controls.authorize(Arc::clone(authenticator), &[]);
        }
        if let Some(seconds) = options.timeout_seconds.filter(|seconds| *seconds > 0) {
            controls = controls.timeout_seconds(seconds);
        }
        let endpoint = Arc::new(Endpoint {
            api: api.to_owned(),
            entries,
            routes: self.routes,
            checks: self.checks,
            manual_routes: self.manual_routes,
            authenticator: self.authenticator,
            server,
        });
        let route = controls.apply(post(answer).get(answer).delete(answer));
        Ok(Router::new()
            .route(MCP_PATH, route)
            .with_state(endpoint)
            .layer(from_fn(request_ids)))
    }
}

// One tool of the endpoint, and the operation whose route a call of it
// runs through.
struct Entry {
    tool: RouteTool,
    operation: OperationInfo,
    method: Method,
}

// A mounted endpoint: what every request to it is served with.
struct Endpoint {
    api: String,
    entries: Vec<Entry>,
    routes: Router,
    checks: Option<Router>,
    manual_routes: bool,
    authenticator: Option<Arc<dyn Authenticator>>,
    // What the transport says about the server, without a request's caller
    // and id.
    server: Options,
}

// answer serves one request to the endpoint, as the caller its controls
// established.
async fn answer(State(endpoint): State<Arc<Endpoint>>, request: Request) -> Response {
    let (mut parts, body) = request.into_parts();
    // What the endpoint's own route put on the request is no part of a
    // call's: the route of the call establishes its own.
    let principal = parts.extensions.remove::<Principal>();
    let service = parts.extensions.remove::<ServiceCaller>();
    parts.extensions.remove::<MatchedPath>();
    parts.extensions.remove::<OriginalUri>();
    let request_id = request_id_of(&parts.headers);
    let Ok(body) = read_capped(body, MCP_MAX_BODY_BYTES).await else {
        return error_response(ApiError::bad_request("The request body could not be read"))
            .into_response();
    };
    let request = http::Request::from_parts(parts, body);
    let options = endpoint
        .server
        .clone()
        .with_caller(caller_of(
            principal
                .as_ref()
                .map(|principal| principal.subject.as_str()),
            service.as_ref().map(|caller| caller.deployable.as_str()),
        ))
        .with_request_id(request_id.as_str());
    let tools = RouterTools {
        endpoint: &endpoint,
        incoming: &request,
        principal,
        request_id: &request_id,
    };
    serve(&request, &tools, &options).await.map(Body::from)
}

// read_capped reads a body up to limit bytes and one frame past it, which
// the transport then refuses as too large without the rest being read.
async fn read_capped(mut body: Body, limit: usize) -> Result<Bytes, axum::Error> {
    let mut read = BytesMut::new();
    while let Some(frame) = body.frame().await {
        if let Ok(data) = frame?.into_data() {
            read.extend_from_slice(&data);
            if read.len() > limit {
                break;
            }
        }
    }
    Ok(read.freeze())
}

// The tools one request to the endpoint is served, as its caller.
struct RouterTools<'a> {
    endpoint: &'a Endpoint,
    incoming: &'a http::Request<Bytes>,
    principal: Option<Principal>,
    request_id: &'a str,
}

impl RouterTools<'_> {
    // offered is whether the caller may call the tool's route: its auth
    // requirement, by the authenticator's permits, admits the caller the
    // endpoint's controls established.
    fn offered(&self, entry: &Entry) -> bool {
        if !entry.operation.requires_auth {
            return true;
        }
        self.endpoint
            .authenticator
            .as_ref()
            .is_some_and(|authenticator| {
                entry
                    .operation
                    .admit_end_user(authenticator.as_ref(), self.principal.clone())
                    .is_ok()
            })
    }

    // entry is the tool the caller is offered under name.
    fn entry(&self, name: &str) -> Result<&Entry, ToolError> {
        self.endpoint
            .entries
            .iter()
            .find(|entry| entry.tool.tool.name == name)
            .filter(|entry| self.offered(entry))
            .ok_or_else(|| {
                ToolError::unknown(format!(
                    "{} has no tool {}",
                    self.endpoint.api,
                    Value::from(name)
                ))
            })
    }

    // request_of is the request a call of the tool makes to its route, with
    // what the endpoint's host said of the connection, which the route's
    // authenticator reads as the endpoint's did (D116); or the refusal of
    // arguments no request carries, and of a manual operation's tool on a
    // router that holds no route for it.
    fn request_of(
        &self,
        entry: &Entry,
        arguments: &Map<String, Value>,
    ) -> Result<http::Request<Bytes>, Problem> {
        let mut request = tool_request(
            &entry.tool,
            &entry.method,
            entry.operation.path,
            arguments,
            self.incoming,
            self.request_id,
        )?;
        if entry.operation.manual && !self.endpoint.manual_routes {
            return Err(problem_body(
                &ApiError::not_implemented(format!(
                    "{} has no manual route handler",
                    entry.operation.name
                )),
                Some(self.request_id),
            ));
        }
        *request.extensions_mut() = self.incoming.extensions().clone();
        Ok(request)
    }
}

#[async_trait]
impl ToolSet for RouterTools<'_> {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Ok(self
            .endpoint
            .entries
            .iter()
            .filter(|entry| self.offered(entry))
            .map(|entry| entry.tool.listed())
            .collect())
    }

    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        let entry = self.entry(name)?;
        let request = match self.request_of(entry, arguments) {
            Ok(request) => request,
            Err(problem) => return Ok(CallOutcome::Problem(problem)),
        };
        let path = request.uri().path().to_owned();
        let response = handle(&self.endpoint.routes, request).await;
        Ok(outcome_of(&response, self.request_id, &path))
    }

    async fn confirms(&self, name: &str) -> Result<Option<Tool>, ToolError> {
        Ok(self
            .endpoint
            .entries
            .iter()
            .find(|entry| entry.tool.tool.name == name)
            .filter(|entry| entry.tool.confirm && self.offered(entry))
            .map(|entry| entry.tool.tool.clone()))
    }

    // The call's request through its route's check (D98): the same request
    // call makes, and every step of the route before the implementation but
    // its rate limit.
    async fn check(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<Option<Problem>, ToolError> {
        let entry = self.entry(name)?;
        let request = match self.request_of(entry, arguments) {
            Ok(request) => request,
            Err(problem) => return Ok(Some(problem)),
        };
        let Some(checks) = &self.endpoint.checks else {
            return Ok(None);
        };
        let response = handle(checks, request).await;
        Ok((response.status().as_u16() >= 400).then(|| problem_of(&response, self.request_id)))
    }
}

/// What a route's check answers a request nothing refused (D98): it would
/// reach the implementation. A generated router's check of a route runs
/// the route's controls but its rate limit, then its decoding, and answers
/// this in the implementation's place.
pub fn reached() -> Response {
    http::StatusCode::NO_CONTENT.into_response()
}
