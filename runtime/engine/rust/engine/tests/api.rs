//! The transport-neutral HTTP API (`engine::api`, D58) driven directly, as
//! a host drives it, on SQLite and, with the variable set, on Postgres:
//! what [`routes`] lists, the caller rules, a write's envelope and headers,
//! the event stream a host pulls (its open comment, replay and `ready`, a
//! heartbeat, a commit waking it, its end on close and on shutdown), and
//! the MCP endpoint's JSON-RPC on both protocol eras and its refusals.
//!
//! The recorded exchanges of milestone 1's corpus replay through the same
//! [`handle`] in the test kit (`http_replay.rs`); these are the cases the
//! corpus cannot hold: pull timeouts, a stream ended by the host or by the
//! engine, and malformed MCP messages.

mod support;

use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::api::{
    handle, principal_of, route_of, routes, ApiOptions, Body, Caller, EventStream, McpOptions,
    Pull, Refusal, Request, Response, ServiceCaller, StreamOptions,
};
use superschematic_engine::{CreateOptions, DefineOptions, Engine, Principal, SchemaTarget};
use support::{alice, order_document, targets};

fn engine(target: &support::Target) -> Arc<Engine> {
    Engine::open_shared(target.listening_options()).expect("open the engine")
}

fn published(engine: &Engine) {
    engine
        .schemas()
        .define(
            &alice(),
            order_document(),
            DefineOptions {
                namespace: None,
                source: None,
            },
        )
        .unwrap();
    engine
        .schemas()
        .publish(&alice(), "Order", SchemaTarget { namespace: None })
        .unwrap();
}

fn create(engine: &Engine, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Order",
            &json!({"title": id}),
            CreateOptions {
                id: Some(id),
                ..CreateOptions::default()
            },
        )
        .unwrap();
}

fn request(method: &str, path: &str) -> Request {
    Request {
        method: method.to_owned(),
        prefix: "/api".to_owned(),
        path: format!("/api{path}"),
        caller: Caller {
            principal: Some(alice()),
            ..Caller::default()
        },
        request_id: "r1".to_owned(),
        ..Request::default()
    }
}

fn with_body(mut request: Request, body: &Value) -> Request {
    request
        .headers
        .push(("content-type".to_owned(), "application/json".to_owned()));
    request.body = Some(body.to_string().into_bytes());
    request
}

fn json_of(response: &Response) -> Value {
    serde_json::from_slice(response.bytes()).expect("a JSON body")
}

fn stream_of(response: Response) -> Box<EventStream> {
    match response.body {
        Body::Stream(stream) => stream,
        other => panic!("expected a stream, got {other:?}"),
    }
}

// pull_text pulls a stream until it has sent something, within a deadline.
fn pull_text(stream: &mut EventStream) -> String {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match stream.next(Duration::from_millis(100)) {
            Pull::Data(bytes) => return String::from_utf8(bytes).unwrap(),
            Pull::Pending if Instant::now() < deadline => {}
            other => panic!("expected data, got {other:?}"),
        }
    }
}

#[test]
fn routes_lists_the_27_routes_and_the_mcp_endpoint_and_route_of_finds_one() {
    let all = routes();
    assert_eq!(all.len(), 30);
    assert_eq!(all.iter().filter(|route| route.mcp).count(), 3);
    let streaming: Vec<&str> = all
        .iter()
        .filter(|route| route.stream)
        .map(|route| route.name)
        .collect();
    assert_eq!(streaming, ["readEvents"]);
    assert_eq!(
        route_of("post", "/namespaces/default/schemas/Order/instances").map(|route| route.name),
        Some("createInstance")
    );
    assert_eq!(route_of("PATCH", "/namespaces"), None);
}

#[test]
fn the_engine_answers_401_without_a_caller_and_the_hosts_refusal_in_its_place() {
    for target in targets("api_callers") {
        let engine = engine(&target);
        let options = ApiOptions::default();
        let mut anonymous = request("GET", "/namespaces");
        anonymous.caller = Caller::default();
        let answered = handle(&engine, &options, &anonymous);
        assert_eq!(answered.status, 401);
        assert_eq!(answered.header("x-request-id"), Some("r1"));
        assert_eq!(json_of(&answered)["code"], "unauthorized");

        let mut refused = request("GET", "/namespaces");
        refused.caller.refused = Some(Refusal {
            status: 403,
            code: "service_forbidden".to_owned(),
            detail: None,
        });
        let answered = handle(&engine, &options, &refused);
        assert_eq!(answered.status, 403);
        assert_eq!(json_of(&answered)["detail"], "Service not permitted");

        // Outside the prefix, and under it naming no route: 404.
        let mut outside = request("GET", "/namespaces");
        outside.path = "/other/namespaces".to_owned();
        assert_eq!(handle(&engine, &options, &outside).status, 404);
        assert_eq!(
            handle(&engine, &options, &request("GET", "/nothing")).status,
            404
        );
    }
}

