//! The transport: one request to the MCP endpoint answered as the official
//! SDK's stateless streamable HTTP transport answers it, the port of the
//! TypeScript runtime's `serveMcp` and of the engine's `serve` (D81, D116).
//! Where the two differ this follows the TypeScript runtime, the twin it
//! shares its tools' shape and its confirmations with (D93, D98).

use super::confirm::{
    ConfirmationBinding, ConfirmationMode, ConfirmationRefusal, CONFIRMATION_ARGUMENT,
    CONFIRMATION_DECLINED, CONFIRMATION_INPUT, CONFIRMATION_REQUIRED,
};
use super::json::canonical_object;
use super::{
    CallOutcome, Content, Options, Problem, Tool, ToolError, ToolSet, MCP_DISCOVER_VERSIONS,
    MCP_INITIALIZE_VERSIONS, MCP_MAX_BATCH, MCP_MAX_BODY_BYTES,
};
use crate::response::problem_body;
use crate::ApiError;
use bytes::Bytes;
use http::header::{ACCEPT, CACHE_CONTROL, CONTENT_LENGTH, CONTENT_TYPE};
use http::{HeaderMap, HeaderName, HeaderValue, Request, Response, StatusCode};
use serde_json::{json, Map, Value};
use std::fmt;

// The first revision that carries its version in each request's envelope;
// revisions are dates, so a later one compares greater.
const FIRST_MODERN: &str = "2026-07-28";

const VERSION_KEY: &str = "io.modelcontextprotocol/protocolVersion";
const CLIENT_INFO_KEY: &str = "io.modelcontextprotocol/clientInfo";
const CLIENT_CAPABILITIES_KEY: &str = "io.modelcontextprotocol/clientCapabilities";
const SERVER_INFO_KEY: &str = "io.modelcontextprotocol/serverInfo";

const VERSION_HEADER: HeaderName = HeaderName::from_static("mcp-protocol-version");
const METHOD_HEADER: HeaderName = HeaderName::from_static("mcp-method");

const PARSE_ERROR: i64 = -32700;
const INVALID_REQUEST: i64 = -32600;
const METHOD_NOT_FOUND: i64 = -32601;
const INVALID_PARAMS: i64 = -32602;
const INTERNAL_ERROR: i64 = -32603;
const SERVER_ERROR: i64 = -32000;
const HEADER_MISMATCH: i64 = -32020;
const UNSUPPORTED_VERSION: i64 = -32022;

// The longest arguments' text a confirmation's summary shows, in UTF-16
// code units, as the TypeScript runtime counts a string.
const SUMMARY_ARGUMENTS: usize = 2000;

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
        error.insert("code".to_owned(), Value::from(self.code));
        error.insert("message".to_owned(), Value::from(self.message.as_str()));
        if let Some(data) = &self.data {
            error.insert("data".to_owned(), data.clone());
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

    // failure is a handler's error that is no tool error: the SDK's
    // internal error, whose message stays off the wire and goes to the log.
    fn failure(method: &str, error: &dyn fmt::Display) -> Self {
        tracing::error!(method, error = %error, "an MCP request failed");
        RpcError::new(INTERNAL_ERROR, "Internal error")
    }

    // of_call is a tool set's error during tools/call: an unknown tool is
    // the invalid-params error, with the set's message.
    fn of_call(error: ToolError) -> Self {
        match error {
            ToolError::UnknownTool(message) => RpcError::new(INVALID_PARAMS, message),
            ToolError::Failed(error) => RpcError::failure("tools/call", &error),
        }
    }
}

// How a request is served: refused before any method runs, on the 2025
// revisions, or on a 2026 revision.
enum Era {
    Refused(StatusCode, RpcError),
    Legacy,
    Modern {
        revision: String,
        notification: bool,
    },
}

