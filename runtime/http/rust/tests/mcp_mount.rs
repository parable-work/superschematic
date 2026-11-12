//! An API's MCP endpoint on a router (D81, D116): `Mount::router` over
//! routes a generated router would hold, as the TypeScript runtime's
//! `mcp.test.ts` and `mcpconfirm.test.ts` hold `mountMcp`. The endpoint's
//! controls establish the caller; the list holds the tools whose routes
//! admit it; a call is a request to its route, carrying the endpoint
//! request's credentials and what its host said of the connection; and a
//! call of a tool that asks is checked through its route's check before
//! anyone is asked (D93, D98).
//!
//! Every test but the one of the endpoint's own rate limit drives the
//! router with the runtime's `block_on`, with no async runtime under it,
//! as a host with none drives a generated `Api::handle`.

use async_trait::async_trait;
use axum::extract::{Extension, Path, Request};
use axum::response::IntoResponse;
use axum::routing::{delete, get, patch, post, MethodRouter};
use axum::{Json, Router};
use bytes::Bytes;
use http::request::Parts;
use http::{Method, StatusCode};
use serde_json::{json, Value};
use std::sync::{Arc, Mutex};
use superschematic_http_runtime::mcp::{
    reached, ConfirmationStore, EndpointOptions, MemoryConfirmationStore, Mount, ServerInfo,
    MCP_PATH,
};
use superschematic_http_runtime::{
    block_on, error_response, handle, request_ids, ApiError, Authenticator, OperationInfo,
    Principal, RouteControls, ServiceAuthenticator, ServiceCaller, ServiceClause, ServiceMode,
};

const KEY: &str = "a random key of at least thirty-two bytes";

const fn operation(
    name: &'static str,
    method: &'static str,
    path: &'static str,
    permissions: &'static [&'static str],
) -> OperationInfo {
    OperationInfo {
        namespace: "items",
        name,
        method,
        path,
        requires_auth: true,
        permissions,
        require_ownership: false,
        manual: false,
        rate_limit: None,
        service: None,
    }
}

const GET_ITEM: OperationInfo = operation("getItem", "GET", "/api/items/{id}", &[]);
const RENAME_ITEM: OperationInfo =
    operation("renameItem", "PATCH", "/api/items/{id}", &["items.write"]);
const DELETE_ITEM: OperationInfo =
    operation("deleteItem", "DELETE", "/api/items/{id}", &["items.admin"]);
const SYNC_ITEMS: OperationInfo = OperationInfo {
    requires_auth: false,
    service: Some(ServiceClause {
        mode: ServiceMode::Require,
        from: &[],
    }),
    ..operation("syncItems", "POST", "/api/items/sync", &[])
};
const EXPORT_ITEMS: OperationInfo = OperationInfo {
    manual: true,
    ..operation("exportItems", "GET", "/api/items/export", &[])
};
const OPERATIONS: &[OperationInfo] =
    &[GET_ITEM, RENAME_ITEM, DELETE_ITEM, SYNC_ITEMS, EXPORT_ITEMS];

/// The tool table the build would write for those operations.
fn tools() -> String {
    let tool = |name: &str, operation: &str, arguments: Value, confirm: bool| {
        let mut tool = json!({
            "name": name,
            "title": format!("The {name} tool"),
            "description": format!("Runs {operation}."),
            "inputSchema": {"type": "object", "properties": {"id": {"type": "string"}}},
            "annotations": {"readOnlyHint": !confirm},
            "_meta": {"invocationPolicy": if confirm { "ask" } else { "auto" }},
            "operation": operation,
            "arguments": arguments,
        });
        if confirm {
            tool["confirm"] = Value::Bool(true);
        }
        tool
    };
    let id = json!({"key": "id", "in": "path", "name": "id"});
    json!([
        tool("get_item", "items.getItem", json!([id]), false),
        tool(
            "rename_item",
            "items.renameItem",
            json!([id, {"key": "name", "in": "body", "name": "name"}]),
            true,
        ),
        tool("delete_item", "items.deleteItem", json!([id]), true),
        tool("sync_items", "items.syncItems", json!([]), true),
        tool("export_items", "items.exportItems", json!([]), false),
    ])
    .to_string()
}

