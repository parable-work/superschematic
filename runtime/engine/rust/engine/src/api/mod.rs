//! The engine's HTTP API, transport-neutral (D15, D16, D58): the port of
//! `src/http/app.ts`, `src/http/callers.ts`, `src/http/problems.ts`,
//! `src/http/stream.ts` and `src/mcp/app.ts`. A host hands the engine one
//! [`Request`] (its method, the prefix the host mounts the engine at, the
//! path, the query, the headers, the body and the caller the host
//! authenticated) and sends back the [`Response`]: its status, headers and
//! body bytes, or an [`EventStream`] the host copies to the client.
//!
//! The engine owns routing over the 26 routes and the MCP endpoint
//! ([`routes`]), the decoding of their parameters and bodies, the
//! media-type refusals, ETags and `If-Match`, the `Preconditions` header,
//! the success envelope and the RFC 9457 problems, `Location` (from the
//! request's path, prefix and all), 404 for a path under the prefix that
//! names no route, the service-caller rule of D37 (a verified service with
//! no end user stands in for one) and 401 when there is no caller, the
//! event stream's every byte, and MCP's JSON-RPC. The host owns
//! authentication (D50, D37), request ids, body and rate limits, timeouts
//! and CORS: a request it refuses never reaches the engine, or reaches it
//! with the refusal ([`Caller::refused`]) so the engine answers it in its
//! place on a route it serves.

// A refusal is a problem document every handler returns as its error; it
// is the cold path, and boxing it would only add an allocation there.
#![allow(clippy::result_large_err)]

mod handlers;
pub(crate) mod mcp;
pub(crate) mod params;
mod problems;
pub(crate) mod stream;

use std::sync::Arc;

use serde::Serialize;
use serde_json::Value;

use crate::engine::Engine;
use crate::principal::{Principal, PrincipalService};
use crate::tools::ToolFilter;

pub use mcp::{McpOptions, MCP_DISCOVER_VERSIONS, MCP_INITIALIZE_VERSIONS, MCP_SERVER_NAME};
pub use problems::{status_text, Problem};
pub use stream::{
    start as start_stream, EventStream, Pull, StreamOptions, DEFAULT_HEARTBEAT_MS,
    DEFAULT_STREAM_PAGE_SIZE,
};

/// The service the host's service step verified (D37): the calling
/// deployable, the API services it serves, and its credential's subject.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ServiceCaller {
    pub deployable: String,
    pub serves: Vec<String>,
    pub subject: String,
}

/// A refusal the host's authentication made, which the engine answers on
/// a route it serves: its status, its code (`service_unauthorized`,
/// `service_forbidden`, `unauthorized`) and, optionally, its detail.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Refusal {
    pub status: u16,
    pub code: String,
    pub detail: Option<String>,
}

/// Who the host established for a request.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Caller {
    /// What the deployment's authenticator returned for the end user;
    /// `None` when it returned none, or there is no end-user authenticator.
    /// A principal with an empty subject is no caller (401).
    pub principal: Option<Principal>,
    /// The service the host's service step verified; `None` for none.
    pub service: Option<ServiceCaller>,
    /// The host's own refusal, answered in its place.
    pub refused: Option<Refusal>,
}

/// One request a host hands the engine.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    /// `GET`, `POST`, ...
    pub method: String,
    /// The path the host mounts the engine at (`/api`, or empty): routes
    /// match the path after it.
    pub prefix: String,
    /// The path as the host received it, prefix and all, still
    /// percent-encoded.
    pub path: String,
    /// The query string without `?`; empty for none.
    pub query: String,
    /// The request's headers, names in any case.
    pub headers: Vec<(String, String)>,
    /// The body's bytes; `None` for none.
    pub body: Option<Vec<u8>>,
    pub caller: Caller,
    /// The id the host gave the request, which every response carries.
    pub request_id: String,
}

impl Request {
    /// A header's value, by its name in any case.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// What a response's body is: bytes, or a stream of server-sent events
/// the host pulls ([`EventStream::next`]).
pub enum Body {
    Empty,
    Bytes(Vec<u8>),
    Stream(Box<EventStream>),
}

impl std::fmt::Debug for Body {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Body::Empty => f.write_str("Empty"),
            Body::Bytes(bytes) => write!(f, "Bytes({} bytes)", bytes.len()),
            Body::Stream(_) => f.write_str("Stream"),
        }
    }
}

