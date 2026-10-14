//! The MCP endpoint (D16, D58), the port of `src/mcp/app.ts` and of what it
//! takes from the official TypeScript SDK (`@modelcontextprotocol/server`
//! 2.2.0, `createMcpHandler`): `/namespaces/{namespace}/mcp` speaks MCP
//! over the streamable HTTP transport, statelessly, one tool per operation
//! of every live schema the namespace reaches, the schema tools and the
//! namespace tools ([`crate::tools::Tools`]).
//!
//! The engine authenticates the endpoint as it does every route (401 with
//! no caller), resolves the namespace (the HTTP API's 404 problem for one
//! it does not have), and then answers the JSON-RPC exchange itself, hand
//! rolled, as the SDK answers it:
//!
//! - The 2025 revisions (a request with no per-request `_meta` envelope):
//!   `initialize` (negotiating one of [`MCP_INITIALIZE_VERSIONS`]), `ping`,
//!   `tools/list` and `tools/call`, alone or in a batch. A request answers
//!   200 with a `text/event-stream` body of one `message` event per
//!   request; notifications and posted responses alone answer 202. The
//!   engine answers every call before it responds, so the event stream is
//!   whole when it hands it back: [`Body::Bytes`], which the host sends as
//!   it is.
//! - 2026-07-28 (a request whose `params._meta` carries the
//!   `io.modelcontextprotocol/protocolVersion` claim): `server/discover`,
//!   `ping`, `tools/list` and `tools/call`, each answered as one JSON body,
//!   every result marked complete and stamped with the server's identity.
//! - GET and DELETE answer 405: a stateless server keeps no session and
//!   opens no stream.
//!
//! The caller the host established is the principal every call acts as, so
//! the access policy answers each one and the list holds the tools the
//! policy lets that caller see; the mount's tool filter
//! ([`super::ApiOptions::tools`]) narrows it further. A call the engine
//! refuses is a tool error (`isError`) whose structured content is the
//! problem document the HTTP API would answer with; a tool the namespace
//! does not have is a JSON-RPC invalid-params error, and a malformed
//! message the SDK's JSON-RPC error, with its status.
//!
//! Where it differs from the SDK: a 2026-07-28 request's `Mcp-Method`
//! header is checked against the body when it is there, and not required;
//! `Mcp-Name` is not read. The SDK's `Connection: keep-alive` is the
//! host's to set.

use serde_json::{json, Map, Value};

use super::problems::Problem;
use super::{ApiOptions, Body, Matched, Request, Response};
use crate::errors::{EngineErrorKind, Error};
use crate::json;
use crate::principal::Principal;
use crate::tools::ToolTarget;
use crate::Engine;

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

/// The name `initialize` and every 2026-07-28 result give the server unless
/// the deployment gives one; the version is this crate's, the TypeScript
/// package's.
pub const MCP_SERVER_NAME: &str = "@superschematic/engine";

// The first revision that carries its version in each request's envelope;
// revisions are dates, so a later one compares greater.
const FIRST_MODERN: &str = "2026-07-28";

// The SDK's limits: a body of at most 4 MiB, a batch of at most 100.
pub(crate) const MAX_BODY_BYTES: usize = 4 * 1024 * 1024;
const MAX_BATCH: usize = 100;

const VERSION_KEY: &str = "io.modelcontextprotocol/protocolVersion";
const CLIENT_INFO_KEY: &str = "io.modelcontextprotocol/clientInfo";
const CLIENT_CAPABILITIES_KEY: &str = "io.modelcontextprotocol/clientCapabilities";
const SERVER_INFO_KEY: &str = "io.modelcontextprotocol/serverInfo";

const PARSE_ERROR: i64 = -32700;
const INVALID_REQUEST: i64 = -32600;
const METHOD_NOT_FOUND: i64 = -32601;
const INVALID_PARAMS: i64 = -32602;
const INTERNAL_ERROR: i64 = -32603;
const SERVER_ERROR: i64 = -32000;
const HEADER_MISMATCH: i64 = -32020;
const UNSUPPORTED_VERSION: i64 = -32022;