/// Serves one request to the MCP endpoint with `tools`, its caller already
/// authenticated: the JSON-RPC exchange as the official SDK's stateless
/// transport answers it. The request's body is read already, and the
/// answer's is bytes: a JSON body, the whole event stream of a 2025
/// request, or nothing. The future awaits `tools` and the confirmation
/// store of `options` and nothing else.
pub async fn serve(
    request: &Request<Bytes>,
    tools: &dyn ToolSet,
    options: &Options,
) -> Response<Bytes> {
    if !request.method().as_str().eq_ignore_ascii_case("POST") {
        return entry_error(
            StatusCode::METHOD_NOT_ALLOWED,
            &RpcError::new(SERVER_ERROR, "Method not allowed."),
            Value::Null,
        );
    }
    let headers = request.headers();
    if !is_json(header(headers, &CONTENT_TYPE).as_deref()) {
        return entry_error(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            &RpcError::new(
                SERVER_ERROR,
                "Unsupported Media Type: Content-Type must be application/json",
            ),
            Value::Null,
        );
    }
    let bytes = request.body();
    if declares_more_than(headers, MCP_MAX_BODY_BYTES) || bytes.len() > MCP_MAX_BODY_BYTES {
        return entry_error(
            StatusCode::PAYLOAD_TOO_LARGE,
            &RpcError::new(
                SERVER_ERROR,
                format!(
                    "Payload Too Large: Request body must not exceed {MCP_MAX_BODY_BYTES} bytes"
                ),
            ),
            Value::Null,
        );
    }
    // The body as text, a byte order mark aside, as a fetch Request reads
    // it.
    let text = String::from_utf8_lossy(bytes);
    let text = text.strip_prefix('\u{feff}').unwrap_or(&text);
    let parsed = (!text.is_empty())
        .then(|| serde_json::from_str::<Value>(text).ok())
        .flatten();
    let Some(body) = parsed else {
        // A body that is not JSON goes the 2025 way, whose transport
        // refuses it.
        if let Some(refused) = refuse_accept(headers) {
            return refused;
        }
        return entry_error(
            StatusCode::BAD_REQUEST,
            &RpcError::new(PARSE_ERROR, "Parse error: Invalid JSON"),
            Value::Null,
        );
    };
    let trimmed = |name: &HeaderName| header(headers, name).map(|value| value.trim().to_owned());
    let version_header = trimmed(&VERSION_HEADER);
    let method_header = trimmed(&METHOD_HEADER);
    let server = Server { tools, options };
    match classify(&body, version_header.as_deref(), method_header.as_deref()) {
        Era::Refused(status, error) => entry_error(status, &error, echoable_id(&body)),
        Era::Legacy => {
            server
                .legacy(headers, &body, version_header.as_deref())
                .await
        }
        Era::Modern {
            revision,
            notification,
        } => {
            server
                .modern(&body, &revision, notification, version_header.as_deref())
                .await
        }
    }
}

struct Server<'a> {
    tools: &'a dyn ToolSet,
    options: &'a Options,
}

// What the confirmation of a call came to: the arguments it runs with, the
// token's taken out, or what the call answers instead.
enum Gate {
    Run(Map<String, Value>),
    Answer(Value),
}

