//! The confirmation of a call of a tool whose invocation policy is ask
//! (D93), as the TypeScript runtime's `mcpconfirm.test.ts` holds it: the
//! tokens, which bind the tool, the caller and the exact arguments, expire,
//! redeem once and cannot be made without the key; and the endpoint, which
//! asks a 2026-07-28 client that elicits forms in band and gives every
//! other client a two-step token, and runs the call only once it is
//! confirmed; and asks no one about a call its route would refuse before
//! the implementation, nor spends a confirmation on it (D98).
//!
//! The tokens are the TypeScript runtime's: `testdata/mcp/
//! confirm_vectors.json` holds tokens it minted and the canonical JSON it
//! binds arguments as, which this runtime redeems and writes.
//!
//! The endpoint's tools are a router's, as a generated one makes them
//! (D116): a call is the request `tool_request` builds, answered by a
//! route, and read by `outcome_of`.

use async_trait::async_trait;
use bytes::Bytes;
use http::{Method, Request, Response, StatusCode};
use serde_json::{json, Map, Value};
use std::future::Future;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll, Waker};
use superschematic_http_runtime::mcp::{
    caller_of, canonical_json, check_route_tools, outcome_of, problem_of, serve, tool_request,
    CallOutcome, ConfirmationBinding, ConfirmationMode, ConfirmationRefusal, ConfirmationStore,
    Confirmations, ConfirmationsError, MemoryConfirmationStore, Options, Problem, RouteTool,
    ServerInfo, Tool, ToolError, ToolSet, CONFIRMATION_ARGUMENT,
};
use superschematic_http_runtime::{problem_body, ApiError, BoxError, PROBLEM_CONTENT_TYPE};

const KEY: &[u8] = b"kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeey";
const OTHER_KEY: &[u8] = b"kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeez";
const VECTORS: &str = "tests/testdata/mcp/confirm_vectors.json";

/// Polls a future to its end on this thread: neither `serve` nor a
/// confirmation waits on anything but its tools and its store, which here
/// are ready.
fn block_on<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(output) => output,
        Poll::Pending => panic!("the future waited on something that was not ready"),
    }
}

fn object(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

fn confirmations(key: &[u8], scope: &str) -> Confirmations {
    Confirmations::new(key)
        .expect("a key of 33 bytes")
        .with_scope(scope)
}

/// A call a confirmation is bound to.
struct Call {
    tool: &'static str,
    caller: &'static str,
    arguments: Map<String, Value>,
}

impl Call {
    fn binding(&self) -> ConfirmationBinding<'_> {
        ConfirmationBinding {
            tool: self.tool,
            caller: self.caller,
            arguments: &self.arguments,
        }
    }

    fn with(&self, arguments: Value) -> Call {
        Call {
            tool: self.tool,
            caller: self.caller,
            arguments: object(arguments),
        }
    }
}

fn ada() -> Call {
    Call {
        tool: "rename_item",
        caller: r#"["ada",null]"#,
        arguments: object(json!({"id": "i1", "newName": "Desk"})),
    }
}

fn redeem(
    confirmations: &Confirmations,
    token: &str,
    mode: ConfirmationMode,
    call: &Call,
) -> Option<ConfirmationRefusal> {
    block_on(confirmations.redeem(&Value::from(token), mode, &call.binding()))
        .expect("the in-process store answers")
}

fn mint(confirmations: &Confirmations, mode: ConfirmationMode, call: &Call) -> String {
    confirmations
        .mint(mode, &call.binding())
        .expect("random bytes")
        .token
}

fn is_base64url(text: &str, length: usize) -> bool {
    text.len() == length
        && text
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_')
}

#[test]
fn a_token_redeems_once_for_the_call_it_was_minted_for() {
    let confirmations = confirmations(KEY, "items-api");
    let minted = confirmations
        .mint(ConfirmationMode::Token, &ada().binding())
        .expect("random bytes");
    let parts: Vec<&str> = minted.token.split('.').collect();
    assert_eq!(parts.len(), 6, "{}", minted.token);
    assert_eq!(&parts[..2], ["v1", "token"]);
    assert!(parts[2].bytes().all(|byte| byte.is_ascii_digit()));
    assert!(is_base64url(parts[3], 22) && is_base64url(parts[4], 43) && is_base64url(parts[5], 43));
    let at = minted.expires_at.as_bytes();
    assert_eq!(at.len(), 20, "{}", minted.expires_at);
    assert_eq!((at[4], at[10], at[13], at[19]), (b'-', b'T', b':', b'Z'));
    // The arguments in another order are the same call.
    let reordered = ada().with(json!({"newName": "Desk", "id": "i1"}));
    let token = &minted.token;
    assert_eq!(
        redeem(&confirmations, token, ConfirmationMode::Token, &reordered),
        None
    );
    assert_eq!(
        redeem(&confirmations, token, ConfirmationMode::Token, &ada()),
        Some(ConfirmationRefusal::Used)
    );
}

#[test]
fn a_token_for_another_call_is_a_mismatch_and_stays_good_for_its_own() {
    let confirmations = confirmations(KEY, "items-api");
    let token = mint(&confirmations, ConfirmationMode::Token, &ada());
    let mismatch = Some(ConfirmationRefusal::Mismatch);
    let other_tool = Call {
        tool: "delete_item",
        ..ada()
    };
    let other_caller = Call {
        caller: r#"["root",null]"#,
        ..ada()
    };
    for call in [
        other_tool,
        ada().with(json!({"id": "i1", "newName": "Chair"})),
        ada().with(json!({"id": "i1", "newName": "Desk", "extra": null})),
        other_caller,
    ] {
        assert_eq!(
            redeem(&confirmations, &token, ConfirmationMode::Token, &call),
            mismatch
        );
    }
    assert_eq!(
        redeem(&confirmations, &token, ConfirmationMode::Elicit, &ada()),
        mismatch
    );
    let orders = self::confirmations(KEY, "orders-api");
    assert_eq!(
        redeem(&orders, &token, ConfirmationMode::Token, &ada()),
        mismatch
    );
    assert_eq!(
        redeem(&confirmations, &token, ConfirmationMode::Token, &ada()),
        None
    );
}

/// A clock a test moves, at 2026-10-26T12:00:00Z.
fn clock() -> (Arc<AtomicU64>, Arc<dyn Fn() -> u64 + Send + Sync>) {
    let now = Arc::new(AtomicU64::new(1_793_016_000_000));
    let read = Arc::clone(&now);
    (now, Arc::new(move || read.load(Ordering::SeqCst)))
}