/// What a host says of a connection, in the request's extensions: who is
/// at the other end of a local socket.
#[derive(Clone)]
struct Peer(&'static str);

/// `Authorization: Bearer <subject>[:<permission>,...]` is that subject
/// holding those permissions; without the header, the connection's peer,
/// holding none; with neither, no caller.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let token = request
            .headers
            .get(http::header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "));
        if let Some(token) = token {
            let (subject, held) = token.split_once(':').unwrap_or((token, ""));
            let held: Vec<&str> = held.split(',').filter(|held| !held.is_empty()).collect();
            return Ok(Some(Principal::new(subject, held)));
        }
        Ok(request
            .extensions
            .get::<Peer>()
            .map(|peer| Principal::new(peer.0, Vec::<String>::new())))
    }
}

/// `Service-Authorization: Bearer <deployable>` is that deployable.
struct Services;

#[async_trait]
impl ServiceAuthenticator for Services {
    async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        Ok(request
            .headers
            .get("service-authorization")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .map(|deployable| ServiceCaller::new(deployable, ["web"], deployable)))
    }
}

/// The operations a route's implementation ran, in order.
type Ran = Arc<Mutex<Vec<String>>>;

/// A route's controls, as the generated router applies an operation's.
fn controls(operation: &OperationInfo) -> RouteControls {
    let mut controls = RouteControls::under(false);
    controls = match operation.service {
        Some(clause) => controls.require_service(Arc::new(Services), clause.from),
        None => controls.identify_service(Arc::new(Services)),
    };
    if operation.requires_auth {
        controls = controls.authorize(Arc::new(Bearer), operation.permissions);
    }
    controls
}

/// What the rename route refuses of its body before its implementation.
fn decode_rename(body: &Bytes) -> Result<String, ApiError> {
    let body: Value = serde_json::from_slice(body)
        .map_err(|_| ApiError::bad_request("Request body is required"))?;
    match body.get("name").and_then(Value::as_str) {
        Some(name) if !name.is_empty() => Ok(name.to_owned()),
        _ => Err(ApiError::bad_request("name must be a non-empty string")),
    }
}

/// The routes of the operations, each behind its controls, and the routes'
/// checks: the same controls, the same decoding, and no implementation.
fn routers(ran: &Ran, manual: bool) -> (Router, Router) {
    let route = |operation: &OperationInfo, route: MethodRouter| controls(operation).apply(route);
    let ran_get = Arc::clone(ran);
    let get_item = get(
        move |Path(id): Path<String>,
              Extension(principal): Extension<Principal>,
              service: Option<Extension<ServiceCaller>>| async move {
            ran_get.lock().expect("the log").push(format!("get {id}"));
            Json(json!({"data": {
                "id": id,
                "as": principal.subject,
                "service": service.map(|Extension(caller)| caller.deployable),
            }, "meta": {}}))
        },
    );
    let ran_rename = Arc::clone(ran);
    let rename_item = patch(move |Path(id): Path<String>, body: Bytes| async move {
        match decode_rename(&body) {
            Ok(name) => {
                ran_rename
                    .lock()
                    .expect("the log")
                    .push(format!("rename {id} {name}"));
                Json(json!({"data": {"id": id, "name": name}, "meta": {}})).into_response()
            }
            Err(err) => error_response(err).into_response(),
        }
    });
    let ran_delete = Arc::clone(ran);
    let delete_item = delete(move |Path(id): Path<String>| async move {
        ran_delete
            .lock()
            .expect("the log")
            .push(format!("delete {id}"));
        StatusCode::NO_CONTENT
    });
    let ran_sync = Arc::clone(ran);
    let sync_items = post(move || async move {
        ran_sync.lock().expect("the log").push("sync".to_owned());
        Json(json!({"data": true, "meta": {}}))
    });
    let mut routes = Router::new()
        .route(
            "/api/items/{id}",
            route(&GET_ITEM, get_item)
                .merge(route(&RENAME_ITEM, rename_item))
                .merge(route(&DELETE_ITEM, delete_item)),
        )
        .route("/api/items/sync", route(&SYNC_ITEMS, sync_items));
    if manual {
        routes = routes.route(
            "/api/items/export",
            route(&EXPORT_ITEMS, get(|| async { "id\n" })),
        );
    }
    let checks = Router::new()
        .route(
            "/api/items/{id}",
            route(&GET_ITEM, get(|| async { reached() }))
                .merge(route(
                    &RENAME_ITEM,
                    patch(|body: Bytes| async move {
                        match decode_rename(&body) {
                            Ok(_) => reached(),
                            Err(err) => error_response(err).into_response(),
                        }
                    }),
                ))
                .merge(route(&DELETE_ITEM, delete(|| async { reached() }))),
        )
        .route(
            "/api/items/sync",
            route(&SYNC_ITEMS, post(|| async { reached() })),
        )
        .fallback(|| async { reached() });
    let ids = || axum::middleware::from_fn(request_ids);
    (routes.layer(ids()), checks.layer(ids()))
}