impl Server<'_> {
    // legacy serves the 2025 revisions as the SDK's stateless transport
    // does: every request's response as one event of a stream that ends
    // with the last.
    async fn legacy(
        &self,
        headers: &HeaderMap,
        body: &Value,
        version_header: Option<&str>,
    ) -> Response<Bytes> {
        if let Some(refused) = refuse_accept(headers) {
            return refused;
        }
        let messages: Vec<&Value> = match body {
            Value::Array(batch) => batch.iter().collect(),
            single => vec![single],
        };
        if messages.len() > MCP_MAX_BATCH {
            return entry_error(
                StatusCode::BAD_REQUEST,
                &RpcError::new(
                    INVALID_REQUEST,
                    format!("Invalid Request: Batch must not exceed {MCP_MAX_BATCH} messages"),
                ),
                Value::Null,
            );
        }
        if !messages.iter().all(|message| is_message(message)) {
            return entry_error(
                StatusCode::BAD_REQUEST,
                &RpcError::new(PARSE_ERROR, "Parse error: Invalid JSON-RPC message"),
                Value::Null,
            );
        }
        let initializes = messages
            .iter()
            .any(|message| is_request(message) && message["method"] == "initialize");
        if initializes && messages.len() > 1 {
            return entry_error(
                StatusCode::BAD_REQUEST,
                &RpcError::new(
                    INVALID_REQUEST,
                    "Invalid Request: Only one initialization request is allowed",
                ),
                Value::Null,
            );
        }
        if let Some(version) = version_header
            .filter(|version| !initializes && !MCP_INITIALIZE_VERSIONS.contains(version))
        {
            return entry_error(
                StatusCode::BAD_REQUEST,
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
        let mut frames = String::new();
        for message in messages.into_iter().filter(|message| is_request(message)) {
            let id = message["id"].clone();
            let reply = match self.dispatch(message, false).await {
                Ok(result) => result_message(result, id),
                Err(error) => error_message(&error, id),
            };
            frames.push_str("event: message\ndata: ");
            frames.push_str(&reply.to_string());
            frames.push_str("\n\n");
        }
        if frames.is_empty() {
            // Notifications and responses alone: nothing to answer.
            return accepted();
        }
        let mut response = Response::new(Bytes::from(frames));
        let headers = response.headers_mut();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("text/event-stream"));
        headers.insert(
            CACHE_CONTROL,
            HeaderValue::from_static("no-cache, no-transform"),
        );
        headers.insert(
            HeaderName::from_static("x-accel-buffering"),
            HeaderValue::from_static("no"),
        );
        response
    }

    // modern serves one 2026-07-28 message as one JSON body.
    async fn modern(
        &self,
        body: &Value,
        revision: &str,
        notification: bool,
        version_header: Option<&str>,
    ) -> Response<Bytes> {
        if !MCP_DISCOVER_VERSIONS.contains(&revision) {
            return entry_error(
                StatusCode::BAD_REQUEST,
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
                StatusCode::BAD_REQUEST,
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
                StatusCode::NOT_FOUND,
                &error_message(&RpcError::new(METHOD_NOT_FOUND, "Method not found"), id),
            );
        }
        let reply = match self.dispatch(body, true).await {
            Ok(Value::Object(mut result)) => {
                // A call that asks the person first is input_required
                // (D93); every other result is complete.
                if !result.get("resultType").is_some_and(Value::is_string) {
                    result.insert("resultType".to_owned(), Value::from("complete"));
                }
                if matches!(method, "server/discover" | "tools/list") {
                    // Cache hints: none, and for this caller alone.
                    result.insert("ttlMs".to_owned(), Value::from(0));
                    result.insert("cacheScope".to_owned(), Value::from("private"));
                }
                let mut meta = match result.remove("_meta") {
                    Some(Value::Object(meta)) => meta,
                    _ => Map::new(),
                };
                meta.insert(SERVER_INFO_KEY.to_owned(), self.server_info());
                result.insert("_meta".to_owned(), Value::Object(meta));
                result_message(Value::Object(result), id)
            }
            Ok(other) => result_message(other, id),
            Err(error) => error_message(&error, id),
        };
        json_response(StatusCode::OK, &reply)
    }

    // dispatch runs one request's method.
    async fn dispatch(&self, message: &Value, modern: bool) -> Result<Value, RpcError> {
        let method = message["method"].as_str().unwrap_or_default();
        let params = message.get("params").unwrap_or(&Value::Null);
        match method {
            "initialize" if !modern => self.initialize(params),
            "server/discover" if modern => Ok(self.discover()),
            "ping" => Ok(Value::Object(Map::new())),
            "tools/list" => self.list().await,
            "tools/call" => self.call(params, modern).await,
            _ => Err(RpcError::new(METHOD_NOT_FOUND, "Method not found")),
        }
    }

    fn server_info(&self) -> Value {
        let info = &self.options.server_info;
        json!({"name": info.name, "version": info.version})
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
        result.insert("protocolVersion".to_owned(), Value::from(version));
        result.insert("capabilities".to_owned(), capabilities());
        result.insert("serverInfo".to_owned(), self.server_info());
        self.instructions(&mut result);
        Ok(Value::Object(result))
    }

    fn discover(&self) -> Value {
        let mut result = Map::new();
        result.insert("supportedVersions".to_owned(), json!(MCP_DISCOVER_VERSIONS));
        result.insert("capabilities".to_owned(), capabilities());
        self.instructions(&mut result);
        Value::Object(result)
    }

    fn instructions(&self, result: &mut Map<String, Value>) {
        if let Some(instructions) = self
            .options
            .instructions
            .as_deref()
            .filter(|text| !text.is_empty())
        {
            result.insert("instructions".to_owned(), Value::from(instructions));
        }
    }

    async fn list(&self) -> Result<Value, RpcError> {
        let tools = self
            .tools
            .list()
            .await
            .map_err(|error| RpcError::failure("tools/list", &error))?;
        let tools =
            serde_json::to_value(tools).map_err(|error| RpcError::failure("tools/list", &error))?;
        Ok(json!({"tools": tools}))
    }

    // call runs a tool: its result as text and, for an object, structured
    // content; a refusal as a tool error carrying the problem document. A
    // tool that asks the person first runs only once they confirm the call.
    async fn call(&self, params: &Value, modern: bool) -> Result<Value, RpcError> {
        let Some(name) = params.get("name").and_then(Value::as_str) else {
            return Err(RpcError::new(
                INVALID_PARAMS,
                "Invalid params for tools/call: name must be a string",
            ));
        };
        let none = Map::new();
        let arguments = match params.get("arguments") {
            None | Some(Value::Null) => &none,
            Some(Value::Object(arguments)) => arguments,
            Some(_) => {
                return Err(RpcError::new(
                    INVALID_PARAMS,
                    "Invalid params for tools/call: arguments must be an object",
                ));
            }
        };
        let asks = self.tools.confirms(name).await.map_err(RpcError::of_call)?;
        let confirmed = match asks {
            Some(tool) => match self.confirm(&tool, arguments, params, modern).await? {
                Gate::Run(rest) => Some(rest),
                Gate::Answer(result) => return Ok(result),
            },
            None => None,
        };
        let outcome = self
            .tools
            .call(name, confirmed.as_ref().unwrap_or(arguments))
            .await
            .map_err(RpcError::of_call)?;
        Ok(match outcome {
            CallOutcome::Problem(problem) => tool_error(problem),
            CallOutcome::Content(items) => {
                let items: Vec<Value> = items.iter().map(Content::to_value).collect();
                json!({"content": items})
            }
            CallOutcome::Result(result) => {
                let mut out = Map::new();
                out.insert(
                    "content".to_owned(),
                    json!([{"type": "text", "text": result.to_string()}]),
                );
                if result.is_object() {
                    out.insert("structuredContent".to_owned(), result);
                }
                Value::Object(out)
            }
        })
    }

    // confirm is the confirmation of a call of tool, which asks the person
    // first (D93): the arguments it runs with, the token's taken out, once
    // the call carries a confirmation that redeems, else what the call
    // answers instead, the refusal its check met, a confirmation to ask for
    // or the person's refusal.
    async fn confirm(
        &self,
        tool: &Tool,
        arguments: &Map<String, Value>,
        params: &Value,
        modern: bool,
    ) -> Result<Gate, RpcError> {
        let token = arguments.get(CONFIRMATION_ARGUMENT);
        let rest: Map<String, Value> = arguments
            .iter()
            .filter(|(key, _)| key.as_str() != CONFIRMATION_ARGUMENT)
            .map(|(key, value)| (key.clone(), value.clone()))
            .collect();
        // A call that would not reach the implementation is refused before
        // anyone is asked, and mints and claims nothing (D98). It is
        // checked with the arguments the binding covers and the call runs
        // with.
        let refusal = self
            .tools
            .check(&tool.name, &rest)
            .await
            .map_err(RpcError::of_call)?;
        if let Some(refusal) = refusal {
            return Ok(Gate::Answer(tool_error(refusal)));
        }
        let Some(confirmations) = self.options.confirmations.as_deref() else {
            let problem = ApiError::internal(format!(
                "{} runs only once the person confirms it, and this endpoint has no confirmation key",
                tool.name
            ));
            return Ok(Gate::Answer(tool_error(self.problem(&problem))));
        };
        let binding = ConfirmationBinding {
            tool: &tool.name,
            caller: &self.options.caller,
            arguments: &rest,
        };
        let failed = |error| RpcError::failure("tools/call", &error);
        let elicits = modern && elicits_forms(client_capabilities(params));
        let mut refused = None;
        if let Some(token) = token {
            refused = confirmations
                .redeem(token, ConfirmationMode::Token, &binding)
                .await
                .map_err(failed)?;
            if refused.is_none() {
                return Ok(Gate::Run(rest));
            }
        } else if let Some(state) = params.get("requestState").filter(|_| elicits) {
            let action = params
                .get("inputResponses")
                .and_then(|responses| responses.get(CONFIRMATION_INPUT))
                .and_then(|answer| answer.get("action"))
                .and_then(Value::as_str);
            if let Some(action @ ("accept" | "decline" | "cancel")) = action {
                refused = confirmations
                    .redeem(state, ConfirmationMode::Elicit, &binding)
                    .await
                    .map_err(failed)?;
                if refused.is_none() && action == "accept" {
                    return Ok(Gate::Run(rest));
                }
                if refused.is_none() {
                    let what = if action == "decline" {
                        "declined"
                    } else {
                        "dismissed the confirmation of"
                    };
                    let problem = ApiError::new(
                        StatusCode::FORBIDDEN,
                        CONFIRMATION_DECLINED,
                        format!("The person {what} {}, which did not run", tool.name),
                    )
                    .with_details(json!({"tool": tool.name, "action": action}));
                    return Ok(Gate::Answer(tool_error(self.problem(&problem))));
                }
            }
        }
        let summary = confirmation_summary(tool, &rest);
        let why = refused.map_or_else(String::new, |refused| format!("{} ", refusal_text(refused)));
        if elicits {
            let minted = confirmations
                .mint(ConfirmationMode::Elicit, &binding)
                .map_err(failed)?;
            return Ok(Gate::Answer(json!({
                "resultType": "input_required",
                "inputRequests": {
                    CONFIRMATION_INPUT: {
                        "method": "elicitation/create",
                        "params": {
                            "mode": "form",
                            "message": format!("{why}Run {summary}?"),
                            "requestedSchema": {"type": "object", "properties": {}},
                        },
                    },
                },
                "requestState": minted.token,
            })));
        }
        let minted = confirmations
            .mint(ConfirmationMode::Token, &binding)
            .map_err(failed)?;
        let name = &tool.name;
        let detail = format!(
            "{why}{name} runs only once the person confirms it: {summary}. Show them the call, and once they confirm, \
             call {name} again with the same arguments and {CONFIRMATION_ARGUMENT} set to details.{CONFIRMATION_ARGUMENT}, before {}",
            minted.expires_at
        );
        let mut details = Map::new();
        details.insert("tool".to_owned(), Value::from(name.as_str()));
        details.insert("summary".to_owned(), Value::from(summary));
        details.insert(CONFIRMATION_ARGUMENT.to_owned(), Value::from(minted.token));
        details.insert("expiresAt".to_owned(), Value::from(minted.expires_at));
        if let Some(refused) = refused {
            details.insert("refused".to_owned(), Value::from(refused.as_str()));
        }
        let problem = ApiError::new(
            StatusCode::PRECONDITION_REQUIRED,
            CONFIRMATION_REQUIRED,
            detail,
        )
        .with_details(Value::Object(details));
        Ok(Gate::Answer(tool_error(self.problem(&problem))))
    }

    // problem is a refusal the endpoint itself makes, under the request's
    // id, or a fresh one when the options give none.
    fn problem(&self, error: &ApiError) -> Problem {
        let request_id = self
            .options
            .request_id
            .clone()
            .unwrap_or_else(|| uuid::Uuid::new_v4().to_string());
        problem_body(error, Some(&request_id))
    }
}

