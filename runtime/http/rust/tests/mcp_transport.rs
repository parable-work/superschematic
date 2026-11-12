//! The MCP transport's own answers (D81, D116), which the engine's corpus
//! (`mcp_parity.rs`) does not record: what it refuses before any method
//! runs, a batch, the 2026-07-28 envelope and its headers, and each kind of
//! tool outcome. The cases are those the TypeScript runtime's `mcp.test.ts`
//! holds `serveMcp` to.

use async_trait::async_trait;
use bytes::Bytes;
use http::{Method, Request, Response, StatusCode};
use serde_json::{json, Map, Value};
use std::future::Future;
use std::task::{Context, Poll, Waker};
use superschematic_http_runtime::mcp::{
    serve, CallOutcome, Content, Options, ServerInfo, Tool, ToolError, ToolSet, MCP_MAX_BATCH,
    MCP_MAX_BODY_BYTES,
};

/// Polls a future to its end on this thread: `serve` waits on nothing but
/// its tools, which here are ready.
fn block_on<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(output) => output,
        Poll::Pending => panic!("serve waited on something its tools did not hand it"),
    }
}

/// Tools that answer by the name called.
struct Named;

#[async_trait]
impl ToolSet for Named {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Ok(vec![Tool {
            name: "echo".to_owned(),
            title: Some("Echo".to_owned()),
            description: None,
            input_schema: json!({"type": "object"}),
            annotations: None,
            meta: Some(Map::from_iter([(
                "invocationPolicy".to_owned(),
                json!("auto"),
            )])),
        }])
    }

    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        match name {
            "echo" => Ok(CallOutcome::Result(Value::Object(arguments.clone()))),
            "pong" => Ok(CallOutcome::Result(json!("pong"))),
            "nothing" => Ok(CallOutcome::Result(Value::Null)),
            "export" => Ok(CallOutcome::Content(vec![
                Content::Text {
                    text: "id,name\n".to_owned(),
                },
                Content::Resource {
                    uri: "/api/export".to_owned(),
                    mime_type: "application/pdf".to_owned(),
                    blob: "JVBERg==".to_owned(),
                },
            ])),
            "refused" => Ok(CallOutcome::Problem(json!({
                "type": "about:blank",
                "title": "Conflict",
                "status": 409,
                "detail": "The item changed",
                "code": "conflict",
            }))),
            "broken" => Err(ToolError::Failed("the database is down".into())),
            other => Err(ToolError::unknown(format!("no tool {other}"))),
        }
    }
}

/// Tools whose list fails.
struct Unlisted;

#[async_trait]
impl ToolSet for Unlisted {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Err(ToolError::unknown("no list"))
    }

    async fn call(&self, _: &str, _: &Map<String, Value>) -> Result<CallOutcome, ToolError> {
        Ok(CallOutcome::Result(Value::Null))
    }
}

const ACCEPT: &str = "application/json, text/event-stream";
const JSONRPC: [(&str, &str); 2] = [("accept", ACCEPT), ("content-type", "application/json")];
const MODERN: [(&str, &str); 3] = [
    ("accept", ACCEPT),
    ("content-type", "application/json"),
    ("mcp-protocol-version", "2026-07-28"),
];

fn server() -> Options {
    Options::new(ServerInfo::new("items-api", "1.0.0"))
}

fn send_to(
    options: &Options,
    method: Method,
    headers: &[(&str, &str)],
    body: impl Into<Bytes>,
) -> Response<Bytes> {
    let mut request = Request::builder().method(method).uri("/api/mcp");
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let request = request.body(body.into()).expect("a request");
    block_on(serve(&request, &Named, options))
}

fn send(headers: &[(&str, &str)], body: impl Into<Bytes>) -> Response<Bytes> {
    send_to(&server(), Method::POST, headers, body)
}

fn rpc(id: u64, method: &str, params: Value) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

fn post(message: &Value) -> Response<Bytes> {
    send(&JSONRPC, message.to_string())
}

fn header<'a>(response: &'a Response<Bytes>, name: &str) -> Option<&'a str> {
    response
        .headers()
        .get(name)
        .and_then(|value| value.to_str().ok())
}

/// The status and JSON body of an answer that is no stream.
fn refusal(response: &Response<Bytes>) -> (u16, Value) {
    assert_eq!(header(response, "content-type"), Some("application/json"));
    assert!(header(response, "mcp-session-id").is_none());
    let body = serde_json::from_slice(response.body()).expect("a JSON body");
    (response.status().as_u16(), body)
}