/// One response the engine hands back.
#[derive(Debug)]
pub struct Response {
    pub status: u16,
    /// Lowercase names.
    pub headers: Vec<(String, String)>,
    pub body: Body,
}

impl Response {
    /// A header's value, by its lowercase name.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }

    /// The body's bytes; empty for none or a stream.
    pub fn bytes(&self) -> &[u8] {
        match &self.body {
            Body::Bytes(bytes) => bytes,
            _ => &[],
        }
    }
}

/// How the engine serves its routes: the event stream's page size and
/// heartbeat, the MCP endpoint's server info and instructions, and the
/// mount's tool filter, which narrows the tools document and MCP's tools
/// per caller.
#[derive(Clone, Default)]
pub struct ApiOptions {
    pub stream: StreamOptions,
    pub mcp: McpOptions,
    pub tools: Option<Arc<dyn ToolFilter>>,
}

impl std::fmt::Debug for ApiOptions {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ApiOptions")
            .field("stream", &self.stream)
            .field("mcp", &self.mcp)
            .field("tools", &self.tools.is_some())
            .finish()
    }
}

/// One route the engine serves, as [`routes`] lists it for a host's
/// dispatch, CORS and timeout exemptions.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Route {
    /// The operation's name (`createInstance`), as the TypeScript app
    /// names it.
    pub name: &'static str,
    pub method: &'static str,
    /// The path after the prefix, its parameters in braces.
    pub path: &'static str,
    /// Whether its response may be a stream (the event log with `Accept:
    /// text/event-stream`), which a host's timeout must not cut.
    pub stream: bool,
    /// Whether it is the MCP endpoint.
    pub mcp: bool,
}

impl Route {
    /// Whether its spec declares a request body: the seven routes a host's
    /// body limit applies to ([`DEFAULT_BODY_LIMIT_BYTES`] unless the mount
    /// sets one), as the HTTP runtimes apply theirs to a spec route. The
    /// event stream and the MCP endpoint, which the TypeScript app mounts
    /// by hand, are not among them; MCP caps its own body.
    pub fn takes_body(&self) -> bool {
        matches!(
            self.name,
            "createNamespace"
                | "defineSchema"
                | "createInstance"
                | "updateInstance"
                | "invokeOperation"
                | "invokeSchemaOperation"
                | "searchSchemas"
        )
    }
}

/// The body limit of a route that takes a body ([`Route::takes_body`])
/// unless the host sets one: the HTTP runtimes' `DEFAULT_BODY_LIMIT_BYTES`,
/// 1 MiB. A larger body is the host's 413 `payload_too_large`.
pub const DEFAULT_BODY_LIMIT_BYTES: usize = 1024 * 1024;

const fn route(name: &'static str, method: &'static str, path: &'static str) -> Route {
    Route {
        name,
        method,
        path,
        stream: false,
        mcp: false,
    }
}