/// A mount of the operations' endpoint over routes that log to `ran`.
fn mount<'a>(tools: &'a str, ran: &Ran) -> Mount<'a> {
    let (routes, checks) = routers(ran, false);
    Mount {
        api: "items-api",
        version: "1.0.0",
        requires_auth: true,
        tools,
        operations: OPERATIONS,
        routes,
        checks: Some(checks),
        manual_routes: false,
        authenticator: Some(Arc::new(Bearer)),
        service_authenticator: Some(Arc::new(Services)),
        transport: false,
    }
}

fn keyed() -> EndpointOptions {
    EndpointOptions::default().with_confirmation_key(KEY)
}

/// The endpoint with the confirmation key, and the log of what ran.
fn endpoint() -> (Router, Ran) {
    let ran = Ran::default();
    let tools = tools();
    let router = mount(&tools, &ran).router(&keyed()).expect("a mount");
    (router, ran)
}

/// One request to the endpoint, and its answer: the status and the one
/// message of its event stream, or its JSON body.
struct Client {
    headers: Vec<(&'static str, String)>,
    peer: Option<Peer>,
}

impl Client {
    fn bearer(token: &str) -> Self {
        Self {
            headers: vec![("authorization", format!("Bearer {token}"))],
            peer: None,
        }
    }

    fn none() -> Self {
        Self {
            headers: Vec::new(),
            peer: None,
        }
    }

    fn with(mut self, name: &'static str, value: &str) -> Self {
        self.headers.push((name, value.to_owned()));
        self
    }

    fn send(&self, router: &Router, method: Method, body: &Value) -> (StatusCode, Value) {
        let mut request = http::Request::builder()
            .method(method)
            .uri(MCP_PATH)
            .header("accept", "application/json, text/event-stream")
            .header("content-type", "application/json");
        for (name, value) in &self.headers {
            request = request.header(*name, value);
        }
        let mut request = request
            .body(Bytes::from(body.to_string()))
            .expect("a request");
        if let Some(peer) = &self.peer {
            request.extensions_mut().insert(peer.clone());
        }
        let response = block_on(handle(router, request));
        let text = String::from_utf8(response.body().to_vec()).expect("UTF-8");
        let text = text
            .strip_prefix("event: message\ndata: ")
            .map_or(text.as_str(), str::trim_end);
        let message = if text.is_empty() {
            Value::Null
        } else {
            serde_json::from_str(text).unwrap_or_else(|err| panic!("{text:?}: {err}"))
        };
        (response.status(), message)
    }

