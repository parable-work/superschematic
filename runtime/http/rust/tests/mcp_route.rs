//! A generated router's tools (D81, D116): the tool table as the build
//! writes it, the request a call makes to its operation's route, a filter
//! argument as the bracket form (D106, D112), and what a route's response
//! comes to. The cases are those the TypeScript runtime's `mcp.test.ts`
//! holds its router to, here without a router: the request is read where
//! there it is answered.

use bytes::Bytes;
use http::{Method, Request, Response};
use serde_json::{json, Map, Value};
use std::path::Path;
use superschematic_http_runtime::mcp::{
    caller_of, outcome_of, problem_of, tool_request, with_confirmation_argument, ArgumentLocation,
    CallOutcome, Content, RouteTool, CONFIRMATION_ARGUMENT,
};

/// A tool table the TypeScript API generator wrote, whose JSON a Rust
/// API's is.
const GOLDEN: &str = "../../../internal/generator/tsrestgen/testdata/golden/fixture-mcp/mcp.ts";

fn object(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// A tool of `operation` that takes `arguments`, each `(key, in, name)`.
fn tool(name: &str, arguments: &[(&str, &str, &str)]) -> RouteTool {
    let arguments: Vec<Value> = arguments
        .iter()
        .map(|(key, location, name)| json!({"key": key, "in": location, "name": name}))
        .collect();
    serde_json::from_value(json!({
        "name": name,
        "title": name,
        "description": format!("The {name} tool."),
        "inputSchema": {"type": "object"},
        "annotations": {"readOnlyHint": true},
        "_meta": {"invocationPolicy": "auto"},
        "operation": name,
        "arguments": arguments,
    }))
    .expect("a tool")
}

/// The endpoint's own request, whose headers a call carries.
fn incoming() -> Request<Bytes> {
    Request::builder()
        .method(Method::POST)
        .uri("/api/mcp")
        .header("accept", "application/json, text/event-stream")
        .header("authorization", "Bearer ada")
        .header("content-length", "120")
        .header("content-type", "application/json")
        .header("cookie", "a=1")
        .header("cookie", "b=2")
        .header("host", "api.test")
        .header("if-match", "\"3\"")
        .header("mcp-method", "tools/call")
        .header("mcp-protocol-version", "2025-11-25")
        .header("mcp-session-id", "s1")
        .header("x-api-key", "k1")
        .header("x-request-id", "from-the-client")
        .body(Bytes::new())
        .expect("a request")
}

fn request(
    tool: &RouteTool,
    method: Method,
    path: &str,
    arguments: Value,
) -> Result<Request<Bytes>, Value> {
    tool_request(
        tool,
        &method,
        path,
        &object(arguments),
        &incoming(),
        "req-7",
    )
}

fn header<'a>(request: &'a Request<Bytes>, name: &str) -> Option<&'a str> {
    request
        .headers()
        .get(name)
        .and_then(|value| value.to_str().ok())
}

/// The keys and values of a request's query, decoded, in order.
fn query(request: &Request<Bytes>) -> Vec<(String, String)> {
    let decode = |text: &str| {
        let mut bytes = Vec::new();
        let mut rest = text.bytes();
        while let Some(byte) = rest.next() {
            match byte {
                b'+' => bytes.push(b' '),
                b'%' => {
                    let hex: Vec<u8> = rest.by_ref().take(2).collect();
                    let hex = std::str::from_utf8(&hex).expect("two hex digits");
                    bytes.push(u8::from_str_radix(hex, 16).expect("two hex digits"));
                }
                other => bytes.push(other),
            }
        }
        String::from_utf8(bytes).expect("UTF-8")
    };
    request
        .uri()
        .query()
        .into_iter()
        .flat_map(|query| query.split('&'))
        .map(|pair| {
            let (key, value) = pair.split_once('=').expect("a key and a value");
            (decode(key), decode(value))
        })
        .collect()
}

fn pairs(pairs: &[(&str, &str)]) -> Vec<(String, String)> {
    let mut pairs: Vec<(String, String)> = pairs
        .iter()
        .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
        .collect();
    pairs.sort();
    pairs
}