/// Every route the engine serves, in the order the TypeScript app mounts
/// them, then the MCP endpoint's.
pub const ROUTES: [Route; 29] = [
    route("listNamespaces", "GET", "/namespaces"),
    route("createNamespace", "POST", "/namespaces"),
    route("getNamespace", "GET", "/namespaces/{namespace}"),
    route(
        "archiveNamespace",
        "POST",
        "/namespaces/{namespace}/archive",
    ),
    route(
        "unarchiveNamespace",
        "POST",
        "/namespaces/{namespace}/unarchive",
    ),
    route("listSchemas", "GET", "/namespaces/{namespace}/schemas"),
    route("defineSchema", "POST", "/namespaces/{namespace}/schemas"),
    route("getSchema", "GET", "/namespaces/{namespace}/schemas/{name}"),
    route(
        "getSchemaDraft",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/draft",
    ),
    route(
        "getSchemaVersion",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/versions/{version}",
    ),
    route(
        "describeSchema",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/describe",
    ),
    route(
        "publishSchema",
        "POST",
        "/namespaces/{namespace}/schemas/{name}/publish",
    ),
    route(
        "listInstances",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/instances",
    ),
    route(
        "lookupInstance",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/lookup",
    ),
    route(
        "createInstance",
        "POST",
        "/namespaces/{namespace}/schemas/{name}/instances",
    ),
    route(
        "getInstance",
        "GET",
        "/namespaces/{namespace}/schemas/{name}/instances/{id}",
    ),
    route(
        "updateInstance",
        "PATCH",
        "/namespaces/{namespace}/schemas/{name}/instances/{id}",
    ),
    route(
        "deleteInstance",
        "DELETE",
        "/namespaces/{namespace}/schemas/{name}/instances/{id}",
    ),
    route(
        "invokeOperation",
        "POST",
        "/namespaces/{namespace}/schemas/{name}/instances/{id}/operations/{operation}",
    ),
    route(
        "invokeSchemaOperation",
        "POST",
        "/namespaces/{namespace}/schemas/{name}/operations/{operation}",
    ),
    route("listTools", "GET", "/namespaces/{namespace}/tools"),
    route("listBehaviors", "GET", "/behaviors"),
    route("describeBehavior", "GET", "/behaviors/{name}"),
    route("searchSchemas", "POST", "/namespaces/{namespace}/search"),
    route("getValue", "GET", "/namespaces/{namespace}/values/{hash}"),
    Route {
        stream: true,
        ..route("readEvents", "GET", "/namespaces/{namespace}/events")
    },
    Route {
        mcp: true,
        ..route("mcpPost", "POST", "/namespaces/{namespace}/mcp")
    },
    Route {
        mcp: true,
        ..route("mcpGet", "GET", "/namespaces/{namespace}/mcp")
    },
    Route {
        mcp: true,
        ..route("mcpDelete", "DELETE", "/namespaces/{namespace}/mcp")
    },
];

/// Every route the engine serves (`routes()`): the 26 of the HTTP API,
/// then the MCP endpoint's POST, GET and DELETE.
pub fn routes() -> Vec<Route> {
    ROUTES.to_vec()
}

/// A route matched: the route, and its path parameters, decoded.
pub(crate) struct Matched {
    pub route: &'static Route,
    pub params: Vec<(String, String)>,
}

impl Matched {
    pub fn param(&self, name: &str) -> &str {
        self.params
            .iter()
            .find(|(key, _)| key == name)
            .map_or("", |(_, value)| value.as_str())
    }
}

// matching finds the route a method and a path (after the prefix) name,
// a parameter matching one non-empty segment, decoded once.
fn matching(method: &str, path: &str) -> Option<Matched> {
    let segments: Vec<&str> = path.split('/').skip(1).collect();
    for route in &ROUTES {
        if route.method != method {
            continue;
        }
        let pattern: Vec<&str> = route.path.split('/').skip(1).collect();
        if pattern.len() != segments.len() {
            continue;
        }
        let mut params = Vec::new();
        let matched = pattern.iter().zip(&segments).all(|(want, got)| {
            if let Some(name) = want
                .strip_prefix('{')
                .and_then(|rest| rest.strip_suffix('}'))
            {
                if got.is_empty() {
                    return false;
                }
                params.push((name.to_owned(), params::decode_segment(got)));
                true
            } else {
                want == got
            }
        });
        if matched {
            return Some(Matched { route, params });
        }
    }
    None
}

/// The route a method and a path (after the prefix) name, for a host's
/// dispatch: what [`handle`] would serve.
pub fn route_of(method: &str, path: &str) -> Option<Route> {
    matching(&method.to_uppercase(), path).map(|matched| matched.route.clone())
}