    fn rpc(&self, router: &Router, method: &str, params: Value) -> Value {
        let (status, message) = self.send(
            router,
            Method::POST,
            &json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}),
        );
        assert_eq!(status, StatusCode::OK, "{message}");
        message
    }

    /// The result of a 2025 `tools/call`.
    fn call(&self, router: &Router, name: &str, arguments: Value) -> Value {
        let reply = self.rpc(
            router,
            "tools/call",
            json!({"name": name, "arguments": arguments}),
        );
        assert!(reply["result"].is_object(), "{reply}");
        reply["result"].clone()
    }

    /// The result of a `tools/call` of a 2026-07-28 client whose
    /// capabilities elicit forms, with `more` beside the name and the
    /// arguments.
    fn eliciting_call(&self, router: &Router, name: &str, arguments: Value, more: Value) -> Value {
        let mut params = json!({
            "name": name,
            "arguments": arguments,
            "_meta": {
                "io.modelcontextprotocol/protocolVersion": "2026-07-28",
                "io.modelcontextprotocol/clientInfo": {"name": "t", "version": "1"},
                "io.modelcontextprotocol/clientCapabilities": {"elicitation": {"form": {}}},
            },
        });
        for (key, value) in more.as_object().into_iter().flatten() {
            params[key] = value.clone();
        }
        let client = Self {
            headers: self.headers.clone(),
            peer: self.peer.clone(),
        }
        .with("mcp-protocol-version", "2026-07-28");
        let reply = client.rpc(router, "tools/call", params);
        assert!(reply["result"].is_object(), "{reply}");
        reply["result"].clone()
    }
}

fn ran(log: &Ran) -> Vec<String> {
    log.lock().expect("the log").clone()
}

fn problem(result: &Value) -> &Value {
    assert_eq!(result["isError"], true, "{result}");
    &result["structuredContent"]
}

#[test]
fn the_endpoint_needs_the_caller_a_route_of_the_api_needs() {
    let (router, _) = endpoint();
    let list = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"});
    let (status, refused) = Client::none().send(&router, Method::POST, &list);
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{refused}");
    assert_eq!(refused["code"], "unauthorized");
    assert!(refused["requestId"].is_string(), "{refused}");
    // GET and DELETE answer 405 behind the same gate: a stateless server
    // keeps no session and opens no stream.
    for method in [Method::GET, Method::DELETE] {
        let (status, _) = Client::none().send(&router, method.clone(), &Value::Null);
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        let (status, answer) = Client::bearer("ada").send(&router, method, &Value::Null);
        assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED, "{answer}");
        assert_eq!(answer["error"]["message"], "Method not allowed.");
    }
}