fn entry_error(status: u16, code: i64, message: &str) -> (u16, Value) {
    (
        status,
        json!({"jsonrpc": "2.0", "error": {"code": code, "message": message}, "id": null}),
    )
}

/// The JSON-RPC messages of an event-stream answer.
fn messages(response: &Response<Bytes>) -> Vec<Value> {
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(header(response, "content-type"), Some("text/event-stream"));
    assert_eq!(
        header(response, "cache-control"),
        Some("no-cache, no-transform")
    );
    assert_eq!(header(response, "x-accel-buffering"), Some("no"));
    assert!(header(response, "mcp-session-id").is_none());
    let text = String::from_utf8_lossy(response.body());
    assert!(text.ends_with("\n\n"), "{text:?}");
    text.split("\n\n")
        .filter(|frame| !frame.is_empty())
        .map(|frame| {
            let data = frame
                .strip_prefix("event: message\ndata: ")
                .unwrap_or_else(|| panic!("{frame:?} is no message event"));
            serde_json::from_str(data).expect("a frame's JSON")
        })
        .collect()
}

/// The result of a 2025 `tools/call`.
fn call(name: &str, arguments: Value) -> Value {
    let reply = messages(&post(&rpc(
        1,
        "tools/call",
        json!({"name": name, "arguments": arguments}),
    )));
    assert_eq!(reply.len(), 1);
    reply[0].clone()
}