/// The MCP endpoint's server info and instructions: the name and version
/// `initialize` reports ([`MCP_SERVER_NAME`] and this crate's version
/// unless given), and the instructions it gives the client.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct McpOptions {
    pub server_name: Option<String>,
    pub server_version: Option<String>,
    pub instructions: Option<String>,
}

// A JSON-RPC error: its code, message and data.
struct RpcError {
    code: i64,
    message: String,
    data: Option<Value>,
}

impl RpcError {
    fn new(code: i64, message: impl Into<String>) -> Self {
        RpcError {
            code,
            message: message.into(),
            data: None,
        }
    }

    fn with_data(mut self, data: Value) -> Self {
        self.data = Some(data);
        self
    }

    fn value(&self) -> Value {
        let mut error = Map::new();
        error.insert("code".into(), Value::from(self.code));
        error.insert("message".into(), Value::from(self.message.as_str()));
        if let Some(data) = &self.data {
            error.insert("data".into(), data.clone());
        }
        Value::Object(error)
    }

    // mismatch is the SDK's refusal of headers and a body that disagree.
    fn mismatch(header: &str, body: String) -> Self {
        RpcError::new(
            HEADER_MISMATCH,
            format!("Bad Request: the request headers and body disagree: {body}"),
        )
        .with_data(json!({"mismatch": {"header": header, "body": body}}))
    }
}

// How a request is served: refused before any method runs, on the 2025
// revisions, or on a 2026 revision.
enum Era {
    Refused(u16, RpcError),
    Legacy,
    Modern {
        revision: String,
        notification: bool,
    },
}

/// Serves one request to the MCP endpoint, its caller authenticated.
pub(crate) fn serve(
    engine: &Engine,
    options: &ApiOptions,
    request: &Request,
    matched: &Matched,
    principal: &Principal,
) -> Result<Response, Problem> {
    // An unknown namespace is the HTTP API's 404 problem, before any MCP
    // exchange.
    let namespace = engine
        .namespaces()
        .resolve(Some(matched.param("namespace")))
        .map_err(|error| Problem::of(&error))?;
    let server = Server {
        engine,
        options,
        principal,
        namespace: &namespace,
        request_id: &request.request_id,
    };
    if !request.method.eq_ignore_ascii_case("POST") {
        return Ok(entry_error(
            405,
            &RpcError::new(SERVER_ERROR, "Method not allowed."),
            Value::Null,
        ));
    }
    if !is_json(request.header("content-type")) {
        return Ok(entry_error(
            415,
            &RpcError::new(
                SERVER_ERROR,
                "Unsupported Media Type: Content-Type must be application/json",
            ),
            Value::Null,
        ));
    }
    let bytes = request.body.as_deref().unwrap_or_default();
    if bytes.len() > MAX_BODY_BYTES {
        return Ok(entry_error(
            413,
            &RpcError::new(
                SERVER_ERROR,
                format!("Payload Too Large: Request body must not exceed {MAX_BODY_BYTES} bytes"),
            ),
            Value::Null,
        ));
    }
    let text = String::from_utf8_lossy(bytes);
    let parsed = (!text.is_empty())
        .then(|| serde_json::from_str::<Value>(&text).ok())
        .flatten();
    let Some(body) = parsed else {
        // A body that is not JSON goes the 2025 way, whose transport
        // refuses it.
        if let Some(refused) = refuse_accept(request) {
            return Ok(refused);
        }
        return Ok(entry_error(
            400,
            &RpcError::new(PARSE_ERROR, "Parse error: Invalid JSON"),
            Value::Null,
        ));
    };
    let header = |name: &str| request.header(name).map(|value| value.trim().to_owned());
    let version_header = header("mcp-protocol-version");
    let method_header = header("mcp-method");
    match classify(&body, version_header.as_deref(), method_header.as_deref()) {
        Era::Refused(status, error) => Ok(entry_error(status, &error, echoable_id(&body))),
        Era::Legacy => Ok(server.legacy(request, &body, version_header.as_deref())),
        Era::Modern {
            revision,
            notification,
        } => Ok(server.modern(&body, &revision, notification, version_header.as_deref())),
    }
}