#[test]
fn a_service_with_no_end_user_stands_in_for_one_unless_the_request_carries_authorization() {
    let service = ServiceCaller {
        deployable: "worker".to_owned(),
        serves: vec![],
        subject: "sa".to_owned(),
    };
    let mut alone = request("GET", "/namespaces");
    alone.caller = Caller {
        principal: None,
        service: Some(service.clone()),
        refused: None,
    };
    let principal = principal_of(&alone).expect("the service stands in");
    assert_eq!(principal.subject, "service:worker");
    assert!(principal
        .service
        .as_ref()
        .is_some_and(|service| service.stands_in));

    alone
        .headers
        .push(("authorization".to_owned(), "Bearer nobody".to_owned()));
    assert_eq!(principal_of(&alone), None);

    let mut acting = request("GET", "/namespaces");
    acting.caller.service = Some(service);
    let principal = principal_of(&acting).unwrap();
    assert_eq!(principal.subject, "alice");
    assert!(principal
        .service
        .as_ref()
        .is_some_and(|service| !service.stands_in));

    let mut nobody = request("GET", "/namespaces");
    nobody.caller.principal = Some(Principal::new("", Vec::<String>::new()));
    assert_eq!(principal_of(&nobody), None);
}

#[test]
fn a_create_answers_201_with_the_envelope_its_etag_and_a_location_under_the_prefix() {
    for target in targets("api_create") {
        let engine = engine(&target);
        published(&engine);
        let created = handle(
            &engine,
            &ApiOptions::default(),
            &with_body(
                request("POST", "/namespaces/default/schemas/Order/instances"),
                &json!({"id": "o:1", "data": {"title": "Desk"}}),
            ),
        );
        assert_eq!(created.status, 201, "{:?}", json_of(&created));
        assert_eq!(created.header("etag"), Some("\"1\""));
        assert_eq!(
            created.header("location"),
            Some("/api/namespaces/default/schemas/Order/instances/o%3A1")
        );
        let body = json_of(&created);
        assert_eq!(body["data"]["id"], "o:1");
        assert_eq!(body["meta"], json!({"requestId": "r1"}));

        // A JSON body sent as another media type is refused.
        let mut text = request("POST", "/namespaces/default/schemas/Order/instances");
        text.headers
            .push(("content-type".to_owned(), "text/plain".to_owned()));
        text.body = Some(br#"{"data": {"title": "Desk"}}"#.to_vec());
        assert_eq!(handle(&engine, &ApiOptions::default(), &text).status, 415);
    }
}

#[test]
fn an_event_stream_replays_says_ready_beats_wakes_for_a_commit_and_ends_when_closed() {
    for target in targets("api_stream") {
        let engine = engine(&target);
        published(&engine);
        create(&engine, "o1");
        let options = ApiOptions {
            stream: StreamOptions {
                page_size: Some(1),
                heartbeat_ms: Some(50),
            },
            ..ApiOptions::default()
        };
        let mut read = request("GET", "/namespaces/default/events");
        read.headers
            .push(("accept".to_owned(), "text/event-stream".to_owned()));
        let answered = handle(&engine, &options, &read);
        assert_eq!(answered.status, 200);
        assert_eq!(answered.header("content-type"), Some("text/event-stream"));
        let mut stream = stream_of(answered);
        assert_eq!(pull_text(&mut stream), ": open\n\n");
        // A page of one at a time: the define, the publish, the create,
        // then ready.
        let mut replay = String::new();
        while !replay.contains("event: ready") {
            replay.push_str(&pull_text(&mut stream));
        }
        assert_eq!(replay.matches("data: {\"cursor\":").count(), 4, "{replay}");
        let head = stream.resume_cursor();
        assert_eq!(head, 3);

        // Caught up: a short pull is Pending, then the heartbeat comes.
        assert_eq!(stream.next(Duration::from_millis(1)), Pull::Pending);
        assert_eq!(pull_text(&mut stream), ": keepalive\n\n");

        // A commit wakes it, from another thread.
        let writer = Arc::clone(&engine);
        let wrote = thread::spawn(move || {
            thread::sleep(Duration::from_millis(20));
            create(&writer, "o2");
        });
        let mut woke = pull_text(&mut stream);
        while woke == ": keepalive\n\n" {
            woke = pull_text(&mut stream);
        }
        wrote.join().unwrap();
        assert!(woke.starts_with("id: 4\ndata: "), "{woke}");
        assert!(woke.contains("\"instanceId\":\"o2\""), "{woke}");
        assert_eq!(stream.resume_cursor(), 4);

        stream.close();
        assert_eq!(stream.next(Duration::from_millis(10)), Pull::End);
        assert!(stream.notifier().is_none());
    }
}

#[test]
fn an_event_stream_ends_when_the_engine_shuts_down_and_a_refused_read_is_a_problem() {
    for target in targets("api_stream_end") {
        let engine = engine(&target);
        let mut read = request("GET", "/namespaces/default/events");
        read.headers
            .push(("accept".to_owned(), "text/event-stream".to_owned()));
        let mut stream = stream_of(handle(&engine, &ApiOptions::default(), &read));
        assert_eq!(pull_text(&mut stream), ": open\n\n");
        assert!(pull_text(&mut stream).starts_with("event: ready\nid: 0\n"));
        let waiter = thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                match stream.next(Duration::from_millis(50)) {
                    Pull::End => return true,
                    _ if Instant::now() > deadline => return false,
                    _ => {}
                }
            }
        });
        thread::sleep(Duration::from_millis(50));
        engine.shutdown();
        assert!(waiter.join().unwrap(), "the stream ended at the shutdown");
        // A shut-down engine answers every request 503: the host drains.
        let after = handle(
            &engine,
            &ApiOptions::default(),
            &request("GET", "/namespaces"),
        );
        assert_eq!(after.status, 503);
        assert_eq!(json_of(&after)["code"], "unavailable");

        // A namespace the engine does not have refuses the read before any
        // stream.
        let other = Engine::open_shared(target.options()).unwrap();
        let mut unknown = request("GET", "/namespaces/nowhere/events");
        unknown
            .headers
            .push(("accept".to_owned(), "text/event-stream".to_owned()));
        let answered = handle(&other, &ApiOptions::default(), &unknown);
        assert_eq!(answered.status, 404);
        assert_eq!(json_of(&answered)["code"], "unknown_namespace");
    }
}