fn sorted(mut pairs: Vec<(String, String)>) -> Vec<(String, String)> {
    pairs.sort();
    pairs
}

fn detail_of(refusal: Result<Request<Bytes>, Value>) -> String {
    let problem = refusal.expect_err("a refusal");
    assert_eq!(problem["status"], 400, "{problem}");
    assert_eq!(problem["title"], "Bad Request");
    assert_eq!(problem["code"], "bad_request");
    assert_eq!(problem["requestId"], "req-7");
    problem["detail"].as_str().expect("a detail").to_owned()
}

#[test]
fn the_tool_table_the_build_writes_reads_and_writes_back_the_same() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(GOLDEN);
    let source = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    let (_, table) = source
        .split_once("mcpTools: readonly McpRouteTool[] = ")
        .expect("the tool table");
    let table: Value = serde_json::from_str(table.trim_end().trim_end_matches(';'))
        .expect("the tool table is JSON");
    let tools: Vec<RouteTool> = serde_json::from_value(table.clone()).expect("route tools");
    assert!(tools.len() >= 3, "{} tools", tools.len());
    assert_eq!(serde_json::to_value(&tools).expect("JSON"), table);
    assert!(tools.iter().any(|tool| tool.confirm));
    assert!(tools.iter().any(|tool| !tool.confirm));
    for location in [
        ArgumentLocation::Path,
        ArgumentLocation::Query,
        ArgumentLocation::Body,
    ] {
        assert!(
            tools
                .iter()
                .flat_map(|tool| &tool.arguments)
                .any(|argument| argument.location == location),
            "no argument goes in {location:?}"
        );
    }
    // A tool that asks is listed with its confirmation argument, and any
    // other as the table holds it.
    for tool in &tools {
        let listed = tool.listed();
        let token = &listed.input_schema["properties"][CONFIRMATION_ARGUMENT];
        assert_eq!(token.is_object(), tool.confirm, "{}", tool.tool.name);
        if !tool.confirm {
            assert_eq!(listed, tool.tool);
        }
    }
}

#[test]
fn a_call_is_a_request_to_its_route_with_the_arguments_where_it_reads_them() {
    let get = tool("get_item", &[("id", "path", "id"), ("tag", "query", "tag")]);
    let sent = request(
        &get,
        Method::GET,
        "/api/items/{id}",
        json!({"id": "a b/c", "tag": ["x", "y z", null, 7, true]}),
    )
    .expect("a request");
    assert_eq!(sent.method(), Method::GET);
    assert_eq!(sent.uri().path(), "/api/items/a%20b%2Fc");
    assert_eq!(
        sent.uri().query(),
        Some("tag=x&tag=y+z&tag=null&tag=7&tag=true")
    );
    assert!(sent.body().is_empty());

    let rename = tool(
        "rename_item",
        &[
            ("id", "path", "id"),
            ("newName", "body", "new_name"),
            ("note", "body", "note"),
        ],
    );
    let sent = request(
        &rename,
        Method::POST,
        "/api/items/{id}/rename",
        json!({"id": 7, "newName": "Desk", "note": null}),
    )
    .expect("a request");
    assert_eq!(sent.method(), Method::POST);
    assert_eq!(
        sent.uri().path_and_query().map(|path| path.as_str()),
        Some("/api/items/7/rename")
    );
    let body: Value = serde_json::from_slice(sent.body()).expect("a JSON body");
    assert_eq!(body, json!({"new_name": "Desk", "note": null}));
    assert_eq!(header(&sent, "content-type"), Some("application/json"));
    // A member left out is left out, and a call with none has no body.
    let sent = request(
        &rename,
        Method::POST,
        "/api/items/{id}/rename",
        json!({"id": "i1"}),
    )
    .expect("a request");
    assert!(sent.body().is_empty() && header(&sent, "content-type").is_none());

    // A value that is no text is its text, and a list or an object its
    // JSON.
    let wide = tool(
        "wide",
        &[("a", "path", "a"), ("b", "path", "b"), ("q", "query", "q")],
    );
    let sent = request(
        &wide,
        Method::GET,
        "/api/{a}/{b}/{a}",
        json!({"a": 1.5, "b": false, "q": {"k": [1, "\u{e9}"]}}),
    )
    .expect("a request");
    assert_eq!(sent.uri().path(), "/api/1.5/false/1.5");
    assert_eq!(query(&sent), pairs(&[("q", "{\"k\":[1,\"\u{e9}\"]}")]));
}