struct Server<'a> {
    engine: &'a Engine,
    options: &'a ApiOptions,
    principal: &'a Principal,
    namespace: &'a str,
    request_id: &'a str,
}

impl Server<'_> {
    // legacy serves the 2025 revisions as the SDK's stateless transport
    // does: every request's response as one event of a stream that ends
    // with the last.
    fn legacy(&self, request: &Request, body: &Value, version_header: Option<&str>) -> Response {
        if let Some(refused) = refuse_accept(request) {
            return refused;
        }
        let messages: Vec<&Value> = match body {
            Value::Array(batch) => batch.iter().collect(),
            single => vec![single],
        };
        if messages.len() > MAX_BATCH {
            return entry_error(
                400,
                &RpcError::new(
                    INVALID_REQUEST,
                    format!("Invalid Request: Batch must not exceed {MAX_BATCH} messages"),
                ),
                Value::Null,
            );
        }
        if !messages.iter().all(|message| is_message(message)) {
            return entry_error(
                400,
                &RpcError::new(PARSE_ERROR, "Parse error: Invalid JSON-RPC message"),
                Value::Null,
            );
        }
        let initializes = messages
            .iter()
            .any(|message| is_request(message) && message["method"] == "initialize");
        if initializes && messages.len() > 1 {
            return entry_error(
                400,
                &RpcError::new(
                    INVALID_REQUEST,
                    "Invalid Request: Only one initialization request is allowed",
                ),
                Value::Null,
            );
        }
        if !initializes {
            if let Some(version) = version_header {
                if !MCP_INITIALIZE_VERSIONS.contains(&version) {
                    return entry_error(
                        400,
                        &RpcError::new(
                            SERVER_ERROR,
                            format!(
                                "Bad Request: Unsupported protocol version: {version} (supported versions: {})",
                                MCP_INITIALIZE_VERSIONS.join(", ")
                            ),
                        ),
                        Value::Null,
                    );
                }
            }
        }
        let mut frames = String::new();
        for message in messages.into_iter().filter(|message| is_request(message)) {
            let id = message["id"].clone();
            let reply = match self.dispatch(message, false) {
                Ok(result) => result_message(result, id),
                Err(error) => error_message(&error, id),
            };
            frames.push_str("event: message\ndata: ");
            frames.push_str(&json::stringify(&reply));
            frames.push_str("\n\n");
        }
        if frames.is_empty() {
            // Notifications and responses alone: nothing to answer.
            return accepted();
        }
        Response {
            status: 200,
            headers: vec![
                ("content-type".to_owned(), "text/event-stream".to_owned()),
                (
                    "cache-control".to_owned(),
                    "no-cache, no-transform".to_owned(),
                ),
                ("x-accel-buffering".to_owned(), "no".to_owned()),
            ],
            body: Body::Bytes(frames.into_bytes()),
        }
    }

    // modern serves one 2026-07-28 message as one JSON body.
    fn modern(
        &self,
        body: &Value,
        revision: &str,
        notification: bool,
        version_header: Option<&str>,
    ) -> Response {
        if !MCP_DISCOVER_VERSIONS.contains(&revision) {
            return entry_error(
                400,
                &RpcError::new(
                    UNSUPPORTED_VERSION,
                    format!("Unsupported protocol version: {revision}"),
                )
                .with_data(json!({"supported": MCP_DISCOVER_VERSIONS, "requested": revision})),
                echoable_id(body),
            );
        }
        if notification {
            return accepted();
        }
        if version_header.is_none() {
            return entry_error(
                400,
                &RpcError::mismatch(
                    "(missing)",
                    format!(
                        "the body envelope names protocol version {revision} but the required MCP-Protocol-Version header is absent"
                    ),
                ),
                echoable_id(body),
            );
        }
        let id = body["id"].clone();
        let method = body["method"].as_str().unwrap_or_default();
        if !matches!(
            method,
            "server/discover" | "ping" | "tools/list" | "tools/call"
        ) {
            // A method the era does not define is refused before any
            // handler runs, with the ladder's status.
            return json_response(
                404,
                &error_message(&RpcError::new(METHOD_NOT_FOUND, "Method not found"), id),
            );
        }
        let reply = match self.dispatch(body, true) {
            Ok(Value::Object(mut result)) => {
                result.insert("resultType".into(), Value::from("complete"));
                if matches!(method, "server/discover" | "tools/list") {
                    // Cache hints: none, and for this caller alone.
                    result.insert("ttlMs".into(), Value::from(0));
                    result.insert("cacheScope".into(), Value::from("private"));
                }
                let mut meta = result
                    .remove("_meta")
                    .and_then(|meta| match meta {
                        Value::Object(meta) => Some(meta),
                        _ => None,
                    })
                    .unwrap_or_default();
                meta.insert(SERVER_INFO_KEY.into(), self.server_info());
                result.insert("_meta".into(), Value::Object(meta));
                result_message(Value::Object(result), id)
            }
            Ok(other) => result_message(other, id),
            Err(error) => error_message(&error, id),
        };
        json_response(200, &reply)
    }

    // dispatch runs one request's method.
    fn dispatch(&self, message: &Value, modern: bool) -> Result<Value, RpcError> {
        let method = message["method"].as_str().unwrap_or_default();
        let params = message.get("params").unwrap_or(&Value::Null);
        match method {
            "initialize" if !modern => self.initialize(params),
            "server/discover" if modern => Ok(self.discover()),
            "ping" => Ok(Value::Object(Map::new())),
            "tools/list" => self.list(),
            "tools/call" => self.call(params),
            _ => Err(RpcError::new(METHOD_NOT_FOUND, "Method not found")),
        }
    }

    fn server_info(&self) -> Value {
        let mcp = &self.options.mcp;
        json!({
            "name": mcp.server_name.as_deref().unwrap_or(MCP_SERVER_NAME),
            "version": mcp.server_version.as_deref().unwrap_or(env!("CARGO_PKG_VERSION")),
        })
    }

    fn capabilities() -> Value {
        json!({"tools": {"listChanged": false}})
    }

    fn initialize(&self, params: &Value) -> Result<Value, RpcError> {
        let Some(requested) = params.get("protocolVersion").and_then(Value::as_str) else {
            return Err(RpcError::new(
                INVALID_PARAMS,
                "Invalid params for initialize: protocolVersion must be a string",
            ));
        };
        let version = if MCP_INITIALIZE_VERSIONS.contains(&requested) {
            requested
        } else {
            MCP_INITIALIZE_VERSIONS[0]
        };
        let mut result = Map::new();
        result.insert("protocolVersion".into(), Value::from(version));
        result.insert("capabilities".into(), Self::capabilities());
        result.insert("serverInfo".into(), self.server_info());
        self.instructions(&mut result);
        Ok(Value::Object(result))
    }

    fn discover(&self) -> Value {
        let mut result = Map::new();
        result.insert("supportedVersions".into(), json!(MCP_DISCOVER_VERSIONS));
        result.insert("capabilities".into(), Self::capabilities());
        self.instructions(&mut result);
        Value::Object(result)
    }

    fn instructions(&self, result: &mut Map<String, Value>) {
        if let Some(instructions) = self
            .options
            .mcp
            .instructions
            .as_deref()
            .filter(|text| !text.is_empty())
        {
            result.insert("instructions".into(), Value::from(instructions));
        }
    }

    fn target(&self) -> ToolTarget<'_> {
        ToolTarget {
            namespace: Some(self.namespace),
            filter: self.options.tools.as_deref(),
        }
    }

    fn list(&self) -> Result<Value, RpcError> {
        let tools = self
            .engine
            .tools()
            .mcp_tools(self.principal, self.target())
            .map_err(|error| failure(&error))?;
        Ok(json!({"tools": tools}))
    }

    // call runs a tool: its result as text and, for an object, structured
    // content; a refusal as a tool error carrying the problem document.
    fn call(&self, params: &Value) -> Result<Value, RpcError> {
        let Some(name) = params.get("name").and_then(Value::as_str) else {
            return Err(RpcError::new(
                INVALID_PARAMS,
                "Invalid params for tools/call: name must be a string",
            ));
        };
        let args = params.get("arguments").unwrap_or(&Value::Null);
        if !(args.is_null() || args.is_object()) {
            return Err(RpcError::new(
                INVALID_PARAMS,
                "Invalid params for tools/call: arguments must be an object",
            ));
        }
        match self
            .engine
            .tools()
            .call(self.principal, name, args, self.target())
        {
            Ok(result) => {
                let mut out = Map::new();
                out.insert(
                    "content".into(),
                    json!([{"type": "text", "text": json::stringify(&result)}]),
                );
                if result.is_object() {
                    out.insert("structuredContent".into(), result);
                }
                Ok(Value::Object(out))
            }
            Err(Error::Engine(refusal))
                if matches!(refusal.kind(), EngineErrorKind::UnknownTool { .. }) =>
            {
                Err(RpcError::new(INVALID_PARAMS, refusal.message()))
            }
            Err(error) => {
                let body = Problem::of(&error).body(self.request_id);
                let mut out = Map::new();
                out.insert(
                    "content".into(),
                    json!([{"type": "text", "text": json::stringify(&body)}]),
                );
                out.insert("structuredContent".into(), body);
                out.insert("isError".into(), Value::Bool(true));
                Ok(Value::Object(out))
            }
        }
    }
}