fn envelope() -> Value {
    json!({
        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
        "io.modelcontextprotocol/clientInfo": {"name": "t", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": {},
    })
}

fn server_stamp() -> Value {
    json!({"io.modelcontextprotocol/serverInfo": {"name": "items-api", "version": "1.0.0"}})
}

#[test]
fn get_and_delete_answer_405_since_a_stateless_server_keeps_no_session() {
    for method in [Method::GET, Method::DELETE, Method::PUT] {
        let answer = send_to(&server(), method, &[("accept", "text/event-stream")], "");
        assert_eq!(
            refusal(&answer),
            entry_error(405, -32000, "Method not allowed.")
        );
    }
}

#[test]
fn refuses_what_is_not_a_json_rpc_exchange_over_json_as_the_sdk_does() {
    assert_eq!(
        refusal(&send(
            &[("accept", ACCEPT), ("content-type", "text/plain")],
            "{}"
        )),
        entry_error(
            415,
            -32000,
            "Unsupported Media Type: Content-Type must be application/json"
        )
    );
    assert_eq!(refusal(&send(&[("accept", ACCEPT)], "{}")).0, 415);
    let not_acceptable = entry_error(
        406,
        -32000,
        "Not Acceptable: Client must accept both application/json and text/event-stream",
    );
    let ping = rpc(1, "ping", json!({})).to_string();
    for accept in [Some("application/json"), Some("text/event-stream"), None] {
        let mut headers = vec![("content-type", "application/json")];
        headers.extend(accept.map(|accept| ("accept", accept)));
        assert_eq!(refusal(&send(&headers, ping.clone())), not_acceptable);
        // A body that is no JSON is refused for its Accept first.
        assert_eq!(refusal(&send(&headers, "{nope")), not_acceptable);
    }
    let parse_error = entry_error(400, -32700, "Parse error: Invalid JSON");
    assert_eq!(refusal(&send(&JSONRPC, "{nope")), parse_error);
    assert_eq!(refusal(&send(&JSONRPC, "")), parse_error);
    assert_eq!(
        refusal(&send(&JSONRPC, "[]")),
        entry_error(400, -32600, "Bad Request: empty JSON-RPC batch")
    );
    // An error the entry builds echoes the id of what had a method and an
    // id, and null otherwise.
    for (body, id) in [
        (json!({"id": 1}), Value::Null),
        (json!("text"), Value::Null),
        (
            json!({"jsonrpc": "1.0", "id": 1, "method": "ping"}),
            json!(1),
        ),
        (
            json!({"jsonrpc": "2.0", "id": 1.5, "method": "ping"}),
            Value::Null,
        ),
        (
            json!({"jsonrpc": "2.0", "id": "a", "method": "ping", "params": [1]}),
            json!("a"),
        ),
    ] {
        let mut invalid = entry_error(
            400,
            -32600,
            "Bad Request: the request body is not a valid JSON-RPC message",
        );
        invalid.1["id"] = id;
        assert_eq!(refusal(&post(&body)), invalid, "{body}");
    }
    assert_eq!(
        refusal(&post(&json!([rpc(1, "ping", json!({})), {"id": 2}]))),
        entry_error(
            400,
            -32600,
            "Bad Request: JSON-RPC batch contains an invalid message"
        )
    );
    let init = rpc(1, "initialize", json!({"protocolVersion": "2025-11-25"}));
    assert_eq!(
        refusal(&post(&json!([init, rpc(2, "ping", json!({}))]))),
        entry_error(
            400,
            -32600,
            "Invalid Request: Only one initialization request is allowed"
        )
    );
    let old = [
        ("accept", ACCEPT),
        ("content-type", "application/json; charset=utf-8"),
        ("mcp-protocol-version", " 2024-01-01 "),
    ];
    assert_eq!(
        refusal(&send(&old, ping.clone())),
        entry_error(
            400,
            -32000,
            "Bad Request: Unsupported protocol version: 2024-01-01 (supported versions: 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05, 2024-10-07)"
        )
    );
    // The header of a revision it speaks, and none on initialize, pass.
    let known = [
        ("accept", ACCEPT),
        ("content-type", "Application/JSON"),
        ("mcp-protocol-version", "2025-06-18"),
    ];
    assert_eq!(
        messages(&send(&known, ping.clone()))[0]["result"],
        json!({})
    );
    assert_eq!(messages(&send(&old, init.to_string())).len(), 1);
}

#[test]
fn a_body_past_the_limit_is_refused_by_its_length_or_the_length_it_declares() {
    let too_large = entry_error(
        413,
        -32000,
        "Payload Too Large: Request body must not exceed 4194304 bytes",
    );
    let declared = (MCP_MAX_BODY_BYTES + 1).to_string();
    let headers = [
        ("accept", ACCEPT),
        ("content-type", "application/json"),
        ("content-length", declared.as_str()),
    ];
    assert_eq!(refusal(&send(&headers, "{}")), too_large);
    let padding = " ".repeat(MCP_MAX_BODY_BYTES);
    let ping = rpc(1, "ping", json!({})).to_string();
    assert_eq!(
        refusal(&send(&JSONRPC, format!("{ping}{padding}"))),
        too_large
    );
    // A body at the limit is read.
    let at_limit = format!("{ping}{}", &padding[ping.len()..]);
    assert_eq!(at_limit.len(), MCP_MAX_BODY_BYTES);
    assert_eq!(messages(&send(&JSONRPC, at_limit)).len(), 1);
}

#[test]
fn answers_a_batch_with_one_event_per_request_a_notification_with_202_and_an_unknown_method_with_its_error(
) {
    let batch = json!([
        rpc(1, "ping", json!({})),
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 9, "result": {}},
        rpc(2, "resources/list", json!({})),
        {"jsonrpc": "2.0", "id": "three", "method": "ping"},
    ]);
    assert_eq!(
        messages(&post(&batch)),
        [
            json!({"result": {}, "jsonrpc": "2.0", "id": 1}),
            json!({"jsonrpc": "2.0", "id": 2, "error": {"code": -32601, "message": "Method not found"}}),
            json!({"result": {}, "jsonrpc": "2.0", "id": "three"}),
        ]
    );
    // Notifications and posted responses alone: nothing to answer.
    for body in [
        json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
        json!({"jsonrpc": "2.0", "id": 4, "result": {}}),
        json!({"jsonrpc": "2.0", "id": null, "error": {"code": -32000, "message": "no"}}),
        json!([{"jsonrpc": "2.0", "method": "notifications/initialized"}]),
    ] {
        let answer = post(&body);
        assert_eq!(answer.status(), StatusCode::ACCEPTED, "{body}");
        assert!(answer.body().is_empty() && answer.headers().is_empty());
    }
    let full: Vec<Value> = (0..MCP_MAX_BATCH as u64)
        .map(|id| rpc(id, "ping", json!({})))
        .collect();
    assert_eq!(messages(&post(&json!(full))).len(), MCP_MAX_BATCH);
    let mut over = full;
    over.push(rpc(100, "ping", json!({})));
    assert_eq!(
        refusal(&post(&json!(over))),
        entry_error(
            400,
            -32600,
            "Invalid Request: Batch must not exceed 100 messages"
        )
    );
    // A byte order mark before the body, and an Accept in two lines, are
    // read as a fetch Request reads them.
    let marked = format!("\u{feff}{}", rpc(5, "ping", json!({})));
    let lines = [
        ("accept", "application/json"),
        ("accept", "text/event-stream"),
        ("content-type", "application/json"),
    ];
    assert_eq!(messages(&send(&lines, marked))[0]["id"], 5);
}