#[test]
fn a_call_carries_the_endpoint_requests_headers_but_the_transports_and_its_request_id() {
    let ping = tool("ping", &[]);
    let sent = request(&ping, Method::GET, "/api/ping", json!({})).expect("a request");
    assert_eq!(
        sent.uri().path_and_query().map(|path| path.as_str()),
        Some("/api/ping")
    );
    assert_eq!(header(&sent, "authorization"), Some("Bearer ada"));
    assert_eq!(header(&sent, "x-api-key"), Some("k1"));
    assert_eq!(header(&sent, "host"), Some("api.test"));
    let cookies: Vec<_> = sent.headers().get_all("cookie").iter().collect();
    assert_eq!(cookies, ["a=1", "b=2"]);
    assert_eq!(header(&sent, "accept"), Some("application/json"));
    assert_eq!(header(&sent, "x-request-id"), Some("req-7"));
    for dropped in [
        "content-length",
        "content-type",
        "if-match",
        "mcp-method",
        "mcp-protocol-version",
        "mcp-session-id",
    ] {
        assert!(sent.headers().get(dropped).is_none(), "{dropped} went on");
    }
    assert_eq!(sent.headers().len(), 7);

    // The scheme and authority of the endpoint's request are the call's.
    let absolute = Request::builder()
        .uri("https://api.test:8443/api/mcp?x=1")
        .body(())
        .expect("a request");
    let sent = tool_request(
        &ping,
        &Method::GET,
        "/api/ping",
        &Map::new(),
        &absolute,
        "req-8",
    )
    .expect("a request");
    assert_eq!(sent.uri().to_string(), "https://api.test:8443/api/ping");
    assert_eq!(header(&sent, "x-request-id"), Some("req-8"));
}

#[test]
fn arguments_the_tool_does_not_take_and_a_missing_path_argument_are_refused() {
    let get = tool("get_item", &[("id", "path", "id"), ("tag", "query", "tag")]);
    let path = "/api/items/{id}";
    assert_eq!(
        detail_of(request(
            &get,
            Method::GET,
            path,
            json!({"id": "i1", "extra": 1})
        )),
        r#"get_item takes id, tag, not "extra""#
    );
    assert_eq!(
        detail_of(request(
            &tool("ping", &[]),
            Method::GET,
            "/api/ping",
            json!({"x": 1})
        )),
        r#"ping takes no arguments, not "x""#
    );
    for arguments in [json!({}), json!({"id": null}), json!({"tag": "x"})] {
        assert_eq!(
            detail_of(request(&get, Method::GET, path, arguments)),
            "get_item: id is required"
        );
    }
}

/// The query of a call of a tool that takes a tag and the filter as
/// `where`.
fn filtered(arguments: Value) -> Result<Request<Bytes>, Value> {
    let list = tool(
        "list_parts",
        &[("tag", "query", "tag"), ("where", "filter", "filter")],
    );
    request(&list, Method::GET, "/api/parts", arguments)
}