// failure is a handler's error that is no tool error: a refusal's message,
// or the SDK's internal error for anything else, whose message stays off
// the wire.
fn failure(error: &Error) -> RpcError {
    match error {
        Error::Engine(refusal) => RpcError::new(INTERNAL_ERROR, refusal.message()),
        _ => RpcError::new(INTERNAL_ERROR, "Internal error"),
    }
}

// classify routes a parsed body as the SDK's entry does.
fn classify(body: &Value, version_header: Option<&str>, method_header: Option<&str>) -> Era {
    let invalid = |message: &str| Era::Refused(400, RpcError::new(INVALID_REQUEST, message));
    if let Value::Array(batch) = body {
        if batch.is_empty() {
            return invalid("Bad Request: empty JSON-RPC batch");
        }
        for element in batch {
            if claim_of(element.get("params")).is_some() {
                return invalid(
                    "Bad Request: JSON-RPC batches may not contain requests for protocol revision 2026-07-28 or later",
                );
            }
            if !is_message(element) {
                return invalid("Bad Request: JSON-RPC batch contains an invalid message");
            }
        }
        return Era::Legacy;
    }
    if is_response(body) {
        return Era::Legacy;
    }
    let params = body.get("params");
    let method = body["method"].as_str().unwrap_or_default();
    let header_modern = version_header.is_some_and(|version| version >= FIRST_MODERN);
    if is_request(body) {
        if method == "initialize" && !valid_modern_claim(params) {
            if let Some(header) = version_header.filter(|_| header_modern) {
                return Era::Refused(
                    400,
                    RpcError::mismatch(
                        header,
                        "an initialize request (legacy handshake) was sent with a modern MCP-Protocol-Version header".to_owned(),
                    ),
                );
            }
            return Era::Legacy;
        }
        if let Some(claim) = claim_of(params) {
            if let Some((key, problem)) = envelope_issues(params).into_iter().next() {
                return Era::Refused(
                    400,
                    RpcError::new(
                        INVALID_PARAMS,
                        format!("Invalid _meta envelope for protocol revision 2026-07-28: {key}: {problem}"),
                    )
                    .with_data(json!({"envelope": {"key": key, "problem": problem}})),
                );
            }
            let claimed = claim.as_str().unwrap_or_default().to_owned();
            if let Some(header) = version_header.filter(|header| *header != claimed) {
                return Era::Refused(
                    400,
                    RpcError::mismatch(
                        header,
                        format!(
                            "the body envelope names protocol version {claimed} but the MCP-Protocol-Version header names {header}"
                        ),
                    ),
                );
            }
            if let Some(refused) = method_mismatch(method_header, method, "body") {
                return refused;
            }
            return Era::Modern {
                revision: claimed,
                notification: false,
            };
        }
        if let Some(header) = version_header.filter(|_| header_modern) {
            let missing: Vec<String> = match params.and_then(|params| params.get("_meta")) {
                None => vec!["_meta".to_owned()],
                Some(_) => {
                    let absent: Vec<String> = envelope_issues(params)
                        .into_iter()
                        .filter(|(_, problem)| problem == "missing")
                        .map(|(key, _)| key)
                        .collect();
                    if absent.is_empty() {
                        vec![VERSION_KEY.to_owned()]
                    } else {
                        absent
                    }
                }
            };
            return Era::Refused(
                400,
                RpcError::new(
                    INVALID_PARAMS,
                    format!(
                        "Invalid params: the MCP-Protocol-Version header names protocol revision {header}, but the request is missing the required per-request envelope key(s): {}",
                        missing.join(", ")
                    ),
                )
                .with_data(json!({"envelope": {"missing": missing}})),
            );
        }
        return Era::Legacy;
    }
    if is_notification(body) {
        if let Some(claim) = claim_of(params) {
            let Some(claimed) = claim.as_str().map(str::to_owned) else {
                let problem = "expected a protocol version string";
                return Era::Refused(
                    400,
                    RpcError::new(
                        INVALID_PARAMS,
                        format!("Invalid _meta envelope for protocol revision 2026-07-28: {VERSION_KEY}: {problem}"),
                    )
                    .with_data(json!({"envelope": {"key": VERSION_KEY, "problem": problem}})),
                );
            };
            if let Some(header) = version_header.filter(|header| *header != claimed) {
                return Era::Refused(
                    400,
                    RpcError::mismatch(
                        header,
                        format!(
                            "the notification envelope names protocol version {claimed} but the MCP-Protocol-Version header names {header}"
                        ),
                    ),
                );
            }
            if claimed.as_str() >= FIRST_MODERN {
                if let Some(refused) = method_mismatch(method_header, method, "notification body") {
                    return refused;
                }
            }
            return Era::Modern {
                revision: claimed,
                notification: true,
            };
        }
        if let Some(header) = version_header.filter(|_| header_modern) {
            if let Some(refused) = method_mismatch(method_header, method, "notification body") {
                return refused;
            }
            return Era::Modern {
                revision: header.to_owned(),
                notification: true,
            };
        }
        return Era::Legacy;
    }
    invalid("Bad Request: the request body is not a valid JSON-RPC message")
}