fn mcp_post(body: &Value, version: Option<&str>) -> Request {
    let mut post = with_body(request("POST", "/namespaces/default/mcp"), body);
    post.headers.push((
        "accept".to_owned(),
        "application/json, text/event-stream".to_owned(),
    ));
    if let Some(version) = version {
        post.headers
            .push(("mcp-protocol-version".to_owned(), version.to_owned()));
    }
    post
}

// messages reads the JSON-RPC messages of a whole MCP event stream.
fn messages(response: &Response) -> Vec<Value> {
    let text = String::from_utf8(response.bytes().to_vec()).unwrap();
    text.split("\n\n")
        .filter_map(|block| {
            block
                .lines()
                .find_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str(data).unwrap())
        })
        .collect()
}

fn envelope() -> Value {
    json!({
        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
        "io.modelcontextprotocol/clientInfo": {"name": "test", "version": "1"},
        "io.modelcontextprotocol/clientCapabilities": {},
    })
}

#[test]
fn mcp_answers_the_2025_revisions_as_an_event_stream_and_2026_as_json() {
    for target in targets("api_mcp") {
        let engine = engine(&target);
        published(&engine);
        create(&engine, "o1");
        let options = ApiOptions {
            mcp: McpOptions {
                server_name: Some("shop".to_owned()),
                server_version: Some("2.0.0".to_owned()),
                instructions: Some("Orders.".to_owned()),
            },
            ..ApiOptions::default()
        };
        let initialized = handle(
            &engine,
            &options,
            &mcp_post(
                &json!({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "test", "version": "1"},
                }}),
                None,
            ),
        );
        assert_eq!(initialized.status, 200);
        assert_eq!(
            initialized.header("content-type"),
            Some("text/event-stream")
        );
        assert_eq!(
            messages(&initialized),
            [json!({"result": {
                "protocolVersion": "2025-06-18",
                "capabilities": {"tools": {"listChanged": false}},
                "serverInfo": {"name": "shop", "version": "2.0.0"},
                "instructions": "Orders.",
            }, "jsonrpc": "2.0", "id": 0})]
        );

        // A notification alone is 202; a batch answers each request in
        // order, an unknown method with its error.
        let notified = handle(
            &engine,
            &options,
            &mcp_post(
                &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
                Some("2025-06-18"),
            ),
        );
        assert_eq!((notified.status, notified.bytes().len()), (202, 0));
        let batch = handle(
            &engine,
            &options,
            &mcp_post(
                &json!([
                    {"jsonrpc": "2.0", "id": 1, "method": "ping"},
                    {"jsonrpc": "2.0", "method": "notifications/progress"},
                    {"jsonrpc": "2.0", "id": "two", "method": "prompts/list"},
                    {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "order_get", "arguments": {"id": "o1"}}},
                ]),
                Some("2025-06-18"),
            ),
        );
        let replies = messages(&batch);
        assert_eq!(replies.len(), 3);
        assert_eq!(replies[0], json!({"result": {}, "jsonrpc": "2.0", "id": 1}));
        assert_eq!(
            replies[1],
            json!({"jsonrpc": "2.0", "id": "two", "error": {"code": -32601, "message": "Method not found"}})
        );
        assert_eq!(replies[2]["result"]["structuredContent"]["id"], "o1");

        // 2026-07-28: one JSON body, marked complete and stamped with the
        // server's identity.
        let listed = handle(
            &engine,
            &options,
            &mcp_post(
                &json!({"jsonrpc": "2.0", "id": 4, "method": "tools/list", "params": {"_meta": envelope()}}),
                Some("2026-07-28"),
            ),
        );
        assert_eq!(listed.status, 200);
        assert_eq!(listed.header("content-type"), Some("application/json"));
        let result = &json_of(&listed)["result"];
        assert_eq!(result["resultType"], "complete");
        assert_eq!(result["cacheScope"], "private");
        assert_eq!(
            result["_meta"]["io.modelcontextprotocol/serverInfo"],
            json!({"name": "shop", "version": "2.0.0"})
        );
        assert!(result["tools"]
            .as_array()
            .unwrap()
            .iter()
            .any(|tool| tool["name"] == "order_get"));
        let unknown = handle(
            &engine,
            &options,
            &mcp_post(
                &json!({"jsonrpc": "2.0", "id": 5, "method": "initialize", "params": {"_meta": envelope()}}),
                Some("2026-07-28"),
            ),
        );
        assert_eq!(unknown.status, 404);
        assert_eq!(json_of(&unknown)["error"]["code"], -32601);
    }
}