#[test]
fn a_filter_reaches_the_route_as_the_bracket_form() {
    // A value is equality, an object its operators, an in joined by
    // commas.
    let sent = filtered(json!({
        "tag": "steel",
        "where": {
            "name": "Hex bolt",
            "quantity": {"gte": 2, "lt": 10, "in": [3, 4]},
            "discontinued": false,
            "retiredOn": {"isNull": true},
        },
    }))
    .expect("a request");
    assert_eq!(sent.uri().path(), "/api/parts");
    assert_eq!(
        sorted(query(&sent)),
        pairs(&[
            ("tag", "steel"),
            ("filter[name]", "Hex bolt"),
            ("filter[quantity][gte]", "2"),
            ("filter[quantity][lt]", "10"),
            ("filter[quantity][in]", "3,4"),
            ("filter[discontinued]", "false"),
            ("filter[retiredOn][isNull]", "true"),
        ])
    );
    assert_eq!(query(&sent)[0], ("tag".to_owned(), "steel".to_owned()));
    // The brackets and what a value holds are escaped.
    let raw = sent.uri().query().expect("a query");
    assert!(raw.contains("filter%5Bname%5D=Hex+bolt"), "{raw}");

    let sent = filtered(json!({
        "where": {"name": {"contains": "a&b=c d", "eq": "x, y "}, "quantity": {"eq": 7}},
    }))
    .expect("a request");
    assert_eq!(
        sorted(query(&sent)),
        pairs(&[
            ("filter[name][contains]", "a&b=c d"),
            ("filter[name][eq]", "x, y "),
            ("filter[quantity][eq]", "7"),
        ])
    );

    // An in of one value, of 100, and of values with a space inside.
    let hundred: Vec<u32> = (0..100).collect();
    let joined: Vec<String> = hundred.iter().map(u32::to_string).collect();
    let sent = filtered(json!({"where": {"quantity": {"in": hundred}, "name": {"in": "Hex nut"}}}))
        .expect("a request");
    assert_eq!(
        sorted(query(&sent)),
        pairs(&[
            ("filter[name][in]", "Hex nut"),
            ("filter[quantity][in]", &joined.join(",")),
        ])
    );

    // ne is a value and notIn a list, as eq and in are (D112).
    let sent = filtered(json!({
        "where": {
            "name": {"ne": "x, y ", "notIn": ["Hex nut", "Wing nut"]},
            "quantity": {"notIn": 7, "ne": 3},
        },
    }))
    .expect("a request");
    assert_eq!(
        sorted(query(&sent)),
        pairs(&[
            ("filter[name][ne]", "x, y "),
            ("filter[name][notIn]", "Hex nut,Wing nut"),
            ("filter[quantity][notIn]", "7"),
            ("filter[quantity][ne]", "3"),
        ])
    );

    // A field or an operator the operation does not take, and an in of no
    // value, go to the route, whose reader refuses them.
    let sent =
        filtered(json!({"where": {"secret": 1, "name": {"gt": "a"}, "quantity": {"in": []}}}))
            .expect("a request");
    assert_eq!(
        sorted(query(&sent)),
        pairs(&[
            ("filter[secret]", "1"),
            ("filter[name][gt]", "a"),
            ("filter[quantity][in]", ""),
        ])
    );
}

#[test]
fn absent_null_or_empty_the_route_reads_no_filter() {
    for arguments in [
        json!({}),
        json!({"where": null}),
        json!({"where": {}}),
        json!({"where": {"quantity": {}}}),
    ] {
        let sent = filtered(arguments.clone()).expect("a request");
        assert_eq!(
            sent.uri().path_and_query().map(|path| path.as_str()),
            Some("/api/parts"),
            "{arguments}"
        );
    }
}