fn method_mismatch(header: Option<&str>, method: &str, what: &str) -> Option<Era> {
    let header = header.filter(|header| *header != method)?;
    Some(Era::Refused(
        400,
        RpcError::mismatch(
            header,
            format!("the {what} names method {method} but the Mcp-Method header names {header}"),
        ),
    ))
}

// claim_of is the envelope's protocol-version claim, when the params carry
// one.
fn claim_of(params: Option<&Value>) -> Option<&Value> {
    params?.get("_meta")?.as_object()?.get(VERSION_KEY)
}

// valid_modern_claim is whether the params carry a well-formed envelope
// that names a 2026 revision, which makes even `initialize` a 2026 request.
fn valid_modern_claim(params: Option<&Value>) -> bool {
    claim_of(params)
        .and_then(Value::as_str)
        .is_some_and(|claimed| claimed >= FIRST_MODERN)
        && envelope_issues(params).is_empty()
}

// envelope_issues lists what is wrong with a 2026 request's envelope: each
// required key that is missing, then each that is malformed.
fn envelope_issues(params: Option<&Value>) -> Vec<(String, String)> {
    let empty = Map::new();
    let meta = params
        .and_then(|params| params.get("_meta"))
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let mut issues = Vec::new();
    for key in [VERSION_KEY, CLIENT_INFO_KEY, CLIENT_CAPABILITIES_KEY] {
        if !meta.contains_key(key) {
            issues.push((key.to_owned(), "missing".to_owned()));
        }
    }
    if meta
        .get(VERSION_KEY)
        .is_some_and(|value| !value.is_string())
    {
        issues.push((VERSION_KEY.to_owned(), "expected string".to_owned()));
    }
    if let Some(info) = meta.get(CLIENT_INFO_KEY) {
        let named = |field: &str| info.get(field).is_some_and(Value::is_string);
        if !info.is_object() || !named("name") || !named("version") {
            issues.push((
                CLIENT_INFO_KEY.to_owned(),
                "expected an object with a name and a version".to_owned(),
            ));
        }
    }
    if meta
        .get(CLIENT_CAPABILITIES_KEY)
        .is_some_and(|value| !value.is_object())
    {
        issues.push((
            CLIENT_CAPABILITIES_KEY.to_owned(),
            "expected object".to_owned(),
        ));
    }
    issues
}