// refusal_text is what the person, or the agent, reads of a confirmation
// that did not redeem, before the call is asked about again.
fn refusal_text(refused: ConfirmationRefusal) -> &'static str {
    match refused {
        ConfirmationRefusal::Invalid => "The confirmation sent is not one this endpoint gave.",
        ConfirmationRefusal::Expired => "The confirmation sent has expired.",
        ConfirmationRefusal::Mismatch => "The confirmation sent is for another call.",
        ConfirmationRefusal::Used => "The confirmation sent was used already.",
    }
}

// confirmation_summary is what the person confirms: the tool's title and
// handle, and its arguments as JSON with sorted keys, the part past
// SUMMARY_ARGUMENTS characters counted rather than shown. The token binds
// every argument, whatever the summary shows.
fn confirmation_summary(tool: &Tool, arguments: &Map<String, Value>) -> String {
    let name = match tool.title.as_deref() {
        Some(title) if !title.is_empty() && title != tool.name => {
            format!("{title} ({})", tool.name)
        }
        _ => tool.name.clone(),
    };
    if arguments.is_empty() {
        return format!("{name} with no arguments");
    }
    let text = canonical_object(arguments);
    let length = text.encode_utf16().count();
    if length <= SUMMARY_ARGUMENTS {
        return format!("{name} with {text}");
    }
    // The cut falls on a character, so one past the basic plane that
    // straddles it is counted, not shown.
    let mut shown = 0;
    let mut end = 0;
    for (at, character) in text.char_indices() {
        if shown + character.len_utf16() > SUMMARY_ARGUMENTS {
            break;
        }
        shown += character.len_utf16();
        end = at + character.len_utf8();
    }
    format!(
        "{name} with {}\u{2026} ({} more characters)",
        &text[..end],
        length - shown
    )
}

