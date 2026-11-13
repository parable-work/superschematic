//! The generated Rust server of fixture-mcp with outputs.api.mcp on (D81,
//! D116): its MCP endpoint, a route of `build_router_with`'s router and of
//! `Api::handle` alike, driven with no socket under it and no async
//! runtime, by the runtime's `block_on`. The cases are those the
//! TypeScript router is held to (tsrestgen's mcp_runtime.test.ts). The
//! implementation records each operation it runs with what it was given,
//! so a call shows where its route read each argument and as whom.
//!
//! With the `guarded` feature the crate is the build whose routes need
//! callers: getOrder a permission, openReturn a caller or a listed calling
//! service, and updateOrder takes one request a minute.

use std::sync::{Arc, Mutex};

use api::runtime::mcp::EndpointOptions;
use api::runtime::{block_on, handle, ApiError, RequestContext};
use api::types::{IdentityUUID, Order, OrderStatus};
use api::{
    build_router_with, Api, Handlers, Implementations, OrderDeleteOrderArgs, OrderGetOrderArgs,
    OrderImplementation, OrderListOrdersArgs, OrderOpenReturnArgs, OrderUpdateOrderArgs,
    RouterOptions,
};
use async_trait::async_trait;
use axum::body::Bytes;
use axum::Router;
use http::{Method, Request, Response, StatusCode};
use serde_json::{json, Value};

const ORDER: &str = "00000000-0000-4000-8000-000000000001";

/// The key the endpoint signs the confirmations of update_order, whose
/// policy is ask, with (D93); the router needs one.
const KEY: &str = "a random key of at least thirty-two bytes";

/// The operations the implementation ran, in order, each with what it was
/// given.
type Ran = Arc<Mutex<Vec<String>>>;

struct Orders {
    ran: Ran,
}

impl Orders {
    /// Records `what` as `ctx`'s caller ran it, and answers an order.
    fn ran(&self, ctx: &RequestContext, what: String, revision: f64) -> Result<Order, ApiError> {
        let user = ctx.principal.as_ref().map(|principal| principal.subject.as_str());
        let service = ctx.service_caller.as_ref().map(|caller| caller.deployable.as_str());
        let who = match (user, service) {
            (None, None) => String::new(),
            (user, service) => format!(" as {} via {}", user.unwrap_or("no one"), service.unwrap_or("no service")),
        };
        self.ran.lock().expect("the log").push(format!("{what}{who}"));
        Ok(Order {
            id: serde_json::from_value(json!(ORDER)).expect("an id"),
            status: OrderStatus::Open,
            revision,
            total_cents: 0.0,
        })
    }
}

/// An id as the wire carries it.
fn text(id: &IdentityUUID) -> String {
    serde_json::to_value(id).expect("an id").as_str().expect("text").to_owned()
}

#[async_trait]
impl OrderImplementation for Orders {
    async fn list_orders(&self, ctx: RequestContext, args: OrderListOrdersArgs) -> Result<Vec<Order>, ApiError> {
        let what = format!("listOrders {:?} {:?}", args.statuses, args.limit);
        Ok(vec![self.ran(&ctx, what, 1.0)?])
    }

    async fn export_orders(&self, _ctx: RequestContext) -> Result<String, ApiError> {
        Ok("id\n".to_owned())
    }

    async fn delete_order(&self, ctx: RequestContext, args: OrderDeleteOrderArgs) -> Result<Order, ApiError> {
        self.ran(&ctx, format!("deleteOrder {}", text(&args.id)), 1.0)
    }

    async fn get_order(&self, ctx: RequestContext, args: OrderGetOrderArgs) -> Result<Order, ApiError> {
        self.ran(&ctx, format!("getOrder {}", text(&args.id)), 1.0)
    }

    async fn update_order(&self, ctx: RequestContext, args: OrderUpdateOrderArgs) -> Result<Order, ApiError> {
        let what = format!("updateOrder {} {:?}", text(&args.id), args.input.note);
        self.ran(&ctx, what, args.input.revision)
    }

    async fn open_return(&self, ctx: RequestContext, args: OrderOpenReturnArgs) -> Result<Order, ApiError> {
        let what = format!(
            "openReturn {} {} {:?} {:?}",
            args.input.reason, args.input.pickup.city, args.input.pickup.postal_code, args.input.labels
        );
        self.ran(&ctx, what, 1.0)
    }
}