#[test]
fn mcp_refuses_what_the_transport_refuses_with_its_status_and_json_rpc_error() {
    for target in targets("api_mcp_refusals") {
        let engine = engine(&target);
        let options = ApiOptions::default();
        let error = |response: &Response| {
            let body = json_of(response);
            (response.status, body["error"]["code"].as_i64().unwrap())
        };
        let get = request("GET", "/namespaces/default/mcp");
        assert_eq!(error(&handle(&engine, &options, &get)), (405, -32000));
        let delete = request("DELETE", "/namespaces/default/mcp");
        assert_eq!(error(&handle(&engine, &options, &delete)), (405, -32000));

        let ping = json!({"jsonrpc": "2.0", "id": 1, "method": "ping"});
        let mut text = mcp_post(&ping, None);
        text.headers.retain(|(name, _)| name != "content-type");
        text.headers
            .push(("content-type".to_owned(), "text/plain".to_owned()));
        assert_eq!(error(&handle(&engine, &options, &text)), (415, -32000));

        let mut no_stream = mcp_post(&ping, None);
        no_stream.headers.retain(|(name, _)| name != "accept");
        no_stream
            .headers
            .push(("accept".to_owned(), "application/json".to_owned()));
        assert_eq!(error(&handle(&engine, &options, &no_stream)), (406, -32000));

        let mut garbled = mcp_post(&ping, None);
        garbled.body = Some(b"{not json".to_vec());
        assert_eq!(error(&handle(&engine, &options, &garbled)), (400, -32700));

        let old = mcp_post(&ping, Some("2023-01-01"));
        assert_eq!(error(&handle(&engine, &options, &old)), (400, -32000));
        let modern_without_envelope = mcp_post(&ping, Some("2026-07-28"));
        assert_eq!(
            error(&handle(&engine, &options, &modern_without_envelope)),
            (400, -32602)
        );
        let later = mcp_post(
            &json!({"jsonrpc": "2.0", "id": 1, "method": "ping", "params": {"_meta": {
                "io.modelcontextprotocol/protocolVersion": "2027-01-01",
                "io.modelcontextprotocol/clientInfo": {"name": "test", "version": "1"},
                "io.modelcontextprotocol/clientCapabilities": {},
            }}}),
            Some("2027-01-01"),
        );
        let refused = handle(&engine, &options, &later);
        assert_eq!(error(&refused), (400, -32022));
        assert_eq!(json_of(&refused)["id"], 1);

        // The engine's own refusals come first: no caller, no namespace.
        let mut anonymous = mcp_post(&ping, None);
        anonymous.caller = Caller::default();
        assert_eq!(handle(&engine, &options, &anonymous).status, 401);
        let mut nowhere = mcp_post(&ping, None);
        nowhere.path = "/api/namespaces/nowhere/mcp".to_owned();
        assert_eq!(handle(&engine, &options, &nowhere).status, 404);
    }
}