// client_capabilities is the capabilities a 2026-07-28 request's envelope
// declares.
fn client_capabilities(params: &Value) -> Option<&Value> {
    params
        .get("_meta")?
        .as_object()?
        .get(CLIENT_CAPABILITIES_KEY)
}

// elicits_forms is whether capabilities let the server elicit a form: an
// elicitation capability that names form, or, as the 2025 revisions
// declared it, one that names neither form nor url.
fn elicits_forms(capabilities: Option<&Value>) -> bool {
    let Some(elicitation) = capabilities
        .and_then(|capabilities| capabilities.get("elicitation"))
        .and_then(Value::as_object)
    else {
        return false;
    };
    match elicitation.get("form") {
        Some(form) => form.is_object(),
        None => !elicitation.contains_key("url"),
    }
}

fn tool_error(problem: Problem) -> Value {
    json!({
        "content": [{"type": "text", "text": problem.to_string()}],
        "structuredContent": problem,
        "isError": true,
    })
}

fn capabilities() -> Value {
    json!({"tools": {"listChanged": false}})
}

// classify routes a parsed body as the SDK's entry does.
fn classify(body: &Value, version_header: Option<&str>, method_header: Option<&str>) -> Era {
    let refused = |error: RpcError| Era::Refused(StatusCode::BAD_REQUEST, error);
    let invalid = |message: &str| refused(RpcError::new(INVALID_REQUEST, message));
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
    let modern_header = version_header.filter(|version| *version >= FIRST_MODERN);
    if is_request(body) {
        if method == "initialize" && !valid_modern_claim(params) {
            if let Some(header) = modern_header {
                return refused(RpcError::mismatch(
                    header,
                    "an initialize request (legacy handshake) was sent with a modern MCP-Protocol-Version header".to_owned(),
                ));
            }
            return Era::Legacy;
        }
        if let Some(claim) = claim_of(params) {
            if let Some((key, problem)) = envelope_issues(params).into_iter().next() {
                return refused(
                    RpcError::new(
                        INVALID_PARAMS,
                        format!("Invalid _meta envelope for protocol revision 2026-07-28: {key}: {problem}"),
                    )
                    .with_data(json!({"envelope": {"key": key, "problem": problem}})),
                );
            }
            let claimed = claim.as_str().unwrap_or_default();
            if let Some(header) = version_header.filter(|header| *header != claimed) {
                return refused(RpcError::mismatch(
                    header,
                    format!(
                        "the body envelope names protocol version {claimed} but the MCP-Protocol-Version header names {header}"
                    ),
                ));
            }
            if let Some(refusal) = method_mismatch(method_header, method, "body") {
                return refused(refusal);
            }
            return Era::Modern {
                revision: claimed.to_owned(),
                notification: false,
            };
        }
        if let Some(header) = modern_header {
            let missing: Vec<&str> = match params.and_then(|params| params.get("_meta")) {
                None => vec!["_meta"],
                Some(_) => {
                    let absent: Vec<&str> = envelope_issues(params)
                        .into_iter()
                        .filter(|(_, problem)| *problem == "missing")
                        .map(|(key, _)| key)
                        .collect();
                    if absent.is_empty() {
                        vec![VERSION_KEY]
                    } else {
                        absent
                    }
                }
            };
            return refused(
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
            let Some(claimed) = claim.as_str() else {
                let problem = "expected a protocol version string";
                return refused(
                    RpcError::new(
                        INVALID_PARAMS,
                        format!("Invalid _meta envelope for protocol revision 2026-07-28: {VERSION_KEY}: {problem}"),
                    )
                    .with_data(json!({"envelope": {"key": VERSION_KEY, "problem": problem}})),
                );
            };
            if let Some(header) = version_header.filter(|header| *header != claimed) {
                return refused(RpcError::mismatch(
                    header,
                    format!(
                        "the notification envelope names protocol version {claimed} but the MCP-Protocol-Version header names {header}"
                    ),
                ));
            }
            if claimed >= FIRST_MODERN {
                if let Some(refusal) = method_mismatch(method_header, method, "notification body") {
                    return refused(refusal);
                }
            }
            return Era::Modern {
                revision: claimed.to_owned(),
                notification: true,
            };
        }
        if let Some(header) = modern_header {
            if let Some(refusal) = method_mismatch(method_header, method, "notification body") {
                return refused(refusal);
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

fn method_mismatch(header: Option<&str>, method: &str, what: &str) -> Option<RpcError> {
    let header = header.filter(|header| *header != method)?;
    Some(RpcError::mismatch(
        header,
        format!("the {what} names method {method} but the Mcp-Method header names {header}"),
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
fn envelope_issues(params: Option<&Value>) -> Vec<(&'static str, &'static str)> {
    let none = Map::new();
    let meta = params
        .and_then(|params| params.get("_meta"))
        .and_then(Value::as_object)
        .unwrap_or(&none);
    let mut issues = Vec::new();
    for key in [VERSION_KEY, CLIENT_INFO_KEY, CLIENT_CAPABILITIES_KEY] {
        if !meta.contains_key(key) {
            issues.push((key, "missing"));
        }
    }
    if meta
        .get(VERSION_KEY)
        .is_some_and(|value| !value.is_string())
    {
        issues.push((VERSION_KEY, "expected string"));
    }
    if let Some(info) = meta.get(CLIENT_INFO_KEY) {
        let named = |field: &str| info.get(field).is_some_and(Value::is_string);
        if !info.is_object() || !named("name") || !named("version") {
            issues.push((
                CLIENT_INFO_KEY,
                "expected an object with a name and a version",
            ));
        }
    }
    if meta
        .get(CLIENT_CAPABILITIES_KEY)
        .is_some_and(|value| !value.is_object())
    {
        issues.push((CLIENT_CAPABILITIES_KEY, "expected object"));
    }
    issues
}

fn is_rpc(message: &Value) -> bool {
    message.is_object() && message.get("jsonrpc").and_then(Value::as_str) == Some("2.0")
}

// is_integer is JavaScript's `Number.isInteger`: a number with no
// fraction, however it was written.
fn is_integer(value: &Value) -> bool {
    value.is_i64()
        || value.is_u64()
        || value
            .as_f64()
            .is_some_and(|number| number.is_finite() && number.fract() == 0.0)
}

fn is_id(id: &Value) -> bool {
    id.is_string() || is_integer(id)
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
        error.is_object()
            && error.get("code").is_some_and(is_integer)
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

// header is a request header as a fetch Request gives it: its values
// joined by a comma and a space, or None when it is not there.
fn header(headers: &HeaderMap, name: &HeaderName) -> Option<String> {
    let mut values = headers
        .get_all(name)
        .iter()
        .map(|value| String::from_utf8_lossy(value.as_bytes()));
    let mut joined = values.next()?.into_owned();
    for value in values {
        joined.push_str(", ");
        joined.push_str(&value);
    }
    Some(joined)
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

// declares_more_than is whether the request's Content-Length says its body
// passes limit, whatever the body read holds.
fn declares_more_than(headers: &HeaderMap, limit: usize) -> bool {
    header(headers, &CONTENT_LENGTH)
        .and_then(|value| value.trim().parse::<f64>().ok())
        .is_some_and(|declared| declared.is_finite() && declared > limit as f64)
}

// refuse_accept is the transport's 406 for a client that does not take
// both JSON and an event stream.
fn refuse_accept(headers: &HeaderMap) -> Option<Response<Bytes>> {
    let accept = header(headers, &ACCEPT).unwrap_or_default();
    if accept.contains("application/json") && accept.contains("text/event-stream") {
        return None;
    }
    Some(entry_error(
        StatusCode::NOT_ACCEPTABLE,
        &RpcError::new(
            SERVER_ERROR,
            "Not Acceptable: Client must accept both application/json and text/event-stream",
        ),
        Value::Null,
    ))
}

fn result_message(result: Value, id: Value) -> Value {
    json!({"result": result, "jsonrpc": "2.0", "id": id})
}

fn error_message(error: &RpcError, id: Value) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "error": error.value()})
}

// entry_error is an error the entry or the transport answers before any
// method runs, as the SDK writes it.
fn entry_error(status: StatusCode, error: &RpcError, id: Value) -> Response<Bytes> {
    json_response(
        status,
        &json!({"jsonrpc": "2.0", "error": error.value(), "id": id}),
    )
}

fn json_response(status: StatusCode, value: &Value) -> Response<Bytes> {
    let mut response = Response::new(Bytes::from(value.to_string()));
    *response.status_mut() = status;
    response
        .headers_mut()
        .insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
    response
}

fn accepted() -> Response<Bytes> {
    let mut response = Response::new(Bytes::new());
    *response.status_mut() = StatusCode::ACCEPTED;
    response
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request(method: &str, params: Value) -> Value {
        json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
    }

    fn envelope() -> Value {
        json!({"_meta": {
            VERSION_KEY: "2026-07-28",
            CLIENT_INFO_KEY: {"name": "c", "version": "1"},
            CLIENT_CAPABILITIES_KEY: {},
        }})
    }

    fn refused_with(era: &Era, code: i64) -> bool {
        matches!(era, Era::Refused(StatusCode::BAD_REQUEST, error) if error.code == code)
    }

    #[test]
    fn a_body_without_an_envelope_is_a_2025_request_and_one_with_it_a_2026_request() {
        assert!(matches!(
            classify(&request("tools/list", json!({})), Some("2025-11-25"), None),
            Era::Legacy
        ));
        assert!(matches!(
            classify(&request("tools/list", envelope()), Some("2026-07-28"), None),
            Era::Modern {
                notification: false,
                ..
            }
        ));
        // A modern header without the envelope, and a header the envelope
        // contradicts, are refused.
        assert!(refused_with(
            &classify(&request("tools/list", json!({})), Some("2026-07-28"), None),
            INVALID_PARAMS
        ));
        assert!(refused_with(
            &classify(&request("tools/list", envelope()), Some("2026-08-01"), None),
            HEADER_MISMATCH
        ));
        assert!(refused_with(
            &classify(
                &request("tools/list", envelope()),
                Some("2026-07-28"),
                Some("tools/call")
            ),
            HEADER_MISMATCH
        ));
        assert!(refused_with(
            &classify(
                &request("initialize", json!({"protocolVersion": "2025-11-25"})),
                Some("2026-07-28"),
                None
            ),
            HEADER_MISMATCH
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
            classify(
                &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
                Some("2026-07-28"),
                None
            ),
            Era::Modern {
                notification: true,
                ..
            }
        ));
        assert!(refused_with(
            &classify(&json!([]), None, None),
            INVALID_REQUEST
        ));
        assert!(refused_with(
            &classify(&json!([request("tools/list", envelope())]), None, None),
            INVALID_REQUEST
        ));
        assert!(refused_with(
            &classify(&json!({"id": 1}), None, None),
            INVALID_REQUEST
        ));
    }

    #[test]
    fn an_envelope_names_each_key_it_misses() {
        let issues = envelope_issues(Some(&json!({"_meta": {VERSION_KEY: 1}})));
        let keys: Vec<&str> = issues.iter().map(|(key, _)| *key).collect();
        assert_eq!(
            keys,
            [CLIENT_INFO_KEY, CLIENT_CAPABILITIES_KEY, VERSION_KEY],
            "{issues:?}"
        );
    }

    #[test]
    fn an_id_is_a_string_or_a_whole_number() {
        for id in [json!("a"), json!(1), json!(-3), json!(2.0)] {
            assert!(is_id(&id), "{id}");
        }
        for id in [json!(1.5), json!(null), json!([1]), json!({})] {
            assert!(!is_id(&id), "{id}");
        }
    }

    #[test]
    fn a_form_is_elicited_by_a_capability_that_names_form_or_no_mode() {
        for (capabilities, elicits) in [
            (json!({"elicitation": {"form": {}}}), true),
            (json!({"elicitation": {}}), true),
            (json!({"elicitation": {"form": {}, "url": {}}}), true),
            (json!({"elicitation": {"url": {}}}), false),
            (json!({"elicitation": {"form": true}}), false),
            (json!({"sampling": {}}), false),
            (json!({}), false),
        ] {
            assert_eq!(
                elicits_forms(Some(&capabilities)),
                elicits,
                "{capabilities}"
            );
        }
        assert!(!elicits_forms(None));
    }

    #[test]
    fn a_summary_cuts_on_a_character() {
        let tool = Tool {
            name: "t".to_owned(),
            title: None,
            description: None,
            input_schema: json!({}),
            annotations: None,
            meta: None,
        };
        // {"ab":" is seven units, then 996 characters of two units each
        // come to 1999: the next would straddle the cut.
        let long = "\u{1f600}".repeat(1200);
        let arguments = json!({"ab": long});
        let arguments = arguments.as_object().cloned().unwrap_or_default();
        let summary = confirmation_summary(&tool, &arguments);
        let shown = 7 + 996 * 2;
        let total = 7 + 1200 * 2 + 2;
        assert!(summary.starts_with("t with {\"ab\":\"\u{1f600}"));
        assert_eq!(summary.chars().filter(|c| *c == '\u{1f600}').count(), 996);
        assert!(summary.ends_with(&format!(
            "\u{1f600}\u{2026} ({} more characters)",
            total - shown
        )));
    }
}