#[test]
fn a_filter_with_no_bracket_form_is_a_refusal_that_names_the_member() {
    let hundred_and_one: Vec<u32> = (0..101).collect();
    for (filter, detail) in [
        (json!("name=x"), "list_parts: where is an object with a member per field"),
        (json!(["name"]), "list_parts: where is an object with a member per field"),
        (
            json!({"retiredOn": null}),
            "list_parts: where.retiredOn is a value, for equality, or an object of operators",
        ),
        (
            json!({"name": ["a", "b"]}),
            "list_parts: where.name is a value, for equality, or an object of operators",
        ),
        (json!({"quantity": {"gte": null}}), "list_parts: where.quantity.gte is a value"),
        (json!({"quantity": {"gte": [1]}}), "list_parts: where.quantity.gte is a value"),
        (
            json!({"quantity": {"in": [1, [2]]}}),
            "list_parts: where.quantity.in is a list of values",
        ),
        (json!({"name": {"in": {"a": 1}}}), "list_parts: where.name.in is a list of values"),
        (json!({"name": {"in": null}}), "list_parts: where.name.in is a list of values"),
        // What the bracket form would read as another filter than the
        // caller sent.
        (
            json!({"name": {"in": ["Hex nut", "Smith, John"]}}),
            "list_parts: where.name.in[1] must not be blank or hold a comma or a space at either end",
        ),
        (
            json!({"name": {"in": [""]}}),
            "list_parts: where.name.in[0] must not be blank or hold a comma or a space at either end",
        ),
        (
            json!({"name": {"in": ["a", "b", " c"]}}),
            "list_parts: where.name.in[2] must not be blank or hold a comma or a space at either end",
        ),
        (
            json!({"name": {"in": "c\u{85}"}}),
            "list_parts: where.name.in[0] must not be blank or hold a comma or a space at either end",
        ),
        (
            json!({"quantity": {"in": hundred_and_one}}),
            "list_parts: where.quantity.in holds more than 100 values",
        ),
        (json!({"name": {"contains": ""}}), "list_parts: where.name.contains must not be empty"),
        (json!({"name": {"ne": ["a"]}}), "list_parts: where.name.ne is a value"),
        (
            json!({"name": {"notIn": {"a": 1}}}),
            "list_parts: where.name.notIn is a list of values",
        ),
        (
            json!({"name": {"notIn": ["Hex nut", "Smith, John"]}}),
            "list_parts: where.name.notIn[1] must not be blank or hold a comma or a space at either end",
        ),
        (
            json!({"quantity": {"notIn": hundred_and_one}}),
            "list_parts: where.quantity.notIn holds more than 100 values",
        ),
    ] {
        assert_eq!(detail_of(filtered(json!({"where": filter}))), detail, "{filter}");
    }
}

fn response(status: u16, content_type: Option<&str>, body: impl Into<Bytes>) -> Response<Bytes> {
    let mut response = Response::builder().status(status);
    if let Some(content_type) = content_type {
        response = response.header("content-type", content_type);
    }
    response.body(body.into()).expect("a response")
}

fn outcome(status: u16, content_type: Option<&str>, body: impl Into<Bytes>) -> CallOutcome {
    outcome_of(
        &response(status, content_type, body),
        "req-7",
        "/api/export",
    )
}

fn text(text: &str) -> CallOutcome {
    CallOutcome::Content(vec![Content::Text {
        text: text.to_owned(),
    }])
}