fn is_rpc(message: &Value) -> bool {
    message.is_object() && message.get("jsonrpc") == Some(&Value::from("2.0"))
}

fn is_id(id: &Value) -> bool {
    id.is_string() || id.is_i64() || id.is_u64()
}

fn params_ok(message: &Value) -> bool {
    message.get("params").is_none_or(Value::is_object)
}

fn is_request(message: &Value) -> bool {
    is_rpc(message)
        && message.get("method").is_some_and(Value::is_string)
        && message.get("id").is_some_and(is_id)
        && params_ok(message)
}

fn is_notification(message: &Value) -> bool {
    is_rpc(message)
        && message.get("method").is_some_and(Value::is_string)
        && message.get("id").is_none()
        && params_ok(message)
}

fn is_response(message: &Value) -> bool {
    if !is_rpc(message) || message.get("method").is_some() {
        return false;
    }
    let result =
        message.get("id").is_some_and(is_id) && message.get("result").is_some_and(Value::is_object);
    let error = message.get("error").is_some_and(|error| {
        error.get("code").is_some_and(|code| code.is_i64())
            && error.get("message").is_some_and(Value::is_string)
    });
    result || error
}

fn is_message(message: &Value) -> bool {
    is_request(message) || is_notification(message) || is_response(message)
}