#[test]
fn initialize_negotiates_a_revision_and_names_the_server() {
    let init = |version: Value| {
        rpc(
            1,
            "initialize",
            json!({"protocolVersion": version, "capabilities": {}}),
        )
    };
    let reply = messages(&post(&init(json!("2099-01-01"))));
    assert_eq!(
        reply[0]["result"],
        json!({
            "protocolVersion": "2025-11-25",
            "capabilities": {"tools": {"listChanged": false}},
            "serverInfo": {"name": "items-api", "version": "1.0.0"},
        })
    );
    let reply = messages(&post(&init(json!("2025-03-26"))));
    assert_eq!(reply[0]["result"]["protocolVersion"], "2025-03-26");
    let reply = messages(&post(&init(json!(20250326))));
    assert_eq!(
        reply[0]["error"],
        json!({"code": -32602, "message": "Invalid params for initialize: protocolVersion must be a string"})
    );

    let named = Options::new(ServerInfo::new("items", "2.0.0")).with_instructions("Items.");
    let answer = send_to(
        &named,
        Method::POST,
        &JSONRPC,
        init(json!("2025-11-25")).to_string(),
    );
    let result = &messages(&answer)[0]["result"];
    assert_eq!(
        result["serverInfo"],
        json!({"name": "items", "version": "2.0.0"})
    );
    assert_eq!(result["instructions"], "Items.");
    // Instructions of no text are none.
    let silent = server().with_instructions("");
    let answer = send_to(
        &silent,
        Method::POST,
        &JSONRPC,
        init(json!("2025-11-25")).to_string(),
    );
    assert!(messages(&answer)[0]["result"].get("instructions").is_none());
}

#[test]
fn a_call_answers_its_result_as_text_and_for_an_object_as_structured_content() {
    let echoed = call("echo", json!({"b": [1, 2], "a": "x"}));
    let structured = json!({"a": "x", "b": [1, 2]});
    assert_eq!(echoed["result"]["structuredContent"], structured);
    let text = echoed["result"]["content"][0]["text"]
        .as_str()
        .expect("text");
    assert_eq!(
        serde_json::from_str::<Value>(text).expect("JSON"),
        structured
    );
    assert!(!text.contains(' ') && echoed["result"].get("isError").is_none());
    // No arguments, and null ones, are none.
    for params in [
        json!({"name": "echo"}),
        json!({"name": "echo", "arguments": null}),
    ] {
        let reply = messages(&post(&rpc(1, "tools/call", params)));
        assert_eq!(
            reply[0]["result"],
            json!({"content": [{"type": "text", "text": "{}"}], "structuredContent": {}})
        );
    }
    // A result that is no object is text alone.
    assert_eq!(
        call("pong", json!({}))["result"],
        json!({"content": [{"type": "text", "text": "\"pong\""}]})
    );
    assert_eq!(
        call("nothing", json!({}))["result"],
        json!({"content": [{"type": "text", "text": "null"}]})
    );
    // Content of another kind is sent as it is.
    assert_eq!(
        call("export", json!({}))["result"],
        json!({"content": [
            {"type": "text", "text": "id,name\n"},
            {"type": "resource", "resource": {"uri": "/api/export", "mimeType": "application/pdf", "blob": "JVBERg=="}},
        ]})
    );
    // A refusal is a tool error carrying the problem.
    let refused = call("refused", json!({}));
    let problem = &refused["result"]["structuredContent"];
    assert_eq!(refused["result"]["isError"], true);
    assert_eq!(
        (&problem["status"], &problem["code"]),
        (&json!(409), &json!("conflict"))
    );
    let text = refused["result"]["content"][0]["text"]
        .as_str()
        .expect("text");
    assert_eq!(&serde_json::from_str::<Value>(text).expect("JSON"), problem);
}