#[test]
fn a_success_is_its_envelopes_data_a_json_body_as_it_is_and_any_other_body_its_content() {
    let json = Some("application/json");
    assert_eq!(
        outcome(
            200,
            json,
            r#"{"data":{"id":"i1"},"meta":{"requestId":"r"}}"#
        ),
        CallOutcome::Result(json!({"id": "i1"}))
    );
    assert_eq!(
        outcome(
            201,
            Some("application/json; charset=utf-8"),
            r#"{"data":"pong","meta":{}}"#
        ),
        CallOutcome::Result(json!("pong"))
    );
    assert_eq!(
        outcome(200, json, r#"{"data":null,"meta":{"requestId":"r"}}"#),
        CallOutcome::Result(Value::Null)
    );
    // A body that is no envelope: data with no meta, meta that is no
    // object, a list.
    for body in [
        r#"{"data":1}"#,
        r#"{"data":1,"meta":"m"}"#,
        r#"{"rows":3}"#,
        "[1,2]",
        "7",
    ] {
        let value: Value = serde_json::from_str(body).expect("JSON");
        assert_eq!(
            outcome(200, json, body),
            CallOutcome::Result(value),
            "{body}"
        );
    }
    assert_eq!(
        outcome(200, Some("Application/Vnd.Items+JSON"), r#"{"rows":3}"#),
        CallOutcome::Result(json!({"rows": 3}))
    );
    assert_eq!(outcome(204, None, ""), CallOutcome::Result(Value::Null));
    assert_eq!(outcome(200, json, ""), CallOutcome::Result(Value::Null));
    // Text, and what is called JSON and is not.
    assert_eq!(outcome(200, json, "{nope"), text("{nope"));
    assert_eq!(
        outcome(200, Some("text/csv"), "id,name\n1,desk\n"),
        text("id,name\n1,desk\n")
    );
    assert_eq!(outcome(200, None, "plain"), text("plain"));
    assert_eq!(outcome(200, Some("application/xml"), "<a/>"), text("<a/>"));
    assert_eq!(
        outcome(200, Some("image/svg+xml"), "<svg/>"),
        text("<svg/>")
    );
    // Any other body is a resource at the route's path.
    assert_eq!(
        outcome(200, Some("application/pdf"), &b"%PDF\x00\xff"[..]),
        CallOutcome::Content(vec![Content::Resource {
            uri: "/api/export".to_owned(),
            mime_type: "application/pdf".to_owned(),
            blob: "JVBERgD/".to_owned(),
        }])
    );
}

#[test]
fn a_refusal_is_the_routes_problem_or_one_made_of_its_status_and_text() {
    let problem = json!({
        "type": "about:blank",
        "title": "Too Many Requests",
        "status": 429,
        "detail": "Too Many Requests",
        "code": "too_many_requests",
        "requestId": "from-the-route",
        "details": {"retryAfterSeconds": 3},
        "extension": true,
    });
    let refused = response(429, Some("application/problem+json"), problem.to_string());
    assert_eq!(problem_of(&refused, "req-7"), problem);
    assert_eq!(
        outcome_of(&refused, "req-7", "/api/items"),
        CallOutcome::Problem(problem)
    );
    // A body that is no problem document: text, nothing, JSON that is no
    // object, and what is called JSON and is not.
    let made = |status: u16, title: &str, detail: &str| {
        json!({
            "type": "about:blank",
            "title": title,
            "status": status,
            "detail": detail,
            "requestId": "req-7",
        })
    };
    assert_eq!(
        outcome(502, Some("text/plain"), " upstream is down \n"),
        CallOutcome::Problem(made(502, "Bad Gateway", "upstream is down"))
    );
    assert_eq!(
        outcome(404, None, ""),
        CallOutcome::Problem(made(404, "Not Found", "Request failed"))
    );
    assert_eq!(
        outcome(400, Some("application/json"), "[1]"),
        CallOutcome::Problem(made(400, "Bad Request", "[1]"))
    );
    assert_eq!(
        outcome(500, Some("application/json"), "{nope"),
        CallOutcome::Problem(made(500, "Internal Server Error", "{nope"))
    );
}

#[test]
fn a_caller_is_the_subject_and_the_calling_service_as_json() {
    assert_eq!(caller_of(Some("ada"), None), r#"["ada",null]"#);
    assert_eq!(caller_of(None, Some("janitor")), r#"[null,"janitor"]"#);
    assert_eq!(
        caller_of(Some("ada"), Some("janitor")),
        r#"["ada","janitor"]"#
    );
    assert_eq!(caller_of(None, None), "[null,null]");
    assert_eq!(
        caller_of(Some("a\"b\\c\n\u{e9}"), None),
        "[\"a\\\"b\\\\c\\n\u{e9}\",null]"
    );
}

#[test]
fn a_tool_that_asks_lists_the_argument_its_confirmation_comes_back_in() {
    let schema = json!({
        "type": "object",
        "additionalProperties": false,
        "properties": {"id": {"type": "string"}},
        "required": ["id"],
    });
    let with = with_confirmation_argument(&schema);
    assert_eq!(with["properties"]["id"], json!({"type": "string"}));
    assert_eq!(with["properties"][CONFIRMATION_ARGUMENT]["type"], "string");
    assert!(with["properties"][CONFIRMATION_ARGUMENT]["description"]
        .as_str()
        .is_some_and(|text| text.contains("confirmation_required")));
    assert_eq!(with["required"], json!(["id"]));
    assert_eq!(with["additionalProperties"], false);
    // A schema with no properties gains the one, and what is no object
    // stays as it is.
    let bare = with_confirmation_argument(&json!({"type": "object"}));
    assert_eq!(bare["properties"].as_object().map(Map::len), Some(1));
    assert_eq!(with_confirmation_argument(&json!(true)), json!(true));
}