// echoable_id is the id an entry-built error answers with: a single
// request's, else null.
fn echoable_id(body: &Value) -> Value {
    match (body.get("method"), body.get("id")) {
        (Some(Value::String(_)), Some(id)) if is_id(id) => id.clone(),
        _ => Value::Null,
    }
}

// is_json is whether a Content-Type is JSON's, parameters aside.
fn is_json(content_type: Option<&str>) -> bool {
    content_type.is_some_and(|value| {
        value
            .split(';')
            .next()
            .unwrap_or_default()
            .trim()
            .eq_ignore_ascii_case("application/json")
    })
}

// refuse_accept is the transport's 406 for a client that does not take
// both JSON and an event stream.
fn refuse_accept(request: &Request) -> Option<Response> {
    let accept = request.header("accept").unwrap_or_default();
    if accept.contains("application/json") && accept.contains("text/event-stream") {
        return None;
    }
    Some(entry_error(
        406,
        &RpcError::new(
            SERVER_ERROR,
            "Not Acceptable: Client must accept both application/json and text/event-stream",
        ),
        Value::Null,
    ))
}

fn result_message(result: Value, id: Value) -> Value {
    let mut message = Map::new();
    message.insert("result".into(), result);
    message.insert("jsonrpc".into(), Value::from("2.0"));
    message.insert("id".into(), id);
    Value::Object(message)
}