#[test]
fn a_tool_the_set_has_not_got_and_a_call_that_is_no_call_are_json_rpc_errors() {
    assert_eq!(
        call("nowhere", json!({}))["error"],
        json!({"code": -32602, "message": "no tool nowhere"})
    );
    // A failure's message stays off the wire.
    assert_eq!(
        call("broken", json!({}))["error"],
        json!({"code": -32603, "message": "Internal error"})
    );
    for (params, message) in [
        (
            json!({"arguments": {}}),
            "Invalid params for tools/call: name must be a string",
        ),
        (
            json!({"name": 7}),
            "Invalid params for tools/call: name must be a string",
        ),
        (
            json!({"name": "echo", "arguments": [1]}),
            "Invalid params for tools/call: arguments must be an object",
        ),
        (
            json!({"name": "echo", "arguments": "a"}),
            "Invalid params for tools/call: arguments must be an object",
        ),
    ] {
        let reply = messages(&post(&rpc(1, "tools/call", params)));
        assert_eq!(
            reply[0]["error"],
            json!({"code": -32602, "message": message})
        );
    }
    let request = Request::builder()
        .method(Method::POST)
        .uri("/api/mcp")
        .header("accept", ACCEPT)
        .header("content-type", "application/json")
        .body(Bytes::from(rpc(1, "tools/list", json!({})).to_string()))
        .expect("a request");
    let reply = messages(&block_on(serve(&request, &Unlisted, &server())));
    assert_eq!(
        reply[0]["error"],
        json!({"code": -32603, "message": "Internal error"})
    );
}