#[cfg(not(feature = "guarded"))]
fn implementations(ran: &Ran) -> Implementations {
    Implementations::new(Handlers {
        order: Arc::new(Orders { ran: Arc::clone(ran) }),
    })
}

#[cfg(feature = "guarded")]
fn implementations(ran: &Ran) -> Implementations {
    Implementations::new(
        Handlers {
            order: Arc::new(Orders { ran: Arc::clone(ran) }),
            authenticator: Arc::new(guarded::Bearer),
        },
        Arc::new(guarded::Services),
    )
}

/// The router's options with the endpoint's confirmation key.
fn keyed() -> RouterOptions {
    RouterOptions {
        mcp: EndpointOptions::default().with_confirmation_key(KEY),
        ..RouterOptions::default()
    }
}

/// The API as a host reaches it: through the router a listener serves, or
/// through `Api::handle`, with no transport under it.
enum Server {
    Router(Router),
    Api(Api),
}

impl Server {
    /// Both ways into the API with `options`, each over an implementation
    /// of its own, with the log of what it ran.
    fn each(options: &RouterOptions) -> Vec<(&'static str, Server, Ran)> {
        let (routed, handled) = (Ran::default(), Ran::default());
        vec![
            (
                "build_router_with",
                Server::Router(build_router_with(implementations(&routed), options.clone())),
                routed,
            ),
            ("Api::handle", Server::Api(Api::with(implementations(&handled), options.clone())), handled),
        ]
    }

    fn send(&self, request: Request<Bytes>) -> Response<Bytes> {
        match self {
            Server::Router(router) => block_on(handle(router, request)),
            Server::Api(api) => block_on(api.handle(request)),
        }
    }
}

/// Who calls the endpoint: the headers its requests carry.
#[derive(Clone, Default)]
struct Client {
    headers: Vec<(&'static str, String)>,
}

impl Client {
    #[cfg(feature = "guarded")]
    fn bearer(token: &str) -> Self {
        Self::default().with("authorization", &format!("Bearer {token}"))
    }

    fn with(mut self, name: &'static str, value: &str) -> Self {
        self.headers.push((name, value.to_owned()));
        self
    }

    /// One request to `path`, and its answer: the status and the one
    /// message of its event stream, or its JSON body.
    fn send(&self, server: &Server, method: Method, path: &str, body: Option<&Value>) -> (StatusCode, Value) {
        let mut request = Request::builder()
            .method(method)
            .uri(path)
            .header("accept", "application/json, text/event-stream");
        if body.is_some() {
            request = request.header("content-type", "application/json");
        }
        for (name, value) in &self.headers {
            request = request.header(*name, value);
        }
        let body = body.map_or_else(Bytes::new, |body| Bytes::from(body.to_string()));
        let response = server.send(request.body(body).expect("a request"));
        let text = String::from_utf8(response.body().to_vec()).expect("UTF-8");
        let text = text.strip_prefix("event: message\ndata: ").map_or(text.as_str(), str::trim_end);
        let message = serde_json::from_str(text).unwrap_or(Value::String(text.to_owned()));
        (response.status(), message)
    }

    fn rpc(&self, server: &Server, method: &str, params: Value) -> Value {
        let message = json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params});
        let (status, reply) = self.send(server, Method::POST, "/api/mcp", Some(&message));
        assert_eq!(status, StatusCode::OK, "{reply}");
        reply
    }

    /// The result of a 2025 `tools/call`.
    fn call(&self, server: &Server, name: &str, arguments: Value) -> Value {
        let reply = self.rpc(server, "tools/call", json!({"name": name, "arguments": arguments}));
        assert!(reply["result"].is_object(), "{reply}");
        reply["result"].clone()
    }

    /// The result of a `tools/call` of a 2026-07-28 client whose
    /// capabilities elicit forms, with `more` beside the name and the
    /// arguments.
    fn eliciting_call(&self, server: &Server, name: &str, arguments: Value, more: Value) -> Value {
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
        let reply = self.clone().with("mcp-protocol-version", "2026-07-28").rpc(server, "tools/call", params);
        assert!(reply["result"].is_object(), "{reply}");
        reply["result"].clone()
    }

    fn tools(&self, server: &Server) -> Vec<Value> {
        self.rpc(server, "tools/list", json!({}))["result"]["tools"].as_array().expect("tools").clone()
    }
}