/// Serves one request: routes it, establishes its principal, decodes it and
/// answers with the envelope, a problem or a stream. A path outside the
/// prefix, or under it naming no route, is 404.
pub fn handle(engine: &Engine, options: &ApiOptions, request: &Request) -> Response {
    let request_id = request.request_id.as_str();
    if engine.is_closed() {
        // Shut down: the host is draining, and another instance answers.
        return Problem::new(503, "The engine is shutting down", "unavailable")
            .response(request_id);
    }
    let path = match request
        .path
        .strip_prefix(request.prefix.trim_end_matches('/'))
    {
        Some(rest) if rest.starts_with('/') || rest.is_empty() => rest,
        _ => return Problem::not_found("Not Found").response(request_id),
    };
    let Some(matched) = matching(&request.method.to_uppercase(), path) else {
        return Problem::not_found("Not Found").response(request_id);
    };
    if let Some(refused) = &request.caller.refused {
        let detail = refused
            .detail
            .clone()
            .unwrap_or_else(|| problems::default_detail(&refused.code).to_owned());
        return Problem::new(refused.status, detail, &refused.code).response(request_id);
    }
    let Some(principal) = principal_of(request) else {
        return Problem::unauthorized().response(request_id);
    };
    // A route that captured a path parameter refuses a path that does not
    // decode, so `%ZZ` and `%25ZZ` never reach it as one value.
    if !matched.params.is_empty() && params::strict_decode(&request.path).is_none() {
        return Problem::bad_request("The request path is not valid percent-encoding")
            .with_details(serde_json::json!({"location": "path"}))
            .response(request_id);
    }
    let served = if matched.route.mcp {
        mcp::serve(engine, options, request, &matched, &principal)
    } else {
        handlers::serve(engine, options, request, &matched, &principal)
    };
    match served {
        Ok(response) => response,
        Err(problem) => problem.response(request_id),
    }
}

/// The engine's principal for the callers the host established (D37,
/// `src/http/callers.ts`): the end user's, with the calling service beside
/// it; the service's own, standing in for one, when it brings no end user
/// and the request carries no `Authorization` header; `None` (401)
/// otherwise, and for a principal with no subject.
pub fn principal_of(request: &Request) -> Option<Principal> {
    let caller = &request.caller;
    match &caller.principal {
        Some(principal) => {
            if principal.subject.is_empty() {
                return None;
            }
            let mut principal = principal.clone();
            principal.service = caller.service.as_ref().map(|service| PrincipalService {
                deployable: service.deployable.clone(),
                serves: service.serves.clone(),
                subject: service.subject.clone(),
                stands_in: false,
            });
            Some(principal)
        }
        None => {
            let service = caller.service.as_ref()?;
            if !request
                .header("authorization")
                .unwrap_or_default()
                .trim()
                .is_empty()
            {
                return None;
            }
            Some(Principal::service(
                &service.deployable,
                &service.serves,
                &service.subject,
            ))
        }
    }
}

/// The JSON a JSON body holds: `None` for an empty body, a problem for one
/// that is not JSON.
pub(crate) fn json_body(request: &Request) -> Result<Option<Value>, Problem> {
    let Some(bytes) = &request.body else {
        return Ok(None);
    };
    if bytes.is_empty() {
        return Ok(None);
    }
    let text = std::str::from_utf8(bytes)
        .map_err(|_| Problem::bad_request("Request body is not valid JSON"))?;
    if text.is_empty() {
        return Ok(None);
    }
    serde_json::from_str(text)
        .map(Some)
        .map_err(|_| Problem::bad_request("Request body is not valid JSON"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_path_matches_its_route_with_its_parameters_decoded() {
        let found = matching("GET", "/namespaces/default/schemas/Order/instances/o%3A1").unwrap();
        assert_eq!(found.route.name, "getInstance");
        assert_eq!(found.param("id"), "o:1");
        assert!(matching("PUT", "/namespaces/default/schemas/Order/instances/o1").is_none());
        assert!(matching("GET", "/namespaces/default/nothing").is_none());
        assert_eq!(
            matching(
                "POST",
                "/namespaces/default/schemas/Item/operations/holders"
            )
            .unwrap()
            .route
            .name,
            "invokeSchemaOperation"
        );
        assert_eq!(routes().len(), 29);
        assert_eq!(routes().iter().filter(|route| !route.mcp).count(), 26);
        let bodies: Vec<&str> = routes()
            .iter()
            .filter(|route| route.takes_body())
            .map(|route| route.name)
            .collect();
        assert_eq!(bodies.len(), 7, "{bodies:?}");
        assert!(routes()
            .iter()
            .filter(|route| route.takes_body())
            .all(|route| route.method == "POST" || route.method == "PATCH"));
    }
}