#[test]
fn a_token_expires() {
    let (now, clock) = clock();
    let confirmations = Confirmations::new(KEY)
        .and_then(|confirmations| confirmations.with_ttl_seconds(60))
        .expect("a key and a time to live")
        .with_clock(clock);
    let minted = confirmations
        .mint(ConfirmationMode::Elicit, &ada().binding())
        .expect("random bytes");
    assert_eq!(minted.expires_at, "2026-10-26T12:01:00Z");
    now.fetch_add(59_999, Ordering::SeqCst);
    let early = mint(&confirmations, ConfirmationMode::Elicit, &ada());
    now.fetch_add(1, Ordering::SeqCst);
    assert_eq!(
        redeem(
            &confirmations,
            &minted.token,
            ConfirmationMode::Elicit,
            &ada()
        ),
        Some(ConfirmationRefusal::Expired)
    );
    assert_eq!(
        redeem(&confirmations, &early, ConfirmationMode::Elicit, &ada()),
        None
    );
}

#[test]
fn a_token_no_key_of_this_endpoint_minted_or_changed_in_any_part_is_invalid() {
    let confirmations = confirmations(KEY, "");
    let token = mint(&confirmations, ConfirmationMode::Token, &ada());
    let parts: Vec<&str> = token.split('.').collect();
    let flip = |text: &str| {
        format!(
            "{}{}",
            if text.starts_with('A') { 'B' } else { 'A' },
            &text[1..]
        )
    };
    let invalid = Some(ConfirmationRefusal::Invalid);
    for index in 1..6 {
        let mut changed: Vec<String> = parts.iter().map(|part| (*part).to_owned()).collect();
        changed[index] = match index {
            1 => "elicit".to_owned(),
            2 => (parts[2].parse::<u64>().expect("a Unix second") + 3600).to_string(),
            _ => flip(parts[index]),
        };
        let changed = changed.join(".");
        assert_eq!(
            redeem(&confirmations, &changed, ConfirmationMode::Token, &ada()),
            invalid,
            "part {index}"
        );
    }
    let other = self::confirmations(OTHER_KEY, "");
    assert_eq!(
        redeem(&other, &token, ConfirmationMode::Token, &ada()),
        invalid
    );
    for junk in [
        json!(null),
        json!(7),
        json!(""),
        json!("v1"),
        json!(format!("v2.{}", parts[1..].join("."))),
        json!(format!("{token}.more")),
        json!(token.replace(".token.", ".other.")),
        json!([token]),
    ] {
        let refused =
            block_on(confirmations.redeem(&junk, ConfirmationMode::Token, &ada().binding()))
                .expect("the in-process store answers");
        assert_eq!(refused, invalid, "{junk}");
    }
    // An invalid token claims nothing: the real one still redeems.
    assert_eq!(
        redeem(&confirmations, &token, ConfirmationMode::Token, &ada()),
        None
    );
}

#[test]
fn a_short_key_and_a_time_to_live_of_no_seconds_are_refused() {
    let short = Confirmations::new(b"short").expect_err("five bytes");
    assert_eq!(short, ConfirmationsError::ShortKey { bytes: 5 });
    assert_eq!(
        short.to_string(),
        "the confirmation key holds 5 bytes; it needs at least 32, random"
    );
    let ttl = Confirmations::new(KEY)
        .and_then(|confirmations| confirmations.with_ttl_seconds(0))
        .expect_err("no seconds");
    assert_eq!(ttl.to_string(), "ttlSeconds 0 is no positive whole number");
    assert!(Confirmations::new(&KEY[..32]).is_ok());
    // The key stays out of what a log prints.
    let printed = format!("{:?}", confirmations(KEY, "items-api"));
    assert!(
        printed.contains("items-api") && !printed.contains("kkkk"),
        "{printed}"
    );
}

#[test]
fn the_in_process_store_claims_an_id_once_until_it_expires_and_refuses_rather_than_forgets() {
    let store = MemoryConfirmationStore::with_max_claims(2);
    assert!(store.claim_now("a", 1000, 0));
    assert!(!store.claim_now("a", 1000, 999));
    assert!(store.claim_now("b", 1000, 0));
    assert!(!store.claim_now("c", 1000, 500));
    assert!(store.claim_now("c", 1000, 1000));
    assert!(store.claim_now("a", 1000, 1000));
    let claimed = block_on(store.claim("a", 1000, 1500)).expect("the store answers");
    assert!(!claimed);
}

fn vectors() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&text).unwrap_or_else(|err| panic!("parse {}: {err}", path.display()))
}

fn text_of(value: &Value) -> &str {
    value
        .as_str()
        .unwrap_or_else(|| panic!("{value} is no string"))
}

#[test]
fn canonical_json_is_the_typescript_runtimes() {
    let vectors = vectors();
    let canonical = vectors["canonical"].as_array().expect("canonical vectors");
    assert!(canonical.len() >= 8);
    for vector in canonical {
        let json = text_of(&vector["json"]);
        let value: Value = serde_json::from_str(json).expect("a vector's JSON");
        assert_eq!(
            canonical_json(&value),
            text_of(&vector["canonical"]),
            "{json}"
        );
    }
    assert_eq!(
        canonical_json(&json!({"b": [1, {"d": 2, "a": null}], "a": "x"})),
        r#"{"a":"x","b":[1,{"a":null,"d":2}]}"#
    );
}

#[test]
fn a_token_the_typescript_runtime_minted_redeems_here_once() {
    let vectors = vectors();
    let now = vectors["now"].as_u64().expect("the vectors' time");
    let ttl = vectors["ttlSeconds"]
        .as_u64()
        .expect("the vectors' time to live");
    let tokens = vectors["tokens"].as_array().expect("token vectors");
    assert!(tokens.len() >= 7);
    for vector in tokens {
        let token = text_of(&vector["token"]);
        let arguments: Value = serde_json::from_str(text_of(&vector["args"])).expect("arguments");
        let arguments = object(arguments);
        let binding = ConfirmationBinding {
            tool: text_of(&vector["tool"]),
            caller: text_of(&vector["caller"]),
            arguments: &arguments,
        };
        let (mode, other) = match text_of(&vector["mode"]) {
            "elicit" => (ConfirmationMode::Elicit, ConfirmationMode::Token),
            _ => (ConfirmationMode::Token, ConfirmationMode::Elicit),
        };
        let confirmations = Confirmations::new(text_of(&vectors["key"]).as_bytes())
            .and_then(|confirmations| confirmations.with_ttl_seconds(ttl))
            .expect("the vectors' key")
            .with_scope(text_of(&vector["scope"]))
            .with_clock(Arc::new(move || now));
        let redeem = |mode, binding: &ConfirmationBinding<'_>| {
            block_on(confirmations.redeem(&Value::from(token), mode, binding))
                .expect("the in-process store answers")
        };
        // The same clock gives the same expiry, and the same head but for
        // the nonce.
        let minted = confirmations.mint(mode, &binding).expect("random bytes");
        assert_eq!(minted.expires_at, text_of(&vector["expiresAt"]), "{token}");
        let fields = |token: &str| -> Vec<String> { token.split('.').map(str::to_owned).collect() };
        let (ours, theirs) = (fields(&minted.token), fields(token));
        assert_eq!(
            (&ours[..3], &ours[4]),
            (&theirs[..3], &theirs[4]),
            "the mode, the expiry and the binding of {token}"
        );
        // Another mode, another caller and other arguments do not redeem
        // it, nor spend it.
        let mismatch = Some(ConfirmationRefusal::Mismatch);
        assert_eq!(redeem(other, &binding), mismatch, "{token}");
        let mut more = arguments.clone();
        more.insert("another".to_owned(), Value::Null);
        for unbound in [
            ConfirmationBinding {
                caller: "someone else",
                ..binding
            },
            ConfirmationBinding {
                arguments: &more,
                ..binding
            },
        ] {
            assert_eq!(redeem(mode, &unbound), mismatch, "{token}");
        }
        assert_eq!(redeem(mode, &binding), None, "{token}");
        assert_eq!(
            redeem(mode, &binding),
            Some(ConfirmationRefusal::Used),
            "{token}"
        );
    }
}