#[test]
fn initialize_names_the_api_and_its_served_version_unless_the_options_name_another() {
    let init = json!({"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "t", "version": "1"}});
    let (router, _) = endpoint();
    let reply = Client::bearer("ada").rpc(&router, "initialize", init.clone());
    assert_eq!(
        reply["result"]["serverInfo"],
        json!({"name": "items-api", "version": "1.0.0"})
    );
    assert!(reply["result"].get("instructions").is_none(), "{reply}");

    let log = Ran::default();
    let tools = tools();
    let named = EndpointOptions {
        server_info: Some(ServerInfo::new("items", "9")),
        instructions: Some("Items.".to_owned()),
        ..keyed()
    };
    let router = Mount {
        version: "",
        ..mount(&tools, &log)
    }
    .router(&named)
    .expect("a mount");
    let reply = Client::bearer("ada").rpc(&router, "initialize", init.clone());
    assert_eq!(
        reply["result"]["serverInfo"],
        json!({"name": "items", "version": "9"})
    );
    assert_eq!(reply["result"]["instructions"], "Items.");
    // A router that states no version serves the default one.
    let router = Mount {
        version: "",
        ..mount(&tools, &log)
    }
    .router(&keyed())
    .expect("a mount");
    let reply = Client::bearer("ada").rpc(&router, "initialize", init);
    assert_eq!(reply["result"]["serverInfo"]["version"], "1.0.0");
}

#[test]
fn the_list_holds_the_tools_whose_routes_admit_the_caller() {
    let (router, _) = endpoint();
    let names = |client: &Client| -> Vec<String> {
        let listed = client.rpc(&router, "tools/list", json!({}));
        listed["result"]["tools"]
            .as_array()
            .expect("tools")
            .iter()
            .map(|tool| tool["name"].as_str().expect("a name").to_owned())
            .collect()
    };
    // A route with a service clause and no user clause needs no end user,
    // so its tool is listed; its route refuses the call.
    assert_eq!(
        names(&Client::bearer("ada")),
        ["get_item", "sync_items", "export_items"]
    );
    assert_eq!(
        names(&Client::bearer("ada:items.write")),
        ["get_item", "rename_item", "sync_items", "export_items"]
    );
    // A permission covers what is nested under it, as on the routes.
    assert_eq!(
        names(&Client::bearer("root:items")),
        [
            "get_item",
            "rename_item",
            "delete_item",
            "sync_items",
            "export_items"
        ]
    );
    let listed = Client::bearer("ada:items.write").rpc(&router, "tools/list", json!({}));
    let tools = listed["result"]["tools"].as_array().expect("tools");
    assert!(tools[0]["inputSchema"]["properties"]["confirmationToken"].is_null());
    assert_eq!(
        tools[1]["inputSchema"]["properties"]["confirmationToken"]["type"],
        "string"
    );
    assert!(tools[1].get("operation").is_none() && tools[1].get("arguments").is_none());
}

#[test]
fn a_call_is_a_request_to_its_route_as_the_endpoints_caller() {
    let (router, log) = endpoint();
    let ada = Client::bearer("ada").with("service-authorization", "Bearer web");
    let got = ada.call(&router, "get_item", json!({"id": "i 1"}));
    assert_eq!(
        got["structuredContent"],
        json!({"id": "i 1", "as": "ada", "service": "web"})
    );
    assert_eq!(ran(&log), ["get i 1"]);
    // A tool the caller is not offered is no tool, whether or not it asks.
    for name in ["rename_item", "delete_item", "no_such_tool"] {
        let reply = ada.rpc(
            &router,
            "tools/call",
            json!({"name": name, "arguments": {"id": "i1"}}),
        );
        assert_eq!(
            reply["error"],
            json!({"code": -32602, "message": format!("items-api has no tool \"{name}\"")})
        );
    }
    // An argument the tool does not take is refused before any route.
    let refused = ada.call(&router, "get_item", json!({"id": "i1", "input": {}}));
    assert_eq!(problem(&refused)["status"], 400);
    assert_eq!(
        problem(&refused)["detail"],
        "get_item takes id, not \"input\""
    );
    assert_eq!(ran(&log), ["get i 1"]);
}

#[test]
fn what_the_host_said_of_the_connection_reaches_the_calls_route() {
    let (router, _) = endpoint();
    let local = Client {
        headers: Vec::new(),
        peer: Some(Peer("the-socket's-owner")),
    };
    let got = local.call(&router, "get_item", json!({"id": "i1"}));
    assert_eq!(got["structuredContent"]["as"], "the-socket's-owner");
}

#[test]
fn a_tool_that_asks_runs_once_with_the_token_its_first_call_answered() {
    let (router, log) = endpoint();
    let ada = Client::bearer("ada:items.write");
    let arguments = json!({"id": "i1", "name": "Desk"});
    let asked = ada.call(&router, "rename_item", arguments.clone());
    let required = problem(&asked);
    assert_eq!(required["status"], 428, "{asked}");
    assert_eq!(required["code"], "confirmation_required");
    assert_eq!(
        required["details"]["summary"],
        "The rename_item tool (rename_item) with {\"id\":\"i1\",\"name\":\"Desk\"}"
    );
    let token = required["details"]["confirmationToken"].clone();
    assert!(ran(&log).is_empty());

    // Another caller's call does not redeem it, and does not spend it.
    let mut confirmed = arguments.clone();
    confirmed["confirmationToken"] = token.clone();
    let other = Client::bearer("ben:items.write").call(&router, "rename_item", confirmed.clone());
    assert_eq!(problem(&other)["details"]["refused"], "mismatch");
    assert!(ran(&log).is_empty());

    let renamed = ada.call(&router, "rename_item", confirmed.clone());
    assert_eq!(
        renamed["structuredContent"],
        json!({"id": "i1", "name": "Desk"})
    );
    assert_eq!(ran(&log), ["rename i1 Desk"]);
    let again = ada.call(&router, "rename_item", confirmed);
    assert_eq!(problem(&again)["details"]["refused"], "used");
    assert_eq!(ran(&log), ["rename i1 Desk"]);
}

#[test]
fn a_client_that_elicits_is_asked_in_band_and_the_call_runs_on_accept() {
    let (router, log) = endpoint();
    let ada = Client::bearer("ada:items.write");
    let arguments = json!({"id": "i1", "name": "Desk"});
    let asked = ada.eliciting_call(&router, "rename_item", arguments.clone(), json!({}));
    assert_eq!(asked["resultType"], "input_required", "{asked}");
    assert_eq!(
        asked["inputRequests"]["confirm"]["method"],
        "elicitation/create"
    );
    assert!(ran(&log).is_empty());
    let answer = json!({
        "inputResponses": {"confirm": {"action": "accept"}},
        "requestState": asked["requestState"],
    });
    let renamed = ada.eliciting_call(&router, "rename_item", arguments, answer);
    assert_eq!(renamed["structuredContent"]["name"], "Desk", "{renamed}");
    assert_eq!(ran(&log), ["rename i1 Desk"]);
}

#[test]
fn a_call_its_route_would_refuse_is_refused_at_once_and_no_one_is_asked() {
    let (router, log) = endpoint();
    let ada = Client::bearer("ada:items.write");
    let good = json!({"id": "i1", "name": "Desk"});
    let token = problem(&ada.call(&router, "rename_item", good.clone()))["details"]
        ["confirmationToken"]
        .clone();
    // The route's decoding refuses it: its 400, and no token.
    for arguments in [
        json!({"id": "i1", "name": ""}),
        json!({"id": "i1", "name": 7}),
        json!({"id": "i1"}),
        json!({"id": "i1", "name": "", "confirmationToken": token}),
    ] {
        let refused = ada.call(&router, "rename_item", arguments.clone());
        assert_eq!(problem(&refused)["status"], 400, "{arguments}: {refused}");
        assert_eq!(problem(&refused)["code"], "bad_request");
        assert!(problem(&refused)["details"].is_null(), "{refused}");
        assert!(problem(&refused)["requestId"].is_string(), "{refused}");
    }
    let wrapped = ada.call(
        &router,
        "rename_item",
        json!({"id": "i1", "input": {"name": "Desk"}}),
    );
    assert_eq!(
        problem(&wrapped)["detail"],
        "rename_item takes id, name, not \"input\""
    );
    // A client that elicits is not asked either.
    let elicited = ada.eliciting_call(
        &router,
        "rename_item",
        json!({"id": "i1", "name": ""}),
        json!({}),
    );
    assert_eq!(elicited["resultType"], "complete", "{elicited}");
    assert_eq!(problem(&elicited)["status"], 400);
    assert!(elicited.get("requestState").is_none());
    assert!(ran(&log).is_empty());
    // The token sent with a mis-shaped call was not spent: it runs its own.
    let mut confirmed = good;
    confirmed["confirmationToken"] = token;
    let renamed = ada.call(&router, "rename_item", confirmed);
    assert_eq!(renamed["structuredContent"]["name"], "Desk", "{renamed}");
    assert_eq!(ran(&log), ["rename i1 Desk"]);
}

#[test]
fn a_caller_the_route_refuses_is_refused_at_once_and_no_one_is_asked() {
    let (router, log) = endpoint();
    // sync_items' route is a calling service's alone: the list cannot see
    // its service clause, and its check answers the route's 401.
    let ada = Client::bearer("ada");
    let refused = ada.call(&router, "sync_items", json!({}));
    assert_eq!(problem(&refused)["status"], 401, "{refused}");
    assert_eq!(problem(&refused)["code"], "service_unauthorized");
    assert!(problem(&refused)["details"].is_null(), "{refused}");
    let elicited = ada.eliciting_call(&router, "sync_items", json!({}), json!({}));
    assert_eq!(elicited["resultType"], "complete", "{elicited}");
    assert_eq!(problem(&elicited)["status"], 401);
    // A calling service is asked, bound to its deployable beside the user.
    let web = Client::bearer("ada").with("service-authorization", "Bearer web");
    let asked = web.call(&router, "sync_items", json!({}));
    assert_eq!(problem(&asked)["code"], "confirmation_required", "{asked}");
    let token = problem(&asked)["details"]["confirmationToken"].clone();
    let jobs = Client::bearer("ada").with("service-authorization", "Bearer jobs");
    let other = jobs.call(&router, "sync_items", json!({"confirmationToken": token}));
    assert_eq!(problem(&other)["details"]["refused"], "mismatch", "{other}");
    assert!(ran(&log).is_empty());
    let synced = web.call(&router, "sync_items", json!({"confirmationToken": token}));
    assert_eq!(synced["content"][0]["text"], "true", "{synced}");
    assert_eq!(ran(&log), ["sync"]);
}

#[test]
fn a_tool_of_a_manual_operation_answers_501_where_its_route_is_not_mounted() {
    let (router, _) = endpoint();
    let ada = Client::bearer("ada");
    let refused = ada.call(&router, "export_items", json!({}));
    assert_eq!(problem(&refused)["status"], 501, "{refused}");
    assert_eq!(problem(&refused)["code"], "not_implemented");
    assert_eq!(
        problem(&refused)["detail"],
        "exportItems has no manual route handler"
    );

    let log = Ran::default();
    let tools = tools();
    let (routes, checks) = routers(&log, true);
    let router = Mount {
        routes,
        checks: Some(checks),
        manual_routes: true,
        ..mount(&tools, &log)
    }
    .router(&keyed())
    .expect("a mount");
    let exported = ada.call(&router, "export_items", json!({}));
    assert_eq!(exported["content"][0]["text"], "id\n", "{exported}");
}

#[test]
fn a_router_with_no_checks_confirms_every_call_and_refuses_it_once_it_runs() {
    let log = Ran::default();
    let tools = tools();
    let router = Mount {
        checks: None,
        ..mount(&tools, &log)
    }
    .router(&keyed())
    .expect("a mount");
    let ada = Client::bearer("ada:items.write");
    let asked = ada.call(&router, "rename_item", json!({"id": "i1", "name": ""}));
    assert_eq!(problem(&asked)["code"], "confirmation_required", "{asked}");
    let token = problem(&asked)["details"]["confirmationToken"].clone();
    let refused = ada.call(
        &router,
        "rename_item",
        json!({"id": "i1", "name": "", "confirmationToken": token}),
    );
    assert_eq!(problem(&refused)["status"], 400, "{refused}");
    assert!(ran(&log).is_empty());
}

#[test]
fn instances_under_one_key_and_one_store_redeem_each_others_tokens_once() {
    let log = Ran::default();
    let tools = tools();
    let store: Arc<dyn ConfirmationStore> = Arc::new(MemoryConfirmationStore::new());
    let shared = keyed().with_confirmation_store(Arc::clone(&store));
    let one = mount(&tools, &log).router(&shared).expect("a mount");
    let two = mount(&tools, &log).router(&shared).expect("a mount");
    let ada = Client::bearer("ada:items.write");
    let arguments = json!({"id": "i1", "name": "Desk"});
    let token = problem(&ada.call(&one, "rename_item", arguments.clone()))["details"]
        ["confirmationToken"]
        .clone();
    let mut confirmed = arguments;
    confirmed["confirmationToken"] = token;
    let renamed = ada.call(&two, "rename_item", confirmed.clone());
    assert_eq!(renamed["structuredContent"]["name"], "Desk", "{renamed}");
    let again = ada.call(&one, "rename_item", confirmed.clone());
    assert_eq!(problem(&again)["details"]["refused"], "used");
    // With a store each, the token redeems once on each instance.
    let alone = mount(&tools, &log).router(&keyed()).expect("a mount");
    let renamed = ada.call(&alone, "rename_item", confirmed);
    assert_eq!(renamed["structuredContent"]["name"], "Desk", "{renamed}");
    assert_eq!(shared, shared.clone());
    assert_ne!(shared, keyed());
}

#[test]
fn a_mount_that_cannot_serve_its_tools_is_refused() {
    let log = Ran::default();
    let tools = tools();
    let refusal = |mount: Mount<'_>, options: &EndpointOptions| -> String {
        mount.router(options).expect_err("a refusal")
    };
    assert_eq!(
        refusal(mount(&tools, &log), &EndpointOptions::default()),
        "MCP tool rename_item of items-api runs only once the person confirms it, so RouterOptions.mcp.confirmation_key is required: a random secret of at least 32 bytes, the same on every instance (D93)"
    );
    let short = refusal(
        mount(&tools, &log),
        &EndpointOptions::default().with_confirmation_key("short"),
    );
    assert!(
        short.starts_with("the MCP endpoint of items-api: ") && short.contains("32"),
        "{short}"
    );
    let unknown = tools.replace("items.getItem", "items.findItem");
    assert_eq!(
        refusal(mount(&unknown, &log), &keyed()),
        "MCP tool get_item names operation items.findItem, which the router of items-api does not have"
    );
    let twice = tools.replace("\"name\":\"delete_item\"", "\"name\":\"get_item\"");
    assert_eq!(
        refusal(mount(&twice, &log), &keyed()),
        "two MCP tools are named get_item"
    );
    let unreadable = refusal(mount("{}", &log), &keyed());
    assert!(
        unreadable.starts_with("the MCP tool table of items-api does not read: "),
        "{unreadable}"
    );
    assert_eq!(
        refusal(
            Mount {
                authenticator: None,
                ..mount(&tools, &log)
            },
            &keyed()
        ),
        "the MCP endpoint of items-api needs a caller, and its router has no authenticator"
    );
    // The key stays out of what the options print.
    let printed = format!("{:?}", keyed());
    assert!(
        printed.contains("ConfirmationKey(..)") && !printed.contains("random"),
        "{printed}"
    );
}

/// The endpoint's own rate limit counts its requests, with a transport
/// under the router; a host with none counts them itself.
#[tokio::test]
async fn the_endpoints_rate_limit_is_its_own_and_its_hosts_with_no_transport() {
    let log = Ran::default();
    let tools = tools();
    let limited = EndpointOptions {
        rate_limit_per_minute: Some(1),
        timeout_seconds: Some(30),
        ..keyed()
    };
    let list = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"});
    let post = || {
        http::Request::builder()
            .method(Method::POST)
            .uri(MCP_PATH)
            .header("accept", "application/json, text/event-stream")
            .header("content-type", "application/json")
            .header("authorization", "Bearer ada")
            .body(Bytes::from(list.to_string()))
            .expect("a request")
    };
    let served = Mount {
        transport: true,
        ..mount(&tools, &log)
    }
    .router(&limited)
    .expect("a mount");
    assert_eq!(handle(&served, post()).await.status(), StatusCode::OK);
    let refused = handle(&served, post()).await;
    assert_eq!(refused.status(), StatusCode::TOO_MANY_REQUESTS);
    assert!(refused.headers().contains_key("retry-after"));

    let hostless = mount(&tools, &log).router(&limited).expect("a mount");
    for _ in 0..3 {
        assert_eq!(handle(&hostless, post()).await.status(), StatusCode::OK);
    }
}

/// A body past the transport's limit is refused by the transport, as the
/// TypeScript endpoint refuses it, without the route's body limit.
#[test]
fn a_body_past_the_transports_limit_is_the_transports_to_refuse() {
    let (router, _) = endpoint();
    let request: Request<Bytes> = http::Request::builder()
        .method(Method::POST)
        .uri(MCP_PATH)
        .header("accept", "application/json, text/event-stream")
        .header("content-type", "application/json")
        .header("authorization", "Bearer ada")
        .body(Bytes::from(vec![b' '; 4 * 1024 * 1024 + 1]))
        .expect("a request");
    let response = block_on(handle(&router, request));
    assert_eq!(response.status(), StatusCode::PAYLOAD_TOO_LARGE);
    let answer: Value = serde_json::from_slice(response.body()).expect("JSON");
    assert_eq!(answer["error"]["code"], -32000, "{answer}");
}