fn names(tools: &[Value]) -> Vec<&str> {
    tools.iter().map(|tool| tool["name"].as_str().expect("a name")).collect()
}

fn ran(log: &Ran) -> Vec<String> {
    log.lock().expect("the log").clone()
}

/// The problem a call answered as a tool error.
fn problem(result: &Value) -> &Value {
    assert_eq!(result["isError"], true, "{result}");
    &result["structuredContent"]
}

/// The confirmation token a call of a tool that asks answered.
fn token_of(asked: &Value) -> Value {
    let required = problem(asked);
    assert_eq!(required["status"], 428, "{asked}");
    assert_eq!(required["code"], "confirmation_required", "{asked}");
    assert!(required["details"]["confirmationToken"].is_string(), "{asked}");
    required["details"]["confirmationToken"].clone()
}

fn update(revision: Value, token: Option<&Value>) -> Value {
    let mut arguments = json!({"id": ORDER, "revision": revision, "note": null});
    if let Some(token) = token {
        arguments["confirmationToken"] = token.clone();
    }
    arguments
}

#[cfg(not(feature = "guarded"))]
mod open {
    use super::*;
    use api::runtime::mcp::ServerInfo;

    fn anyone() -> Client {
        Client::default()
    }

    #[test]
    fn the_openapi_routes_are_served_beside_the_endpoint() {
        for (way, server, _) in Server::each(&keyed()) {
            let (status, document) = anyone().send(&server, Method::GET, "/api/openapi.json", None);
            assert_eq!(status, StatusCode::OK, "{way}");
            assert!(document["paths"]["/api/orders/{id}"].is_object(), "{way}: {document}");
            assert!(document["paths"]["/api/mcp"].is_null(), "{way}");
            // GET and DELETE answer 405: a stateless server keeps no
            // session and opens no stream.
            for method in [Method::GET, Method::DELETE] {
                let (status, answer) = anyone().send(&server, method, "/api/mcp", None);
                assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED, "{way}");
                assert_eq!(answer["error"]["message"], "Method not allowed.", "{way}");
            }
        }
    }

    #[test]
    fn initialize_names_the_api_and_its_served_version() {
        let init = json!({"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "t", "version": "1"}});
        let info = |options: &RouterOptions| -> Vec<Value> {
            Server::each(options)
                .iter()
                .map(|(_, server, _)| anyone().rpc(server, "initialize", init.clone())["result"].clone())
                .collect()
        };
        for result in info(&keyed()) {
            assert_eq!(result["serverInfo"], json!({"name": "fixture-mcp", "version": "1.0.0"}));
        }
        let stated = RouterOptions {
            openapi_version: "2.4.0".to_owned(),
            ..keyed()
        };
        for result in info(&stated) {
            assert_eq!(result["serverInfo"], json!({"name": "fixture-mcp", "version": "2.4.0"}));
        }
        let named = RouterOptions {
            mcp: EndpointOptions {
                server_info: Some(ServerInfo::new("orders", "9")),
                instructions: Some("Orders.".to_owned()),
                ..keyed().mcp
            },
            ..keyed()
        };
        for result in info(&named) {
            assert_eq!(result["serverInfo"], json!({"name": "orders", "version": "9"}));
            assert_eq!(result["instructions"], "Orders.");
        }
    }

    #[test]
    fn the_list_holds_the_visible_tools_with_their_policy_and_no_hidden_or_unclassified_operation() {
        for (way, server, _) in Server::each(&keyed()) {
            let tools = anyone().tools(&server);
            assert_eq!(names(&tools), ["list_orders", "get_order", "update_order", "open_return"], "{way}");
            let tool = |name: &str| tools.iter().find(|tool| tool["name"] == name).expect("the tool");
            let get = tool("get_order");
            assert_eq!(get["title"], "Get an order");
            assert_eq!(get["description"], "Returns one order by its identifier.");
            assert_eq!(get["annotations"]["readOnlyHint"], true);
            assert_eq!(get["_meta"]["ui"]["resourceUri"], "ui://orders/detail");
            assert_eq!(get["_meta"]["invocationPolicy"], "auto");
            assert_eq!(
                get["_meta"]["superschematic/operation-guidance"]["useWhen"],
                "Use when you have an order identifier."
            );
            assert!(get["inputSchema"]["properties"]["confirmationToken"].is_null());
            let update = tool("update_order");
            assert_eq!(update["annotations"]["readOnlyHint"], false);
            assert_eq!(update["_meta"]["invocationPolicy"], "ask");
            assert_eq!(update["inputSchema"]["properties"]["confirmationToken"]["type"], "string");
            assert_eq!(tool("list_orders")["inputSchema"]["required"], json!(["statuses"]));
        }
    }

    #[test]
    fn a_hidden_operation_is_no_tool_to_call() {
        for (way, server, log) in Server::each(&keyed()) {
            let reply = anyone().rpc(&server, "tools/call", json!({"name": "delete_order", "arguments": {"id": ORDER}}));
            assert_eq!(
                reply["error"],
                json!({"code": -32602, "message": "fixture-mcp has no tool \"delete_order\""}),
                "{way}"
            );
            assert!(ran(&log).is_empty(), "{way}");
        }
    }

    #[test]
    fn a_call_runs_the_route_which_reads_each_argument_where_the_operation_takes_it() {
        for (way, server, log) in Server::each(&keyed()) {
            let got = anyone().call(&server, "get_order", json!({"id": ORDER}));
            let id = got["structuredContent"]["id"].as_str().expect("an id").to_owned();
            assert_eq!(got["structuredContent"]["status"], "open", "{way}: {got}");
            let text: Value = serde_json::from_str(got["content"][0]["text"].as_str().expect("text")).expect("JSON");
            assert_eq!(text, got["structuredContent"], "{way}");

            let listed = anyone().call(&server, "list_orders", json!({"statuses": ["open", "shipped"], "limit": 5}));
            assert!(listed.get("structuredContent").is_none(), "{way}: {listed}");
            let rows: Value = serde_json::from_str(listed["content"][0]["text"].as_str().expect("text")).expect("JSON");
            assert_eq!(rows[0]["id"], id.as_str(), "{way}");

            let pickup = json!({"line1": "1 Main St", "city": "Springfield", "postalCode": null});
            let opened = anyone().call(
                &server,
                "open_return",
                json!({"requestId": ORDER, "orderId": ORDER, "reason": "Too small", "pickup": pickup, "labels": {"size": "S"}}),
            );
            assert_eq!(opened["structuredContent"]["id"], id.as_str(), "{way}: {opened}");
            assert_eq!(
                ran(&log),
                [
                    format!("getOrder {id}"),
                    "listOrders [Open, Shipped] Some(5.0)".to_owned(),
                    "openReturn Too small Springfield None {\"size\": \"S\"}".to_owned(),
                ],
                "{way}"
            );

            // update_order asks: its first call runs nothing and answers a
            // token, and the same call with the token runs, once (D93).
            let asked = anyone().call(&server, "update_order", update(json!(2), None));
            assert_eq!(problem(&asked)["details"]["tool"], "update_order", "{way}");
            assert_eq!(
                problem(&asked)["details"]["summary"],
                format!("Update an order (update_order) with {{\"id\":\"{ORDER}\",\"note\":null,\"revision\":2}}"),
                "{way}"
            );
            let token = token_of(&asked);
            assert_eq!(ran(&log).len(), 3, "{way}");
            let updated = anyone().call(&server, "update_order", update(json!(2), Some(&token)));
            assert_eq!(updated["structuredContent"]["revision"], 2.0, "{way}: {updated}");
            assert_eq!(ran(&log)[3], format!("updateOrder {id} None"), "{way}");
            let again = anyone().call(&server, "update_order", update(json!(2), Some(&token)));
            assert_eq!(problem(&again)["code"], "confirmation_required", "{way}");
            assert_eq!(problem(&again)["details"]["refused"], "used", "{way}");
            assert_eq!(ran(&log).len(), 4, "{way}");
        }
    }

    #[test]
    fn update_order_is_asked_about_in_band_for_a_client_that_elicits_and_runs_on_accept() {
        for (way, server, log) in Server::each(&keyed()) {
            let asked = anyone().eliciting_call(&server, "update_order", update(json!(2), None), json!({}));
            assert_eq!(asked["resultType"], "input_required", "{way}: {asked}");
            assert_eq!(asked["inputRequests"]["confirm"]["method"], "elicitation/create", "{way}");
            assert!(ran(&log).is_empty(), "{way}");
            let answer = json!({"inputResponses": {"confirm": {"action": "accept"}}, "requestState": asked["requestState"]});
            let updated = anyone().eliciting_call(&server, "update_order", update(json!(2), None), answer);
            assert_eq!(updated["structuredContent"]["revision"], 2.0, "{way}: {updated}");
            assert_eq!(ran(&log).len(), 1, "{way}");
        }
    }

    #[test]
    fn a_call_of_update_order_its_route_would_refuse_is_refused_at_once_and_no_one_is_asked() {
        for (way, server, log) in Server::each(&keyed()) {
            let wrapped = json!({"id": ORDER, "input": {"revision": 2, "note": null}});
            let takes_input = anyone().call(&server, "update_order", wrapped.clone());
            assert_eq!(problem(&takes_input)["status"], 400, "{way}: {takes_input}");
            assert_eq!(problem(&takes_input)["code"], "bad_request", "{way}");
            assert_eq!(
                problem(&takes_input)["detail"],
                "update_order takes id, revision, note, not \"input\"",
                "{way}"
            );
            assert!(problem(&takes_input)["details"].is_null(), "{way}");
            for arguments in [
                json!({"id": ORDER, "revision": "two", "note": null}),
                json!({"id": ORDER, "note": "no revision"}),
                json!({"id": "not an id!", "revision": 2, "note": null}),
            ] {
                let refused = anyone().call(&server, "update_order", arguments.clone());
                assert_eq!(problem(&refused)["status"], 400, "{way}: {arguments}: {refused}");
                assert_eq!(problem(&refused)["code"], "bad_request", "{way}");
                assert!(problem(&refused)["details"]["confirmationToken"].is_null(), "{way}: {refused}");
            }
            // A client that elicits is not asked either.
            let elicited = anyone().eliciting_call(&server, "update_order", wrapped, json!({}));
            assert_eq!(elicited["resultType"], "complete", "{way}: {elicited}");
            assert_eq!(problem(&elicited)["status"], 400, "{way}");
            assert!(elicited.get("requestState").is_none(), "{way}");

            // A token sent with a mis-shaped call is not spent: it runs its
            // own call after.
            let token = token_of(&anyone().call(&server, "update_order", update(json!(2), None)));
            let with_token = anyone().call(&server, "update_order", update(json!("two"), Some(&token)));
            assert_eq!(problem(&with_token)["status"], 400, "{way}: {with_token}");
            assert!(ran(&log).is_empty(), "{way}");
            let updated = anyone().call(&server, "update_order", update(json!(2), Some(&token)));
            assert_eq!(updated["structuredContent"]["revision"], 2.0, "{way}: {updated}");
            assert_eq!(ran(&log).len(), 1, "{way}");
        }
    }

    #[test]
    fn a_tool_that_asks_needs_the_confirmation_key() {
        let build = || {
            std::panic::catch_unwind(|| build_router_with(implementations(&Ran::default()), RouterOptions::default()))
        };
        let panic = build().expect_err("a router with no confirmation key");
        let message = panic.downcast_ref::<String>().expect("a message");
        assert_eq!(
            message,
            "MCP tool update_order of fixture-mcp runs only once the person confirms it, so RouterOptions.mcp.confirmation_key is required: a random secret of at least 32 bytes, the same on every instance (D93)"
        );
    }

    #[test]
    fn the_routes_rules_refuse_a_call_as_they_refuse_a_request_as_a_tool_error() {
        for (way, server, log) in Server::each(&keyed()) {
            let empty = anyone().call(&server, "list_orders", json!({"statuses": []}));
            assert_eq!(problem(&empty)["status"], 400, "{way}: {empty}");
            assert_eq!(problem(&empty)["code"], "bad_request", "{way}");
            let short = anyone().call(
                &server,
                "open_return",
                json!({"requestId": ORDER, "orderId": ORDER, "reason": "", "pickup": {"line1": "a", "city": "b", "postalCode": null}, "labels": {}}),
            );
            assert_eq!(problem(&short)["status"], 400, "{way}: {short}");
            let not_an_id = anyone().call(&server, "get_order", json!({"id": "not an id!"}));
            assert_eq!(problem(&not_an_id)["status"], 400, "{way}: {not_an_id}");
            assert!(ran(&log).is_empty(), "{way}");
        }
    }
}

#[cfg(feature = "guarded")]
mod guarded {
    use super::*;
    use api::runtime::{Authenticator, Principal, ServiceAuthenticator, ServiceCaller};
    use http::request::Parts;

    /// `Authorization: Bearer <subject>[:<permission>,...]` is that subject
    /// holding those permissions; without the header, no caller.
    pub struct Bearer;

    #[async_trait]
    impl Authenticator for Bearer {
        async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
            let token = request
                .headers
                .get(http::header::AUTHORIZATION)
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.strip_prefix("Bearer "));
            Ok(token.map(|token| {
                let (subject, held) = token.split_once(':').unwrap_or((token, ""));
                let held: Vec<&str> = held.split(',').filter(|held| !held.is_empty()).collect();
                Principal::new(subject, held)
            }))
        }
    }

    /// `Service-Authorization: Bearer <api>` is the deployable that serves
    /// that API.
    pub struct Services;

    #[async_trait]
    impl ServiceAuthenticator for Services {
        async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
            Ok(request
                .headers
                .get("service-authorization")
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.strip_prefix("Bearer "))
                .map(|api| ServiceCaller::new(format!("{api}-server"), [api], api)))
        }
    }

    #[test]
    fn the_endpoint_needs_the_caller_a_route_of_the_api_needs() {
        for (way, server, _) in Server::each(&keyed()) {
            let list = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"});
            let (status, refused) = Client::default().send(&server, Method::POST, "/api/mcp", Some(&list));
            assert_eq!(status, StatusCode::UNAUTHORIZED, "{way}: {refused}");
            assert_eq!(refused["code"], "unauthorized", "{way}");
            // A calling service is no end user.
            let service = Client::default().with("service-authorization", "Bearer storefront");
            let (status, _) = service.send(&server, Method::POST, "/api/mcp", Some(&list));
            assert_eq!(status, StatusCode::UNAUTHORIZED, "{way}");
        }
    }

    #[test]
    fn the_list_holds_the_tools_whose_routes_admit_the_caller() {
        for (way, server, _) in Server::each(&keyed()) {
            assert_eq!(
                names(&Client::bearer("ada").tools(&server)),
                ["list_orders", "update_order", "open_return"],
                "{way}"
            );
            assert_eq!(
                names(&Client::bearer("grace:orders.read").tools(&server)),
                ["list_orders", "get_order", "update_order", "open_return"],
                "{way}"
            );
            // A permission covers what is nested under it, as on the route.
            assert!(names(&Client::bearer("root:orders").tools(&server)).contains(&"get_order"), "{way}");
        }
    }

    #[test]
    fn a_call_runs_as_the_caller_its_route_establishes_from_the_endpoints_credentials() {
        for (way, server, log) in Server::each(&keyed()) {
            let ada = Client::bearer("ada:orders.read");
            let got = ada.call(&server, "get_order", json!({"id": ORDER}));
            let id = got["structuredContent"]["id"].as_str().expect("an id").to_owned();
            // A route that needs no caller establishes none.
            ada.call(&server, "list_orders", json!({"statuses": ["open"]}));
            // A listed calling service stands in for the user on
            // openReturn's route; any other caller is the user.
            let pickup = json!({"line1": "1 Main St", "city": "Springfield", "postalCode": "1"});
            let ret = json!({"requestId": ORDER, "orderId": ORDER, "reason": "Too small", "pickup": pickup, "labels": {}});
            ada.call(&server, "open_return", ret.clone());
            let storefront = ada.clone().with("service-authorization", "Bearer storefront");
            storefront.call(&server, "open_return", ret.clone());
            let warehouse = ada.clone().with("service-authorization", "Bearer warehouse");
            warehouse.call(&server, "open_return", ret);
            assert_eq!(
                ran(&log),
                [
                    format!("getOrder {id} as ada via no service"),
                    "listOrders [Open] None".to_owned(),
                    "openReturn Too small Springfield Some(\"1\") {} as ada via no service".to_owned(),
                    "openReturn Too small Springfield Some(\"1\") {} as no one via storefront-server".to_owned(),
                    "openReturn Too small Springfield Some(\"1\") {} as ada via warehouse-server".to_owned(),
                ],
                "{way}"
            );
        }
    }

    #[test]
    fn a_tool_whose_permission_the_caller_lacks_is_no_tool() {
        for (way, server, log) in Server::each(&keyed()) {
            let reply = Client::bearer("ada").rpc(
                &server,
                "tools/call",
                json!({"name": "get_order", "arguments": {"id": ORDER}}),
            );
            assert_eq!(
                reply["error"],
                json!({"code": -32602, "message": "fixture-mcp has no tool \"get_order\""}),
                "{way}"
            );
            assert!(ran(&log).is_empty(), "{way}");
        }
    }

    #[test]
    fn a_confirmation_is_bound_to_its_caller_and_a_misshapen_call_asks_no_one() {
        for (way, server, log) in Server::each(&keyed()) {
            let grace = Client::bearer("grace");
            let refused = grace.call(&server, "update_order", update(json!("two"), None));
            assert_eq!(problem(&refused)["status"], 400, "{way}: {refused}");
            assert!(problem(&refused)["details"]["confirmationToken"].is_null(), "{way}");
            let elicited = grace.eliciting_call(&server, "update_order", update(json!("two"), None), json!({}));
            assert_eq!(elicited["resultType"], "complete", "{way}: {elicited}");
            assert_eq!(problem(&elicited)["status"], 400, "{way}");
            let token = token_of(&grace.call(&server, "update_order", update(json!(2), None)));
            let other = Client::bearer("edsger").call(&server, "update_order", update(json!(2), Some(&token)));
            assert_eq!(problem(&other)["details"]["refused"], "mismatch", "{way}: {other}");
            assert!(ran(&log).is_empty(), "{way}");
            let updated = grace.call(&server, "update_order", update(json!(2), Some(&token)));
            assert_eq!(updated["structuredContent"]["revision"], 2.0, "{way}: {updated}");
            // updateOrder's route needs no caller, so it establishes none:
            // the confirmation is bound to the endpoint's.
            assert_eq!(ran(&log).len(), 1, "{way}");
            assert!(ran(&log)[0].ends_with(" None"), "{way}: {:?}", ran(&log));
        }
    }

    /// updateOrder's route takes one request a minute. A call asked about,
    /// confirmed and run takes one token, since its check takes none
    /// (D98); the next confirmed call is refused at its run, its
    /// confirmation spent. With no transport under the router the limit is
    /// the host's (D116), so both run.
    #[test]
    fn a_call_asked_about_confirmed_and_run_takes_one_token_of_its_routes_rate_limit() {
        for (way, server, log) in Server::each(&keyed()) {
            let grace = Client::bearer("grace");
            let mut answers = Vec::new();
            for revision in [2, 3] {
                let token = token_of(&grace.call(&server, "update_order", update(json!(revision), None)));
                answers.push(grace.call(&server, "update_order", update(json!(revision), Some(&token))));
            }
            assert_eq!(answers[0]["structuredContent"]["revision"], 2.0, "{way}: {}", answers[0]);
            match server {
                Server::Router(_) => {
                    assert_eq!(problem(&answers[1])["status"], 429, "{way}: {}", answers[1]);
                    assert_eq!(problem(&answers[1])["code"], "too_many_requests", "{way}");
                    assert_eq!(ran(&log).len(), 1, "{way}");
                }
                Server::Api(_) => {
                    assert_eq!(answers[1]["structuredContent"]["revision"], 3.0, "{way}: {}", answers[1]);
                    assert_eq!(ran(&log).len(), 2, "{way}");
                }
            }
        }
    }
}