// The endpoint of a router, as a generated one mounts it.

/// The router's tool table, as the build writes one.
fn route_tools() -> Vec<RouteTool> {
    let id_alone = json!({
        "type": "object",
        "additionalProperties": false,
        "properties": {"id": {"type": "string"}},
        "required": ["id"],
    });
    let id_argument = json!([{"key": "id", "in": "path", "name": "id"}]);
    serde_json::from_value(json!([
        {
            "name": "rename_item",
            "title": "Rename an item",
            "description": "Renames one item.",
            "inputSchema": {
                "type": "object",
                "additionalProperties": false,
                "properties": {"id": {"type": "string"}, "newName": {"type": "string"}},
                "required": ["id", "newName"],
            },
            "annotations": {"readOnlyHint": false},
            "_meta": {"invocationPolicy": "ask"},
            "operation": "renameItem",
            "arguments": [
                {"key": "id", "in": "path", "name": "id"},
                {"key": "newName", "in": "body", "name": "new_name"},
            ],
            "confirm": true,
        },
        {
            "name": "get_item",
            "title": "Get an item",
            "description": "",
            "inputSchema": id_alone,
            "annotations": {"readOnlyHint": true},
            "_meta": {"invocationPolicy": "auto"},
            "operation": "getItem",
            "arguments": id_argument,
        },
        {
            "name": "purge_item",
            "title": "Purge an item",
            "description": "",
            "inputSchema": id_alone,
            "annotations": {"readOnlyHint": false},
            "_meta": {"invocationPolicy": "ask"},
            "operation": "purgeItem",
            "arguments": id_argument,
            "confirm": true,
        },
        {
            "name": "archive_item",
            "title": "Archive an item",
            "description": "",
            "inputSchema": id_alone,
            "annotations": {"readOnlyHint": false},
            "_meta": {"invocationPolicy": "ask"},
            "operation": "archiveItem",
            "arguments": id_argument,
            "confirm": true,
        },
    ]))
    .expect("the tool table reads as the build writes it")
}

/// The method and path of an operation of the router.
fn route_of(operation: &str) -> (Method, &'static str) {
    match operation {
        "renameItem" => (Method::POST, "/api/items/{id}/rename"),
        "getItem" => (Method::GET, "/api/items/{id}"),
        "purgeItem" => (Method::DELETE, "/api/items/{id}"),
        "archiveItem" => (Method::POST, "/api/items/{id}/archive"),
        other => panic!("the router has no operation {other}"),
    }
}

fn bearer<B>(request: &Request<B>) -> Option<&str> {
    request
        .headers()
        .get("authorization")?
        .to_str()
        .ok()?
        .strip_prefix("Bearer ")
        .filter(|token| matches!(*token, "ada" | "root" | "reader"))
}

fn request_id<B>(request: &Request<B>) -> &str {
    request
        .headers()
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("none")
}

fn refusal(error: &ApiError, request_id: &str) -> Response<Bytes> {
    Response::builder()
        .status(error.status)
        .header("content-type", PROBLEM_CONTENT_TYPE)
        .body(Bytes::from(
            problem_body(error, Some(request_id)).to_string(),
        ))
        .expect("a problem response")
}

/// A router's routes, which count the renames they run.
#[derive(Default)]
struct Items {
    renames: Mutex<Vec<String>>,
}

impl Items {
    fn renames(&self) -> Vec<String> {
        self.renames.lock().expect("the renames").clone()
    }

    /// Answers a request to a route. With `run` unset it is the route's
    /// check (D98): every step before the implementation, and 204 where
    /// the route would run it.
    fn answer(&self, request: &Request<Bytes>, run: bool) -> Response<Bytes> {
        let id = request_id(request);
        match self.route(request, run) {
            Ok(Some(data)) => Response::builder()
                .header("content-type", "application/json")
                .body(Bytes::from(
                    json!({"data": data, "meta": {"requestId": id}}).to_string(),
                ))
                .expect("an envelope"),
            Ok(None) => Response::builder()
                .status(StatusCode::NO_CONTENT)
                .body(Bytes::new())
                .expect("no content"),
            Err(error) => refusal(&error, id),
        }
    }

    fn route(&self, request: &Request<Bytes>, run: bool) -> Result<Option<Value>, ApiError> {
        let segments: Vec<&str> = request.uri().path().split('/').skip(1).collect();
        let signed_in =
            || bearer(request).ok_or_else(|| ApiError::unauthorized("Authentication required"));
        match (request.method().as_str(), segments.as_slice()) {
            ("POST", ["api", "items", id, "rename"]) => {
                // A key that may read items and not rename them, as an API
                // key's scopes refuse one operation.
                if signed_in()? == "reader" {
                    return Err(ApiError::forbidden(
                        "This key may read items, not rename them",
                    ));
                }
                let body: Value = serde_json::from_slice(request.body())
                    .map_err(|_| ApiError::bad_request("Request body is not valid JSON"))?;
                let name = match body.as_object() {
                    Some(members) if members.len() == 1 => members
                        .get("new_name")
                        .and_then(Value::as_str)
                        .filter(|name| !name.is_empty()),
                    _ => None,
                };
                let Some(name) = name else {
                    return Err(ApiError::bad_request(
                        "Request body does not match the declared input",
                    ));
                };
                if !run {
                    return Ok(None);
                }
                self.renames
                    .lock()
                    .expect("the renames")
                    .push(name.to_owned());
                Ok(Some(json!({"id": id, "name": name})))
            }
            ("GET", ["api", "items", id]) => {
                signed_in()?;
                Ok(run.then(|| json!({"id": id})))
            }
            // Only a calling service may purge, and no request here brings
            // a service credential.
            ("DELETE", ["api", "items", _]) => {
                Err(ApiError::service_unauthorized("Invalid service credential"))
            }
            _ => Err(ApiError::not_found("Not Found")),
        }
    }
}

/// The tools one request to the endpoint is served, as its caller: each
/// call a request to its route.
struct RouterTools<'a> {
    items: &'a Items,
    tools: Vec<RouteTool>,
    incoming: &'a Request<Bytes>,
    request_id: &'a str,
}