#[test]
fn a_2026_request_is_one_json_body_and_its_envelope_and_headers_must_agree() {
    let modern = |method: &str, params: Value| send(&MODERN, rpc(1, method, params).to_string());
    let enveloped = |mut params: Value| {
        params["_meta"] = envelope();
        params
    };

    let listed = refusal(&modern("tools/list", enveloped(json!({}))));
    assert_eq!(listed.0, 200);
    assert_eq!(
        listed.1["result"],
        json!({
            "tools": [{
                "name": "echo",
                "title": "Echo",
                "inputSchema": {"type": "object"},
                "_meta": {"invocationPolicy": "auto"},
            }],
            "resultType": "complete",
            "ttlMs": 0,
            "cacheScope": "private",
            "_meta": server_stamp(),
        })
    );
    assert_eq!(
        (&listed.1["jsonrpc"], &listed.1["id"]),
        (&json!("2.0"), &json!(1))
    );

    let discovered = refusal(&modern("server/discover", enveloped(json!({}))));
    assert_eq!(
        discovered.1["result"],
        json!({
            "supportedVersions": ["2026-07-28"],
            "capabilities": {"tools": {"listChanged": false}},
            "resultType": "complete",
            "ttlMs": 0,
            "cacheScope": "private",
            "_meta": server_stamp(),
        })
    );
    let pinged = refusal(&modern("ping", enveloped(json!({}))));
    assert_eq!(
        pinged.1["result"],
        json!({"resultType": "complete", "_meta": server_stamp()})
    );
    let called = refusal(&modern(
        "tools/call",
        enveloped(json!({"name": "echo", "arguments": {"a": 1}})),
    ));
    assert_eq!(
        called.1["result"],
        json!({
            "content": [{"type": "text", "text": "{\"a\":1}"}],
            "structuredContent": {"a": 1},
            "resultType": "complete",
            "_meta": server_stamp(),
        })
    );
    let unknown = refusal(&modern("tools/call", enveloped(json!({"name": "nowhere"}))));
    assert_eq!(unknown.0, 200);
    assert_eq!(
        unknown.1["error"],
        json!({"code": -32602, "message": "no tool nowhere"})
    );

    // The envelope without the header, and the header without the
    // envelope.
    let absent = refusal(&send(
        &JSONRPC,
        rpc(7, "tools/list", enveloped(json!({}))).to_string(),
    ));
    assert_eq!(
        (absent.0, &absent.1["error"]["code"], &absent.1["id"]),
        (400, &json!(-32020), &json!(7))
    );
    assert_eq!(absent.1["error"]["data"]["mismatch"]["header"], "(missing)");
    let missing = refusal(&modern("tools/list", json!({})));
    assert_eq!(
        missing.1["error"],
        json!({
            "code": -32602,
            "message": "Invalid params: the MCP-Protocol-Version header names protocol revision 2026-07-28, but the request is missing the required per-request envelope key(s): _meta",
            "data": {"envelope": {"missing": ["_meta"]}},
        })
    );
    let partial =
        json!({"_meta": {"io.modelcontextprotocol/clientInfo": {"name": "t", "version": "1"}}});
    let missing = refusal(&modern("tools/list", partial));
    assert_eq!(
        missing.1["error"]["data"],
        json!({"envelope": {"missing": [
            "io.modelcontextprotocol/protocolVersion",
            "io.modelcontextprotocol/clientCapabilities",
        ]}})
    );
    let mut malformed = envelope();
    malformed["io.modelcontextprotocol/clientInfo"] = json!({"name": "t"});
    let refused = refusal(&modern("tools/list", json!({"_meta": malformed})));
    assert_eq!(
        refused.1["error"],
        json!({
            "code": -32602,
            "message": "Invalid _meta envelope for protocol revision 2026-07-28: io.modelcontextprotocol/clientInfo: expected an object with a name and a version",
            "data": {"envelope": {
                "key": "io.modelcontextprotocol/clientInfo",
                "problem": "expected an object with a name and a version",
            }},
        })
    );
    // Headers the body contradicts.
    let mut headers = MODERN.to_vec();
    headers.push(("mcp-method", "tools/call"));
    let wrong_method = refusal(&send(
        &headers,
        rpc(1, "tools/list", enveloped(json!({}))).to_string(),
    ));
    assert_eq!(
        (wrong_method.0, &wrong_method.1["error"]["code"]),
        (400, &json!(-32020))
    );
    headers[3] = ("mcp-method", "tools/list");
    assert_eq!(
        refusal(&send(
            &headers,
            rpc(1, "tools/list", enveloped(json!({}))).to_string()
        ))
        .0,
        200
    );
    let mut later = envelope();
    later["io.modelcontextprotocol/protocolVersion"] = json!("2027-01-01");
    let disagreeing = refusal(&modern("tools/list", json!({"_meta": later.clone()})));
    assert_eq!(disagreeing.1["error"]["code"], -32020);
    let legacy_handshake = refusal(&modern(
        "initialize",
        json!({"protocolVersion": "2025-11-25"}),
    ));
    assert_eq!(legacy_handshake.1["error"]["code"], -32020);
    // A revision it does not speak, and a method the era does not define.
    let future = [
        ("accept", ACCEPT),
        ("content-type", "application/json"),
        ("mcp-protocol-version", "2027-01-01"),
    ];
    let unsupported = refusal(&send(
        &future,
        rpc(3, "tools/list", json!({"_meta": later})).to_string(),
    ));
    assert_eq!(unsupported.0, 400);
    assert_eq!(
        unsupported.1,
        json!({
            "jsonrpc": "2.0",
            "error": {
                "code": -32022,
                "message": "Unsupported protocol version: 2027-01-01",
                "data": {"supported": ["2026-07-28"], "requested": "2027-01-01"},
            },
            "id": 3,
        })
    );
    let undefined = refusal(&modern("initialize", enveloped(json!({}))));
    assert_eq!(undefined.0, 404);
    assert_eq!(
        undefined.1,
        json!({"jsonrpc": "2.0", "id": 1, "error": {"code": -32601, "message": "Method not found"}})
    );
    // A notification is taken, and a batch may hold no 2026 request.
    let notified = send(
        &MODERN,
        json!({"jsonrpc": "2.0", "method": "notifications/cancelled"}).to_string(),
    );
    assert_eq!(notified.status(), StatusCode::ACCEPTED);
    assert!(notified.body().is_empty());
    assert_eq!(
        refusal(&post(&json!([rpc(1, "tools/list", enveloped(json!({})))]))),
        entry_error(
            400,
            -32600,
            "Bad Request: JSON-RPC batches may not contain requests for protocol revision 2026-07-28 or later"
        )
    );
}

#[test]
fn serve_is_a_future_a_server_of_many_threads_may_await() {
    fn is_send<T: Send>(_: &T) {}
    let request = Request::builder()
        .method(Method::POST)
        .uri("/api/mcp")
        .body(Bytes::new())
        .expect("a request");
    let options = server();
    let future = serve(&request, &Named, &options);
    is_send(&future);
    assert_eq!(
        block_on(future).status(),
        StatusCode::UNSUPPORTED_MEDIA_TYPE
    );
}