fn error_message(error: &RpcError, id: Value) -> Value {
    let mut message = Map::new();
    message.insert("jsonrpc".into(), Value::from("2.0"));
    message.insert("id".into(), id);
    message.insert("error".into(), error.value());
    Value::Object(message)
}

// entry_error is an error the entry or the transport answers before any
// method runs, as the SDK writes it.
fn entry_error(status: u16, error: &RpcError, id: Value) -> Response {
    let mut message = Map::new();
    message.insert("jsonrpc".into(), Value::from("2.0"));
    message.insert("error".into(), error.value());
    message.insert("id".into(), id);
    json_response(status, &Value::Object(message))
}

fn json_response(status: u16, value: &Value) -> Response {
    Response {
        status,
        headers: vec![("content-type".to_owned(), "application/json".to_owned())],
        body: Body::Bytes(json::stringify(value).into_bytes()),
    }
}

fn accepted() -> Response {
    Response {
        status: 202,
        headers: Vec::new(),
        body: Body::Empty,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request(method: &str, params: Value) -> Value {
        json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
    }

    #[test]
    fn a_body_without_an_envelope_is_a_2025_request_and_one_with_it_a_2026_request() {
        assert!(matches!(
            classify(&request("tools/list", json!({})), Some("2025-11-25"), None),
            Era::Legacy
        ));
        let envelope = json!({"_meta": {
            VERSION_KEY: "2026-07-28",
            CLIENT_INFO_KEY: {"name": "c", "version": "1"},
            CLIENT_CAPABILITIES_KEY: {},
        }});
        assert!(matches!(
            classify(
                &request("tools/list", envelope.clone()),
                Some("2026-07-28"),
                None
            ),
            Era::Modern {
                notification: false,
                ..
            }
        ));
        // A modern header without the envelope, and a header the envelope
        // contradicts, are refused.
        assert!(matches!(
            classify(&request("tools/list", json!({})), Some("2026-07-28"), None),
            Era::Refused(
                400,
                RpcError {
                    code: INVALID_PARAMS,
                    ..
                }
            )
        ));
        assert!(matches!(
            classify(
                &request("tools/list", envelope.clone()),
                Some("2026-08-01"),
                None
            ),
            Era::Refused(
                400,
                RpcError {
                    code: HEADER_MISMATCH,
                    ..
                }
            )
        ));
        assert!(matches!(
            classify(
                &request("tools/list", envelope),
                Some("2026-07-28"),
                Some("tools/call")
            ),
            Era::Refused(
                400,
                RpcError {
                    code: HEADER_MISMATCH,
                    ..
                }
            )
        ));
        assert!(matches!(
            classify(
                &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
                None,
                None
            ),
            Era::Legacy
        ));
        assert!(matches!(
            classify(&json!([]), None, None),
            Era::Refused(
                400,
                RpcError {
                    code: INVALID_REQUEST,
                    ..
                }
            )
        ));
        assert!(matches!(
            classify(&json!({"id": 1}), None, None),
            Era::Refused(
                400,
                RpcError {
                    code: INVALID_REQUEST,
                    ..
                }
            )
        ));
    }

    #[test]
    fn an_envelope_names_each_key_it_misses() {
        let issues = envelope_issues(Some(&json!({"_meta": {VERSION_KEY: 1}})));
        let keys: Vec<&str> = issues.iter().map(|(key, _)| key.as_str()).collect();
        assert_eq!(
            keys,
            [CLIENT_INFO_KEY, CLIENT_CAPABILITIES_KEY, VERSION_KEY],
            "{issues:?}"
        );
    }
}