impl RouterTools<'_> {
    /// The tool, when the caller is offered it: archive_item needs a
    /// permission no caller here holds.
    fn offered(&self, name: &str) -> Result<&RouteTool, ToolError> {
        self.tools
            .iter()
            .find(|tool| tool.tool.name == name && tool.operation != "archiveItem")
            .ok_or_else(|| {
                ToolError::unknown(format!("items-api has no tool {}", Value::from(name)))
            })
    }

    fn request(
        &self,
        tool: &RouteTool,
        arguments: &Map<String, Value>,
    ) -> Result<(Request<Bytes>, &'static str), Problem> {
        let (method, path) = route_of(&tool.operation);
        tool_request(
            tool,
            &method,
            path,
            arguments,
            self.incoming,
            self.request_id,
        )
        .map(|request| (request, path))
    }
}

#[async_trait]
impl ToolSet for RouterTools<'_> {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Ok(self
            .tools
            .iter()
            .filter(|tool| tool.operation != "archiveItem")
            .map(RouteTool::listed)
            .collect())
    }

    async fn confirms(&self, name: &str) -> Result<Option<Tool>, ToolError> {
        Ok(self
            .offered(name)
            .ok()
            .filter(|tool| tool.confirm)
            .map(|tool| tool.tool.clone()))
    }

    async fn check(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<Option<Problem>, ToolError> {
        let tool = self.offered(name)?;
        Ok(match self.request(tool, arguments) {
            Err(problem) => Some(problem),
            Ok((request, _)) => {
                let checked = self.items.answer(&request, false);
                (checked.status().as_u16() >= 400).then(|| problem_of(&checked, self.request_id))
            }
        })
    }

    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        let tool = self.offered(name)?;
        Ok(match self.request(tool, arguments) {
            Err(problem) => CallOutcome::Problem(problem),
            Ok((request, path)) => {
                outcome_of(&self.items.answer(&request, true), self.request_id, path)
            }
        })
    }
}

/// A router's MCP endpoint over its routes.
struct Endpoint {
    items: Items,
    confirmations: Arc<Confirmations>,
}

static IDS: AtomicU64 = AtomicU64::new(0);

fn next_id() -> u64 {
    IDS.fetch_add(1, Ordering::SeqCst) + 1
}

/// The envelope of a 2026-07-28 request from a client with capabilities.
fn envelope(capabilities: &Value) -> Value {
    json!({
        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
        "io.modelcontextprotocol/clientInfo": {"name": "agent", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": capabilities,
    })
}

fn elicits() -> Option<Value> {
    Some(json!({"elicitation": {"form": {}}}))
}

/// The JSON-RPC message of an answer: an event stream's one, or a JSON
/// body.
fn message(response: &Response<Bytes>) -> Value {
    assert_eq!(response.status(), StatusCode::OK);
    let text = String::from_utf8_lossy(response.body());
    let text = text.strip_prefix("event: message\ndata: ").unwrap_or(&text);
    serde_json::from_str(text.trim_end()).unwrap_or_else(|err| panic!("{text}: {err}"))
}

impl Endpoint {
    fn new() -> Self {
        Self::with(confirmations(KEY, "items-api"))
    }

    fn with(confirmations: Confirmations) -> Self {
        check_route_tools(&route_tools()).expect("the tool table is sound");
        Self {
            items: Items::default(),
            confirmations: Arc::new(confirmations),
        }
    }

    /// A JSON-RPC request's reply: of a 2025 request when `capabilities`
    /// is `None`, else of a 2026-07-28 one from a client with them.
    fn rpc(
        &self,
        method: &str,
        mut params: Value,
        capabilities: Option<&Value>,
        token: &str,
    ) -> Value {
        let mut request = Request::builder()
            .method(Method::POST)
            .uri("/api/mcp")
            .header("accept", "application/json, text/event-stream")
            .header("content-type", "application/json")
            .header("authorization", format!("Bearer {token}"))
            .header("x-request-id", "req-7");
        if let Some(capabilities) = capabilities {
            request = request.header("mcp-protocol-version", "2026-07-28");
            params["_meta"] = envelope(capabilities);
        }
        let body = json!({"jsonrpc": "2.0", "id": next_id(), "method": method, "params": params});
        let request = request
            .body(Bytes::from(body.to_string()))
            .expect("a request");
        // The endpoint's gate established the caller, whom a confirmation
        // is bound to.
        let tools = RouterTools {
            items: &self.items,
            tools: route_tools(),
            incoming: &request,
            request_id: "req-7",
        };
        let options = Options::new(ServerInfo::new("items-api", "1.0.0"))
            .with_confirmations(Arc::clone(&self.confirmations))
            .with_caller(caller_of(bearer(&request), None))
            .with_request_id("req-7");
        message(&block_on(serve(&request, &tools, &options)))
    }

    /// A `tools/call`'s result.
    fn call_as(&self, params: Value, capabilities: Option<&Value>, token: &str) -> Value {
        let reply = self.rpc("tools/call", params, capabilities, token);
        assert!(reply.get("error").is_none(), "{reply}");
        reply["result"].clone()
    }

    fn call(&self, params: Value, capabilities: Option<&Value>) -> Value {
        self.call_as(params, capabilities, "ada")
    }
}

fn rename(arguments: Value) -> Value {
    json!({"name": "rename_item", "arguments": arguments})
}

/// A call's arguments with a confirmation token among them.
fn confirmed(mut arguments: Value, token: &Value) -> Value {
    arguments[CONFIRMATION_ARGUMENT] = token.clone();
    rename(arguments)
}

/// A retry of a call with the person's answer to the elicitation.
fn answered(arguments: Value, action: &str, state: &Value) -> Value {
    json!({
        "name": "rename_item",
        "arguments": arguments,
        "inputResponses": {"confirm": {"action": action}},
        "requestState": state,
    })
}

fn desk() -> Value {
    json!({"id": "i1", "newName": "Desk"})
}

fn token_of(result: &Value) -> Value {
    let token = &result["structuredContent"]["details"][CONFIRMATION_ARGUMENT];
    assert!(token.is_string(), "{result}");
    token.clone()
}

const SUMMARY: &str = r#"Rename an item (rename_item) with {"id":"i1","newName":"Desk"}"#;

#[test]
fn tools_list_gives_a_tool_that_asks_the_argument_its_confirmation_comes_back_in_and_no_other() {
    let endpoint = Endpoint::new();
    let reply = endpoint.rpc("tools/list", json!({}), None, "ada");
    let tools = reply["result"]["tools"].as_array().expect("tools");
    let names: Vec<&str> = tools.iter().map(|tool| text_of(&tool["name"])).collect();
    assert_eq!(names, ["rename_item", "get_item", "purge_item"]);
    let table = route_tools();
    let mut properties = table[0].tool.input_schema["properties"].clone();
    properties[CONFIRMATION_ARGUMENT] =
        tools[0]["inputSchema"]["properties"][CONFIRMATION_ARGUMENT].clone();
    assert_eq!(properties[CONFIRMATION_ARGUMENT]["type"], "string");
    assert_eq!(tools[0]["inputSchema"]["properties"], properties);
    assert_eq!(
        tools[0]["inputSchema"]["required"],
        json!(["id", "newName"])
    );
    assert_eq!(tools[0]["_meta"], json!({"invocationPolicy": "ask"}));
    assert_eq!(tools[1]["inputSchema"], table[1].tool.input_schema);
}

#[test]
fn a_client_without_elicitation_runs_nothing_until_it_sends_the_token_the_first_call_answered_once()
{
    let endpoint = Endpoint::new();
    let first = endpoint.call(rename(desk()), None);
    assert_eq!(first["isError"], true);
    let problem = &first["structuredContent"];
    assert_eq!(problem["status"], 428);
    assert_eq!(problem["title"], "Precondition Required");
    assert_eq!(problem["code"], "confirmation_required");
    assert_eq!(problem["requestId"], "req-7");
    assert_eq!(problem["details"]["tool"], "rename_item");
    assert_eq!(problem["details"]["summary"], SUMMARY);
    let detail = text_of(&problem["detail"]);
    assert!(detail.starts_with(&format!(
        "rename_item runs only once the person confirms it: {SUMMARY}."
    )));
    assert!(detail.contains(
        "call rename_item again with the same arguments and confirmationToken set to details.confirmationToken"
    ));
    assert!(detail.ends_with(&format!(
        "before {}",
        text_of(&problem["details"]["expiresAt"])
    )));
    let text: Value = serde_json::from_str(text_of(&first["content"][0]["text"])).expect("JSON");
    assert_eq!(&text, problem);
    assert!(endpoint.items.renames().is_empty());

    let token = token_of(&first);
    let run = endpoint.call(
        confirmed(json!({"newName": "Desk", "id": "i1"}), &token),
        None,
    );
    assert_eq!(
        run,
        json!({
            "content": [{"type": "text", "text": r#"{"id":"i1","name":"Desk"}"#}],
            "structuredContent": {"id": "i1", "name": "Desk"},
        })
    );
    assert_eq!(endpoint.items.renames(), ["Desk"]);

    let again = endpoint.call(confirmed(desk(), &token), None);
    let problem = &again["structuredContent"];
    assert_eq!(
        (&problem["status"], &problem["code"]),
        (&json!(428), &json!("confirmation_required"))
    );
    assert_eq!(problem["details"]["refused"], "used");
    assert!(text_of(&problem["detail"])
        .starts_with("The confirmation sent was used already. rename_item runs only once"));
    assert_ne!(token_of(&again), token);
    assert_eq!(endpoint.items.renames(), ["Desk"]);
}

#[test]
fn a_token_for_other_arguments_another_caller_or_none_this_endpoint_gave_runs_nothing() {
    let endpoint = Endpoint::new();
    let token = token_of(&endpoint.call(rename(desk()), None));
    let refused = |result: &Value| result["structuredContent"]["details"]["refused"].clone();

    let other = endpoint.call(
        confirmed(json!({"id": "i1", "newName": "Chair"}), &token),
        None,
    );
    assert_eq!(other["structuredContent"]["code"], "confirmation_required");
    assert_eq!(refused(&other), "mismatch");
    assert_eq!(
        other["structuredContent"]["details"]["summary"],
        r#"Rename an item (rename_item) with {"id":"i1","newName":"Chair"}"#
    );
    let root = endpoint.call_as(confirmed(desk(), &token), None, "root");
    assert_eq!(refused(&root), "mismatch");
    let text = text_of(&token);
    let forged = json!(format!("{}AA", &text[..text.len() - 2]));
    assert_eq!(
        refused(&endpoint.call(confirmed(desk(), &forged), None)),
        "invalid"
    );
    assert_eq!(
        refused(&endpoint.call(confirmed(desk(), &json!(42)), None)),
        "invalid"
    );
    assert_eq!(
        refused(&endpoint.call(confirmed(desk(), &Value::Null), None)),
        "invalid"
    );
    let other_key = Endpoint::with(confirmations(OTHER_KEY, "items-api"));
    assert_eq!(
        refused(&other_key.call(confirmed(desk(), &token), None)),
        "invalid"
    );
    assert!(endpoint.items.renames().is_empty() && other_key.items.renames().is_empty());
    // None of the refusals spent the token.
    let run = endpoint.call(confirmed(desk(), &token), None);
    assert_eq!(
        run["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    assert_eq!(endpoint.items.renames(), ["Desk"]);
}

#[test]
fn instances_under_one_key_redeem_each_others_tokens_once_across_them_when_they_share_a_store() {
    let store: Arc<dyn ConfirmationStore> = Arc::new(MemoryConfirmationStore::new());
    let instance =
        || Endpoint::with(confirmations(KEY, "items-api").with_store(Arc::clone(&store)));
    let (one, two) = (instance(), instance());
    let token = token_of(&one.call(rename(desk()), None));
    let run = two.call(confirmed(desk(), &token), None);
    assert_eq!(
        run["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    let again = one.call(confirmed(desk(), &token), None);
    assert_eq!(again["structuredContent"]["details"]["refused"], "used");
    assert!(one.items.renames().is_empty());
    assert_eq!(two.items.renames(), ["Desk"]);

    // Without a shared store a token redeems once on each instance.
    let (alone, apart) = (Endpoint::new(), Endpoint::new());
    let token = token_of(&alone.call(rename(desk()), None));
    for endpoint in [&alone, &apart] {
        let run = endpoint.call(confirmed(desk(), &token), None);
        assert_eq!(run["structuredContent"]["name"], "Desk");
        let again = endpoint.call(confirmed(desk(), &token), None);
        assert_eq!(again["structuredContent"]["details"]["refused"], "used");
    }
}

#[test]
fn a_tool_that_does_not_ask_runs_at_once() {
    let endpoint = Endpoint::new();
    let get = || json!({"name": "get_item", "arguments": {"id": "i1"}});
    assert_eq!(
        endpoint.call(get(), None)["structuredContent"],
        json!({"id": "i1"})
    );
    let modern = endpoint.call(get(), elicits().as_ref());
    assert_eq!(modern["structuredContent"], json!({"id": "i1"}));
    assert_eq!(modern["resultType"], "complete");
}

#[test]
fn a_2026_client_that_elicits_forms_is_asked_in_band_and_the_call_runs_on_accept() {
    let endpoint = Endpoint::new();
    let elicits = elicits();
    let asked = endpoint.call(rename(desk()), elicits.as_ref());
    assert_eq!(asked["resultType"], "input_required");
    assert_eq!(
        asked["inputRequests"],
        json!({"confirm": {
            "method": "elicitation/create",
            "params": {
                "mode": "form",
                "message": format!("Run {SUMMARY}?"),
                "requestedSchema": {"type": "object", "properties": {}},
            },
        }})
    );
    assert_eq!(
        asked["_meta"]["io.modelcontextprotocol/serverInfo"],
        json!({"name": "items-api", "version": "1.0.0"})
    );
    let state = &asked["requestState"];
    assert!(text_of(state).starts_with("v1.elicit."));
    assert!(asked.get("isError").is_none());
    assert!(endpoint.items.renames().is_empty());

    let accepted = endpoint.call(answered(desk(), "accept", state), elicits.as_ref());
    assert_eq!(accepted["resultType"], "complete");
    assert_eq!(
        accepted["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    assert_eq!(endpoint.items.renames(), ["Desk"]);

    // The same retry again is asked about again, and runs nothing.
    let replayed = endpoint.call(answered(desk(), "accept", state), elicits.as_ref());
    assert_eq!(replayed["resultType"], "input_required");
    assert!(
        text_of(&replayed["inputRequests"]["confirm"]["params"]["message"])
            .starts_with("The confirmation sent was used already. Run Rename an item")
    );
    assert_eq!(endpoint.items.renames(), ["Desk"]);
}

#[test]
fn decline_and_cancel_run_nothing_and_are_a_tool_error() {
    let endpoint = Endpoint::new();
    let elicits = elicits();
    for (action, detail) in [
        (
            "decline",
            "The person declined rename_item, which did not run",
        ),
        (
            "cancel",
            "The person dismissed the confirmation of rename_item, which did not run",
        ),
    ] {
        let asked = endpoint.call(rename(desk()), elicits.as_ref());
        let state = &asked["requestState"];
        let refused = endpoint.call(answered(desk(), action, state), elicits.as_ref());
        assert_eq!(refused["resultType"], "complete");
        assert_eq!(refused["isError"], true);
        let problem = &refused["structuredContent"];
        assert_eq!(
            (&problem["status"], &problem["title"]),
            (&json!(403), &json!("Forbidden"))
        );
        assert_eq!(problem["code"], "confirmation_declined");
        assert_eq!(
            problem["details"],
            json!({"tool": "rename_item", "action": action})
        );
        assert_eq!(problem["detail"], detail);
        // The declined confirmation is spent: accepting it later runs
        // nothing.
        let late = endpoint.call(answered(desk(), "accept", state), elicits.as_ref());
        assert_eq!(late["resultType"], "input_required");
    }
    assert!(endpoint.items.renames().is_empty());
}

#[test]
fn a_request_state_for_another_call_or_a_token_of_the_other_path_is_asked_about_again() {
    let endpoint = Endpoint::new();
    let elicits = elicits();
    let asked = endpoint.call(rename(desk()), elicits.as_ref());
    let state = &asked["requestState"];
    let message = |result: &Value| result["inputRequests"]["confirm"]["params"]["message"].clone();

    let chair = json!({"id": "i1", "newName": "Chair"});
    let other = endpoint.call(answered(chair, "accept", state), elicits.as_ref());
    assert_eq!(
        message(&other),
        r#"The confirmation sent is for another call. Run Rename an item (rename_item) with {"id":"i1","newName":"Chair"}?"#
    );
    // A requestState is no confirmation token, so the agent cannot skip
    // the person's answer.
    let as_token = endpoint.call(confirmed(desk(), state), elicits.as_ref());
    assert_eq!(as_token["resultType"], "input_required");
    assert!(text_of(&message(&as_token)).starts_with("The confirmation sent is for another call."));
    let via_token = endpoint.call(confirmed(desk(), state), None);
    assert_eq!(
        via_token["structuredContent"]["details"]["refused"],
        "mismatch"
    );
    // A retry without the answer, or with one that is no answer, is asked
    // about again.
    let unanswered = json!({"name": "rename_item", "arguments": desk(), "requestState": state});
    let unanswered = endpoint.call(unanswered, elicits.as_ref());
    assert_eq!(unanswered["resultType"], "input_required");
    assert_eq!(message(&unanswered), format!("Run {SUMMARY}?"));
    let shrugged = endpoint.call(answered(desk(), "maybe", state), elicits.as_ref());
    assert_eq!(message(&shrugged), format!("Run {SUMMARY}?"));
    assert!(endpoint.items.renames().is_empty());
    // A two-step token works for an eliciting client too.
    let token = token_of(&endpoint.call(rename(desk()), None));
    let run = endpoint.call(confirmed(desk(), &token), elicits.as_ref());
    assert_eq!(
        run["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    assert_eq!(endpoint.items.renames(), ["Desk"]);
    // And the requestState the person never answered is still good.
    let accepted = endpoint.call(answered(desk(), "accept", state), elicits.as_ref());
    assert_eq!(accepted["structuredContent"]["name"], "Desk");
}

#[test]
fn a_2026_client_that_elicits_no_form_and_every_2025_client_gets_the_two_step_token() {
    let endpoint = Endpoint::new();
    for capabilities in [
        json!({}),
        json!({"elicitation": {"url": {}}}),
        json!({"sampling": {}}),
    ] {
        let first = endpoint.call(rename(desk()), Some(&capabilities));
        assert_eq!(first["resultType"], "complete", "{capabilities}");
        assert_eq!(first["isError"], true);
        assert_eq!(first["structuredContent"]["code"], "confirmation_required");
    }
    // The 2025 revisions declare elicitation at initialize, which a
    // stateless endpoint does not keep.
    let legacy = endpoint.call(rename(desk()), None);
    assert_eq!(legacy["structuredContent"]["code"], "confirmation_required");
    assert!(endpoint.items.renames().is_empty());
    // An elicitation capability that names no mode elicits forms, as the
    // 2025 revisions declared it.
    let bare = endpoint.call(rename(desk()), Some(&json!({"elicitation": {}})));
    assert_eq!(bare["resultType"], "input_required");
}

#[test]
fn a_tool_table_may_not_name_a_tool_twice_nor_give_one_that_asks_the_token_argument() {
    let mut tools = route_tools();
    assert_eq!(check_route_tools(&tools), Ok(()));
    let taking: RouteTool = serde_json::from_value(json!({
        "name": "rename_item",
        "inputSchema": {"type": "object"},
        "operation": "renameItem",
        "arguments": [{"key": CONFIRMATION_ARGUMENT, "in": "body", "name": "token"}],
        "confirm": true,
    }))
    .expect("a tool");
    assert_eq!(
        check_route_tools(std::slice::from_ref(&taking)),
        Err(
            "MCP tool rename_item asks the person before it runs, and takes an argument confirmationToken, which carries its confirmation"
                .to_owned()
        )
    );
    // A tool that does not ask may take an argument of that name.
    let mut plain = taking.clone();
    plain.confirm = false;
    assert_eq!(check_route_tools(std::slice::from_ref(&plain)), Ok(()));
    tools.push(plain);
    assert_eq!(
        check_route_tools(&tools),
        Err("two MCP tools are named rename_item".to_owned())
    );
}

// A call of a tool that asks, which its route would refuse (D98).

#[test]
fn a_mis_shaped_call_is_refused_at_once_as_the_route_refuses_it_and_no_one_is_asked() {
    let endpoint = Endpoint::new();
    for capabilities in [None, Some(json!({})), elicits()] {
        let wrapped = json!({"id": "i1", "input": {"newName": "Desk"}});
        let misnamed = endpoint.call(rename(wrapped), capabilities.as_ref());
        assert_eq!(misnamed["isError"], true);
        let problem = &misnamed["structuredContent"];
        assert_eq!(
            (&problem["status"], &problem["code"]),
            (&json!(400), &json!("bad_request"))
        );
        assert_eq!(
            problem["detail"],
            r#"rename_item takes id, newName, not "input""#
        );
        assert!(problem.get("details").is_none() && misnamed.get("requestState").is_none());
        for arguments in [
            json!({"id": "i1", "newName": ""}),
            json!({"id": "i1", "newName": 7}),
            json!({"id": "i1"}),
            json!({"newName": "Desk"}),
        ] {
            let refused = endpoint.call(rename(arguments.clone()), capabilities.as_ref());
            assert_eq!(refused["isError"], true, "{arguments}");
            let problem = &refused["structuredContent"];
            assert_eq!(
                (&problem["status"], &problem["code"]),
                (&json!(400), &json!("bad_request"))
            );
            assert_eq!(problem["requestId"], "req-7");
            assert!(problem.get("details").is_none() && refused.get("inputRequests").is_none());
        }
    }
    assert!(endpoint.items.renames().is_empty());
}

#[test]
fn a_confirmation_sent_with_a_call_that_would_not_run_is_not_spent_and_runs_its_own_call_after() {
    let endpoint = Endpoint::new();
    let elicits = elicits();
    let token = token_of(&endpoint.call(rename(desk()), None));
    let empty = json!({"id": "i1", "newName": ""});
    let mis_shaped = endpoint.call(confirmed(empty, &token), None);
    assert_eq!(mis_shaped["structuredContent"]["status"], 400);
    assert!(mis_shaped["structuredContent"].get("details").is_none());
    // A requestState sent back with a mis-shaped retry, accepted or
    // declined.
    let asked = endpoint.call(rename(desk()), elicits.as_ref());
    let state = &asked["requestState"];
    for action in ["accept", "decline"] {
        let wrapped = json!({"id": "i1", "input": {"newName": "Desk"}});
        let refused = endpoint.call(answered(wrapped, action, state), elicits.as_ref());
        assert_eq!(refused["resultType"], "complete");
        assert_eq!(refused["isError"], true);
        assert_eq!(refused["structuredContent"]["code"], "bad_request");
    }
    assert!(endpoint.items.renames().is_empty());

    // Both confirmations still run the calls they were given for.
    let run = endpoint.call(confirmed(desk(), &token), None);
    assert_eq!(
        run["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    let accepted = endpoint.call(answered(desk(), "accept", state), elicits.as_ref());
    assert_eq!(
        accepted["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    assert_eq!(endpoint.items.renames(), ["Desk", "Desk"]);

    // A confirmation of the mis-shaped call itself, as an endpoint gave
    // one before it checked a call, is refused with the call and not
    // claimed.
    let stale = ada().with(json!({"id": "i1", "input": {"newName": "Desk"}}));
    let minted = mint(&endpoint.confirmations, ConfirmationMode::Token, &stale);
    let refused = endpoint.call(
        confirmed(Value::Object(stale.arguments.clone()), &json!(minted)),
        None,
    );
    assert_eq!(refused["structuredContent"]["status"], 400);
    assert_eq!(
        redeem(
            &endpoint.confirmations,
            &minted,
            ConfirmationMode::Token,
            &stale
        ),
        None
    );
    assert_eq!(endpoint.items.renames(), ["Desk", "Desk"]);
}

#[test]
fn a_caller_the_routes_gate_refuses_is_refused_at_once_and_no_one_is_asked() {
    let endpoint = Endpoint::new();
    for capabilities in [None, elicits()] {
        let scoped_out = endpoint.call_as(rename(desk()), capabilities.as_ref(), "reader");
        assert_eq!(scoped_out["isError"], true);
        let problem = &scoped_out["structuredContent"];
        assert_eq!(
            (&problem["status"], &problem["code"]),
            (&json!(403), &json!("forbidden"))
        );
        assert_eq!(
            problem["detail"],
            "This key may read items, not rename them"
        );
        assert!(scoped_out.get("requestState").is_none());
        let purge = json!({"name": "purge_item", "arguments": {"id": "i1"}});
        let unserved = endpoint.call(purge, capabilities.as_ref());
        assert_eq!(unserved["isError"], true);
        let problem = &unserved["structuredContent"];
        assert_eq!(
            (&problem["status"], &problem["code"]),
            (&json!(401), &json!("service_unauthorized"))
        );
        assert!(unserved.get("requestState").is_none() && problem.get("details").is_none());
    }
    // A confirmation of reader's call is refused with it and not claimed.
    let reader = Call {
        caller: r#"["reader",null]"#,
        ..ada()
    };
    let minted = mint(&endpoint.confirmations, ConfirmationMode::Token, &reader);
    let refused = endpoint.call_as(confirmed(desk(), &json!(minted)), None, "reader");
    assert_eq!(refused["structuredContent"]["code"], "forbidden");
    assert_eq!(
        redeem(
            &endpoint.confirmations,
            &minted,
            ConfirmationMode::Token,
            &reader
        ),
        None
    );
    // A tool whose route needs a permission the caller lacks is no tool
    // of theirs (D81), which asks no one either.
    let archive = json!({"name": "archive_item", "arguments": {"id": "i1"}});
    let reply = endpoint.rpc("tools/call", archive, None, "ada");
    assert_eq!(
        reply["error"],
        json!({"code": -32602, "message": r#"items-api has no tool "archive_item""#})
    );
    assert!(endpoint.items.renames().is_empty());
    // ada, whom the rename route admits, is asked.
    let asked = endpoint.call(rename(desk()), None);
    assert_eq!(asked["structuredContent"]["code"], "confirmation_required");
}

// The transport, over a tool set that is no router's.

/// A tool set whose one tool asks, which records the arguments it checked
/// and ran with.
#[derive(Default)]
struct Asking {
    checks: bool,
    checked: Mutex<Vec<Value>>,
    runs: Mutex<Vec<String>>,
}

impl Asking {
    fn tool() -> Tool {
        route_tools().swap_remove(0).tool
    }

    fn runs(&self) -> Vec<String> {
        self.runs.lock().expect("the runs").clone()
    }
}

#[async_trait]
impl ToolSet for Asking {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Ok(vec![Self::tool()])
    }

    async fn confirms(&self, name: &str) -> Result<Option<Tool>, ToolError> {
        Ok((name == "rename_item").then(Self::tool))
    }

    async fn check(
        &self,
        _name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<Option<Problem>, ToolError> {
        if !self.checks {
            return Ok(None);
        }
        self.checked
            .lock()
            .expect("the checks")
            .push(Value::Object(arguments.clone()));
        Ok((arguments.get("newName") == Some(&json!("")))
            .then(|| problem_body(&ApiError::bad_request("newName is empty"), Some("req-1"))))
    }

    async fn call(
        &self,
        _name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        let arguments = Value::Object(arguments.clone());
        self.runs
            .lock()
            .expect("the runs")
            .push(arguments.to_string());
        Ok(CallOutcome::Result(arguments))
    }
}

/// A 2025 `tools/call`'s reply through the transport, as the caller ada.
fn post(tools: &dyn ToolSet, confirmations: Option<&Arc<Confirmations>>, params: Value) -> Value {
    let body = json!({"jsonrpc": "2.0", "id": next_id(), "method": "tools/call", "params": params});
    let request = Request::builder()
        .method(Method::POST)
        .uri("/api/mcp")
        .header("accept", "application/json, text/event-stream")
        .header("content-type", "application/json")
        .body(Bytes::from(body.to_string()))
        .expect("a request");
    let mut options = Options::new(ServerInfo::new("items-api", "1.0.0"))
        .with_caller("ada")
        .with_request_id("req-1");
    if let Some(confirmations) = confirmations {
        options = options.with_confirmations(Arc::clone(confirmations));
    }
    message(&block_on(serve(&request, tools, &options)))
}

#[test]
fn a_tool_set_that_asks_with_no_confirmations_refuses_the_call_and_runs_nothing() {
    let tools = Asking::default();
    let refused = post(&tools, None, rename(desk()));
    let problem = &refused["result"]["structuredContent"];
    assert_eq!(refused["result"]["isError"], true);
    assert_eq!(
        (&problem["status"], &problem["code"]),
        (&json!(500), &json!("internal_error"))
    );
    assert_eq!(problem["requestId"], "req-1");
    assert_eq!(
        problem["detail"],
        "rename_item runs only once the person confirms it, and this endpoint has no confirmation key"
    );
    assert!(tools.runs().is_empty());
}

#[test]
fn a_tool_sets_check_refuses_a_call_before_it_is_confirmed_given_the_arguments_the_confirmation_binds(
) {
    let strict = Asking {
        checks: true,
        ..Asking::default()
    };
    let confirmations = Arc::new(confirmations(KEY, ""));
    let empty = || json!({"id": "i1", "newName": ""});
    // No confirmations either: the check comes first.
    let unkeyed = post(&strict, None, rename(empty()));
    assert_eq!(unkeyed["result"]["structuredContent"]["status"], 400);
    assert_eq!(
        unkeyed["result"]["structuredContent"]["detail"],
        "newName is empty"
    );
    let token = token_of(&post(&strict, Some(&confirmations), rename(desk()))["result"]);
    let refused = post(&strict, Some(&confirmations), confirmed(empty(), &token));
    assert_eq!(refused["result"]["structuredContent"]["status"], 400);
    assert!(strict.runs().is_empty());
    let run = post(&strict, Some(&confirmations), confirmed(desk(), &token));
    assert_eq!(run["result"]["structuredContent"], desk());
    assert_eq!(strict.runs(), [r#"{"id":"i1","newName":"Desk"}"#]);
    assert_eq!(
        *strict.checked.lock().expect("the checks"),
        [empty(), desk(), empty(), desk()]
    );
}

#[test]
fn an_expired_token_is_asked_about_again_and_the_call_runs_with_the_arguments_the_token_was_not_among(
) {
    let tools = Asking::default();
    let now = Arc::new(AtomicU64::new(0));
    let read = Arc::clone(&now);
    let confirmations = Arc::new(
        Confirmations::new(KEY)
            .and_then(|confirmations| confirmations.with_ttl_seconds(1))
            .expect("a key and a time to live")
            .with_clock(Arc::new(move || read.load(Ordering::SeqCst))),
    );
    let first = post(&tools, Some(&confirmations), rename(desk()));
    assert_eq!(first["result"]["structuredContent"]["requestId"], "req-1");
    assert_eq!(
        first["result"]["structuredContent"]["details"]["expiresAt"],
        "1970-01-01T00:00:01Z"
    );
    let token = token_of(&first["result"]);
    now.store(1000, Ordering::SeqCst);
    let expired = post(&tools, Some(&confirmations), confirmed(desk(), &token));
    let problem = &expired["result"]["structuredContent"];
    assert_eq!(problem["code"], "confirmation_required");
    assert_eq!(problem["details"]["refused"], "expired");
    assert!(text_of(&problem["detail"]).starts_with("The confirmation sent has expired."));
    assert!(tools.runs().is_empty());
    let fresh = token_of(&expired["result"]);
    let run = post(&tools, Some(&confirmations), confirmed(desk(), &fresh));
    assert_eq!(run["result"]["structuredContent"], desk());
    assert_eq!(tools.runs(), [r#"{"id":"i1","newName":"Desk"}"#]);
}

#[test]
fn a_summary_past_2000_characters_of_arguments_counts_the_rest() {
    let tools = Asking::default();
    let confirmations = Arc::new(confirmations(KEY, ""));
    let long = json!({"id": "i1", "newName": "x".repeat(3000)});
    let first = post(&tools, Some(&confirmations), rename(long.clone()));
    let summary = text_of(&first["result"]["structuredContent"]["details"]["summary"]);
    assert!(summary.starts_with(r#"Rename an item (rename_item) with {"id":"i1","newName":"xxx"#));
    let rest = canonical_json(&long).len() - 2000;
    assert!(
        summary.ends_with(&format!("x\u{2026} ({rest} more characters)")),
        "{rest}"
    );
    // A tool with no title, or one that is its name, is its name; no
    // arguments are said so.
    struct Bare;
    #[async_trait]
    impl ToolSet for Bare {
        async fn list(&self) -> Result<Vec<Tool>, ToolError> {
            Ok(Vec::new())
        }
        async fn confirms(&self, name: &str) -> Result<Option<Tool>, ToolError> {
            let mut tool = Asking::tool();
            tool.name = name.to_owned();
            tool.title = (name == "same").then(|| name.to_owned());
            Ok(Some(tool))
        }
        async fn call(&self, _: &str, _: &Map<String, Value>) -> Result<CallOutcome, ToolError> {
            Ok(CallOutcome::Result(Value::Null))
        }
    }
    for name in ["bare", "same"] {
        let asked = post(&Bare, Some(&confirmations), json!({"name": name}));
        assert_eq!(
            asked["result"]["structuredContent"]["details"]["summary"],
            format!("{name} with no arguments")
        );
    }
}

/// A store that cannot be asked.
struct Down;

#[async_trait]
impl ConfirmationStore for Down {
    async fn claim(&self, _id: &str, _ttl_ms: u64, _now_ms: u64) -> Result<bool, BoxError> {
        Err("the store is down".into())
    }
}

#[test]
fn a_store_that_cannot_be_asked_fails_the_call_and_runs_nothing() {
    let tools = Asking::default();
    let confirmations = Arc::new(confirmations(KEY, "").with_store(Arc::new(Down)));
    let token = token_of(&post(&tools, Some(&confirmations), rename(desk()))["result"]);
    let failed = post(&tools, Some(&confirmations), confirmed(desk(), &token));
    assert_eq!(
        failed["error"],
        json!({"code": -32603, "message": "Internal error"})
    );
    assert!(tools.runs().is_empty());
}
