//! The engine's routes through its axum router (the `axum` feature, D58),
//! on SQLite and, with the variable set, on Postgres: a request in process
//! (tower's `oneshot`) under the router's prefix and under a host's nest,
//! the caller from the host's function, from one that awaits, from the
//! request's extensions and as a refusal, the body limit and MCP's own
//! cap, the host's timeout on every route but the event stream's and
//! MCP's, a method the path does not take, an event stream as a body that
//! pulls frames, wakes for a commit, beats and ends at shutdown, and the
//! same stream over a TCP listener, which a client that goes away, or a
//! shutdown, lets the server drain.
//!
//! The recorded corpus replays through the same router in the test kit
//! (`testkit/tests/http_replay.rs`, with its `axum` feature).
//!
//! Each test opens its engines on the test's thread and serves them on a
//! runtime of its own, making any call of its own on the blocking pool
//! ([`blocking`]), as a server must: on Postgres the engine's client
//! blocks on a runtime of its own, which cannot start on an async thread.

#![cfg(feature = "axum")]

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::http::request::Parts;
use axum::http::{header, HeaderMap, Request, StatusCode};
use axum::Router;
use futures_util::StreamExt;
use serde_json::{json, Value};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tower::ServiceExt;

use superschematic_engine::api::{ApiOptions, Caller, Refusal, StreamOptions};
use superschematic_engine::axum::{router, CallerFuture, Callers, Options, Serves};
use superschematic_engine::{CreateOptions, Engine, Principal};
use support::{alice, order_document, targets};

fn engine(target: &support::Target) -> Arc<Engine> {
    Engine::open_shared(target.listening_options()).expect("open the engine")
}

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .expect("a runtime")
}

// blocking makes an engine call on the blocking pool.
async fn blocking<T: Send + 'static>(
    engine: &Arc<Engine>,
    call: impl FnOnce(&Engine) -> T + Send + 'static,
) -> T {
    let engine = Arc::clone(engine);
    tokio::task::spawn_blocking(move || call(&engine))
        .await
        .expect("the call")
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

// bearer is the host's authentication the tests stand in for: the subject
// a bearer token names, with every permission.
fn bearer(request: &Parts) -> Caller {
    let principal = request
        .headers
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .map(|subject| Principal::new(subject, ["*"]));
    Caller {
        principal,
        ..Caller::default()
    }
}

fn post(path: &str, body: &Value) -> Request<Body> {
    Request::post(path)
        .header(header::AUTHORIZATION, "Bearer alice")
        .header(header::CONTENT_TYPE, "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

fn get(path: &str) -> Request<Body> {
    Request::get(path)
        .header(header::AUTHORIZATION, "Bearer alice")
        .body(Body::empty())
        .unwrap()
}

async fn send(router: &Router, request: Request<Body>) -> (StatusCode, HeaderMap, Value) {
    let response = router.clone().oneshot(request).await.unwrap();
    let (parts, body) = response.into_parts();
    let bytes = axum::body::to_bytes(body, usize::MAX).await.unwrap();
    let body = if bytes.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&bytes)
            .unwrap_or_else(|_| json!({"text": String::from_utf8_lossy(&bytes).into_owned()}))
    };
    (parts.status, parts.headers, body)
}

// define publishes the order schema through the router under `prefix`.
async fn define(router: &Router, prefix: &str) {
    let (status, _, body) = send(
        router,
        post(
            &format!("{prefix}/namespaces/default/schemas"),
            &order_document(),
        ),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let (status, _, body) = send(
        router,
        post(
            &format!("{prefix}/namespaces/default/schemas/Order/publish"),
            &json!({}),
        ),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{body}");
}

#[test]
fn a_write_under_the_prefix_answers_as_the_engine_with_the_hosts_caller() {
    for target in targets("axum_write") {
        let engine = engine(&target);
        runtime().block_on(async {
            let asked = Arc::new(AtomicUsize::new(0));
            let counted = Arc::clone(&asked);
            let routes = router(
                Arc::clone(&engine),
                Options::new("/engine/shop/").callers(move |request: &Parts| {
                    counted.fetch_add(1, Ordering::SeqCst);
                    bearer(request)
                }),
            )
            .unwrap();
            define(&routes, "/engine/shop").await;

            let mut created = post(
                "/engine/shop/namespaces/default/schemas/Order/instances",
                &json!({"id": "o:1", "data": {"title": "Desk"}}),
            );
            created
                .headers_mut()
                .insert("x-request-id", "req-1".parse().unwrap());
            let (status, headers, body) = send(&routes, created).await;
            assert_eq!(status, StatusCode::CREATED, "{body}");
            assert_eq!(
                headers[header::LOCATION],
                "/engine/shop/namespaces/default/schemas/Order/instances/o%3A1"
            );
            assert_eq!(headers[header::ETAG], "\"1\"");
            assert_eq!(headers["x-request-id"], "req-1");
            assert_eq!(body["meta"]["requestId"], "req-1");
            assert_eq!(body["data"]["createdBy"], "alice");

            // A percent-encoded id reaches the engine as sent.
            let (status, _, body) = send(
                &routes,
                get("/engine/shop/namespaces/default/schemas/Order/instances/o%3A1"),
            )
            .await;
            assert_eq!(status, StatusCode::OK, "{body}");
            assert_eq!(body["data"]["id"], "o:1");
            let (status, _, body) = send(
                &routes,
                get("/engine/shop/namespaces/default/schemas/Order/instances/%ZZ"),
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
            assert_eq!(body["details"]["location"], "path");

            // No caller: 401, the engine's.
            let anonymous = Request::get("/engine/shop/namespaces")
                .body(Body::empty())
                .unwrap();
            let (status, headers, body) = send(&routes, anonymous).await;
            assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
            assert_eq!(body["code"], "unauthorized");
            assert_eq!(headers["x-request-id"].len(), 36, "a fresh id");

            // A method no route on the path takes is the engine's 404, and the
            // caller is never asked.
            let before = asked.load(Ordering::SeqCst);
            let put = Request::put("/engine/shop/namespaces/default/schemas/Order/instances/o1")
                .header(header::AUTHORIZATION, "Bearer alice")
                .body(Body::from("{}"))
                .unwrap();
            let (status, headers, body) = send(&routes, put).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{body}");
            assert_eq!(headers[header::CONTENT_TYPE], "application/problem+json");
            assert!(
                !headers.contains_key(header::ALLOW),
                "not axum's 405: {headers:?}"
            );
            assert_eq!(body["code"], "not_found");
            assert_eq!(asked.load(Ordering::SeqCst), before);

            // A path no route names is the host's: this router has no fallback.
            let (status, _, body) = send(&routes, get("/engine/shop/nothing")).await;
            assert_eq!(status, StatusCode::NOT_FOUND);
            assert_eq!(body, Value::Null);
        });
    }
}

#[test]
fn a_host_that_nests_the_router_keeps_its_prefix_in_location() {
    for target in targets("axum_nest") {
        let engine = engine(&target);
        runtime().block_on(async {
            let inner = router(Arc::clone(&engine), Options::new("").callers(bearer)).unwrap();
            let nested = Router::new().nest("/engine/shop", inner);
            define(&nested, "/engine/shop").await;
            let (status, headers, body) = send(
                &nested,
                post(
                    "/engine/shop/namespaces/default/schemas/Order/instances",
                    &json!({"id": "o1", "data": {"title": "Desk"}}),
                ),
            )
            .await;
            assert_eq!(status, StatusCode::CREATED, "{body}");
            assert_eq!(
                headers[header::LOCATION],
                "/engine/shop/namespaces/default/schemas/Order/instances/o1"
            );

            // Nested twice: the router's own prefix under the host's.
            let twice = Router::new().nest(
                "/engine",
                router(Arc::clone(&engine), Options::new("/shop").callers(bearer)).unwrap(),
            );
            let (status, headers, body) = send(
                &twice,
                post(
                    "/engine/shop/namespaces/default/schemas/Order/instances",
                    &json!({"id": "o2", "data": {"title": "Lamp"}}),
                ),
            )
            .await;
            assert_eq!(status, StatusCode::CREATED, "{body}");
            assert_eq!(
                headers[header::LOCATION],
                "/engine/shop/namespaces/default/schemas/Order/instances/o2"
            );
        });
    }
}

#[test]
fn the_caller_is_the_extensions_by_default_and_a_refusal_is_answered() {
    for target in targets("axum_callers") {
        let engine = engine(&target);
        runtime().block_on(async {
            let routes = router(Arc::clone(&engine), Options::default()).unwrap();
            let mut listed = Request::get("/namespaces").body(Body::empty()).unwrap();
            listed.extensions_mut().insert(Caller {
                principal: Some(alice()),
                ..Caller::default()
            });
            let (status, _, body) = send(&routes, listed).await;
            assert_eq!(status, StatusCode::OK, "{body}");
            assert!(!body["data"].is_null(), "{body}");

            let mut refused = Request::get("/namespaces").body(Body::empty()).unwrap();
            refused.extensions_mut().insert(Caller {
                refused: Some(Refusal {
                    status: 401,
                    code: "service_unauthorized".to_owned(),
                    detail: None,
                }),
                ..Caller::default()
            });
            let (status, _, body) = send(&routes, refused).await;
            assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
            assert_eq!(body["code"], "service_unauthorized");

            let (status, _, _) = send(
                &routes,
                Request::get("/namespaces").body(Body::empty()).unwrap(),
            )
            .await;
            assert_eq!(status, StatusCode::UNAUTHORIZED, "no extension: no caller");

            // An authentication that awaits, as the HTTP runtime's do.
            let awaiting =
                router(Arc::clone(&engine), Options::default().callers(Awaited)).unwrap();
            let (status, _, body) = send(&awaiting, get("/namespaces")).await;
            assert_eq!(status, StatusCode::OK, "{body}");
        });
    }
}

// Awaited establishes the caller as `bearer` does, after an await.
struct Awaited;

impl Callers for Awaited {
    fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a> {
        Box::pin(async move {
            tokio::task::yield_now().await;
            bearer(request)
        })
    }
}

#[test]
fn a_body_past_the_limit_is_413_and_mcp_refuses_past_its_own_cap() {
    for target in targets("axum_limits") {
        let engine = engine(&target);
        runtime().block_on(async {
            let routes = router(
                Arc::clone(&engine),
                Options::default().callers(bearer).body_limit(64),
            )
            .unwrap();
            let big = json!({"name": "x".repeat(100)});
            let (status, headers, body) = send(&routes, post("/namespaces", &big)).await;
            assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE, "{body}");
            assert_eq!(body["code"], "payload_too_large");
            assert_eq!(body["detail"], "Request body exceeds the accepted size");
            assert_eq!(body["requestId"], headers["x-request-id"].to_str().unwrap());

            // Refused by its declared length, before a byte is read.
            let declared = Request::post("/namespaces")
                .header(header::AUTHORIZATION, "Bearer alice")
                .header(header::CONTENT_TYPE, "application/json")
                .header(header::CONTENT_LENGTH, "100000")
                .body(Body::from("{}"))
                .unwrap();
            let (status, _, _) = send(&routes, declared).await;
            assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);

            // A body within the limit, and a route that takes none, pass.
            let (status, _, body) =
                send(&routes, post("/namespaces", &json!({"name": "ops"}))).await;
            assert_eq!(status, StatusCode::CREATED, "{body}");

            // MCP reads past the host's limit to its own cap, and refuses past
            // that as JSON-RPC.
            let rpc = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {
                "_meta": {
                    "io.modelcontextprotocol/protocolVersion": "2026-07-28",
                    "io.modelcontextprotocol/clientInfo": {"name": "test", "version": "1"},
                    "io.modelcontextprotocol/clientCapabilities": {},
                },
            }});
            let mut listed = post("/namespaces/default/mcp", &rpc);
            listed.headers_mut().insert(
                header::ACCEPT,
                "application/json, text/event-stream".parse().unwrap(),
            );
            listed
                .headers_mut()
                .insert("mcp-protocol-version", "2026-07-28".parse().unwrap());
            let (status, _, body) = send(&routes, listed).await;
            assert_eq!(status, StatusCode::OK, "{body}");
            assert!(body["result"]["tools"].is_array(), "{body}");

            let huge = Request::post("/namespaces/default/mcp")
                .header(header::AUTHORIZATION, "Bearer alice")
                .header(header::CONTENT_TYPE, "application/json")
                .header(header::ACCEPT, "application/json, text/event-stream")
                .body(Body::from(vec![b' '; 4 * 1024 * 1024 + 1]))
                .unwrap();
            let (status, _, body) = send(&routes, huge).await;
            assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE, "{body}");
            assert_eq!(body["jsonrpc"], "2.0", "{body}");
        });
    }
}

// Slow establishes the caller as `bearer` does, after a wait: a request
// that takes that long in the router's steps.
struct Slow(Duration);

impl Callers for Slow {
    fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a> {
        Box::pin(async move {
            tokio::time::sleep(self.0).await;
            bearer(request)
        })
    }
}

fn mcp_tools_list() -> Request<Body> {
    let rpc = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {
        "_meta": {
            "io.modelcontextprotocol/protocolVersion": "2026-07-28",
            "io.modelcontextprotocol/clientInfo": {"name": "test", "version": "1"},
            "io.modelcontextprotocol/clientCapabilities": {},
        },
    }});
    let mut listed = post("/namespaces/default/mcp", &rpc);
    listed.headers_mut().insert(
        header::ACCEPT,
        "application/json, text/event-stream".parse().unwrap(),
    );
    listed
        .headers_mut()
        .insert("mcp-protocol-version", "2026-07-28".parse().unwrap());
    listed
}

#[test]
fn a_route_past_the_hosts_timeout_is_504_but_the_event_stream_and_mcp_run_on() {
    for target in targets("axum_timeout") {
        let engine = engine(&target);
        runtime().block_on(async {
            let slow = Duration::from_millis(400);
            let routes = router(
                Arc::clone(&engine),
                Options::default()
                    .callers(Slow(slow))
                    .timeout(Duration::from_millis(100)),
            )
            .unwrap();
            // A route of the HTTP API past the timeout: the HTTP runtimes'
            // 504, as Go's enginehttp and TypeScript's engineApp answer it.
            let (status, headers, body) = send(&routes, get("/namespaces")).await;
            assert_eq!(status, StatusCode::GATEWAY_TIMEOUT, "{body}");
            assert_eq!(headers[header::CONTENT_TYPE], "application/problem+json");
            assert_eq!(headers[header::CACHE_CONTROL], "no-store");
            assert_eq!(
                body,
                json!({
                    "type": "about:blank",
                    "title": "Gateway Timeout",
                    "status": 504,
                    "detail": "Gateway Timeout",
                    "code": "gateway_timeout",
                    "requestId": headers["x-request-id"].to_str().unwrap(),
                })
            );
            let (status, _, body) =
                send(&routes, post("/namespaces", &json!({"name": "late"}))).await;
            assert_eq!(status, StatusCode::GATEWAY_TIMEOUT, "{body}");

            // The event stream and MCP, whose responses last as long as
            // their clients, are not the timeout's.
            let response = routes.clone().oneshot(events()).await.unwrap();
            assert_eq!(response.status(), StatusCode::OK);
            assert_eq!(
                response.headers()[header::CONTENT_TYPE],
                "text/event-stream"
            );
            let mut chunks = response.into_body().into_data_stream();
            assert_eq!(next_text(&mut chunks).await.as_deref(), Some(": open\n\n"));
            drop(chunks);
            let (status, _, body) = send(&routes, mcp_tools_list()).await;
            assert_eq!(status, StatusCode::OK, "{body}");
            assert!(body["result"]["tools"].is_array(), "{body}");

            // Within the timeout, and with none, the route answers.
            for options in [
                Options::default()
                    .callers(Slow(slow))
                    .timeout(Duration::from_secs(10)),
                Options::default().callers(Slow(slow)),
            ] {
                let routes = router(Arc::clone(&engine), options).unwrap();
                let (status, _, body) = send(&routes, get("/namespaces")).await;
                assert_eq!(status, StatusCode::OK, "{body}");
            }
        });
    }
}

#[test]
fn a_router_serves_the_routes_it_is_given_and_refuses_what_it_cannot_route() {
    for target in targets("axum_serves") {
        let engine = engine(&target);
        runtime().block_on(async {
            let http = router(
                Arc::clone(&engine),
                Options::new("/api").callers(bearer).serves(Serves::Http),
            )
            .unwrap();
            let mcp = router(
                Arc::clone(&engine),
                Options::new("/api").callers(bearer).serves(Serves::Mcp),
            )
            .unwrap();
            let (status, _, _) = send(&http, post("/api/namespaces/default/mcp", &json!({}))).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "the HTTP API alone");
            let (status, _, _) = send(&mcp, get("/api/namespaces")).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "MCP alone");
            // Both under one prefix, merged, as a host mounts them.
            let both = http.merge(mcp);
            let (status, _, _) = send(&both, get("/api/namespaces")).await;
            assert_eq!(status, StatusCode::OK);

            assert!(router(Arc::clone(&engine), Options::new("/api/{db}")).is_err());
            let options = target.options();
            let unshared =
                tokio::task::spawn_blocking(move || Arc::new(Engine::open(options).unwrap()))
                    .await
                    .unwrap();
            let refused = router(Arc::clone(&unshared), Options::default()).unwrap_err();
            tokio::task::spawn_blocking(move || drop(unshared))
                .await
                .unwrap();
            assert!(refused.0.contains("Engine::shared"), "{refused}");
            let out_of_range = Options::default().api(ApiOptions {
                stream: StreamOptions {
                    page_size: Some(0),
                    heartbeat_ms: None,
                },
                ..ApiOptions::default()
            });
            assert!(router(Arc::clone(&engine), out_of_range).is_err());
        });
    }
}

fn streaming(heartbeat_ms: u64) -> Options {
    Options::default().callers(bearer).api(ApiOptions {
        stream: StreamOptions {
            page_size: Some(1),
            heartbeat_ms: Some(heartbeat_ms),
        },
        ..ApiOptions::default()
    })
}

fn events() -> Request<Body> {
    Request::get("/namespaces/default/events")
        .header(header::AUTHORIZATION, "Bearer alice")
        .header(header::ACCEPT, "text/event-stream")
        .body(Body::empty())
        .unwrap()
}

// next_text is the body's next chunk as text, within a deadline.
async fn next_text<S>(chunks: &mut S) -> Option<String>
where
    S: futures_util::Stream<Item = Result<Bytes, axum::Error>> + Unpin,
{
    let next = tokio::time::timeout(Duration::from_secs(10), chunks.next())
        .await
        .expect("a chunk within the deadline")?;
    Some(String::from_utf8(next.unwrap().to_vec()).unwrap())
}

#[test]
fn an_event_stream_is_a_body_that_wakes_for_a_commit_beats_and_ends_at_shutdown() {
    for target in targets("axum_stream") {
        let engine = engine(&target);
        runtime().block_on(async {
            let routes = router(Arc::clone(&engine), streaming(50)).unwrap();
            define(&routes, "").await;
            blocking(&engine, |engine| create(engine, "o1")).await;

            let response = routes.clone().oneshot(events()).await.unwrap();
            assert_eq!(response.status(), StatusCode::OK);
            assert_eq!(
                response.headers()[header::CONTENT_TYPE],
                "text/event-stream"
            );
            assert_eq!(response.headers()[header::CACHE_CONTROL], "no-store");
            let mut chunks = response.into_body().into_data_stream();
            assert_eq!(next_text(&mut chunks).await.unwrap(), ": open\n\n");
            let mut replay = String::new();
            while !replay.contains("event: ready") {
                replay.push_str(&next_text(&mut chunks).await.unwrap());
            }
            assert_eq!(replay.matches("data: {\"cursor\":").count(), 4, "{replay}");

            // Caught up: the heartbeat, then a commit from another thread.
            assert_eq!(next_text(&mut chunks).await.unwrap(), ": keepalive\n\n");
            let writer = Arc::clone(&engine);
            let wrote = std::thread::spawn(move || {
                std::thread::sleep(Duration::from_millis(20));
                create(&writer, "o2");
            });
            let mut woke = next_text(&mut chunks).await.unwrap();
            while woke == ": keepalive\n\n" {
                woke = next_text(&mut chunks).await.unwrap();
            }
            wrote.join().unwrap();
            assert!(woke.starts_with("id: 4\ndata: "), "{woke}");
            assert!(woke.contains("\"instanceId\":\"o2\""), "{woke}");
            // The client goes away: the body drops, which closes the stream.
            drop(chunks);

            // A second stream, still open at the shutdown, ends with it.
            let response = routes.clone().oneshot(events()).await.unwrap();
            let mut chunks = response.into_body().into_data_stream();
            let mut caught_up = String::new();
            while !caught_up.contains("event: ready") {
                caught_up.push_str(&next_text(&mut chunks).await.unwrap());
            }
            blocking(&engine, Engine::shutdown).await;
            loop {
                match next_text(&mut chunks).await {
                    None => break,
                    Some(text) => assert_eq!(text, ": keepalive\n\n"),
                }
            }
            let (status, _, body) = send(&routes, get("/namespaces")).await;
            assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE, "{body}");
        });
    }
}

#[test]
fn an_event_stream_ends_when_the_host_stops_serving_and_the_engine_serves_on() {
    for target in targets("axum_stream_done") {
        let engine = engine(&target);
        runtime().block_on(async {
            let (stop, done) = tokio::sync::watch::channel(false);
            // A heartbeat longer than the test, so only the host's stop ends
            // the wait.
            let routes = router(Arc::clone(&engine), streaming(60_000).done(done)).unwrap();
            define(&routes, "").await;
            let response = routes.clone().oneshot(events()).await.unwrap();
            let mut chunks = response.into_body().into_data_stream();
            let mut caught_up = String::new();
            while !caught_up.contains("event: ready") {
                caught_up.push_str(&next_text(&mut chunks).await.unwrap());
            }
            stop.send_replace(true);
            assert_eq!(next_text(&mut chunks).await, None);
            // The engine is open: a request in flight at the stop still
            // gets its answer.
            let (status, _, body) = send(&routes, get("/namespaces")).await;
            assert_eq!(status, StatusCode::OK, "{body}");
            blocking(&engine, Engine::shutdown).await;
        });
    }
}

// served starts the router on a listener of its own, with a graceful
// shutdown the returned sender starts; the handle finishes once every
// connection has.
async fn served(
    routes: Router,
) -> (
    std::net::SocketAddr,
    tokio::sync::oneshot::Sender<()>,
    tokio::task::JoinHandle<()>,
) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(async move {
        axum::serve(listener, routes)
            .with_graceful_shutdown(async move {
                let _ = stopped.await;
            })
            .await
            .unwrap();
    });
    (address, stop, server)
}

// open_stream sends the stream's request over a connection of its own and
// reads until replay has caught up.
async fn open_stream(address: std::net::SocketAddr) -> (tokio::net::TcpStream, String) {
    let mut connection = tokio::net::TcpStream::connect(address).await.unwrap();
    connection
        .write_all(
            b"GET /namespaces/default/events HTTP/1.1\r\nHost: engine\r\nAuthorization: Bearer alice\r\nAccept: text/event-stream\r\n\r\n",
        )
        .await
        .unwrap();
    let mut read = String::new();
    let mut buffer = [0u8; 4096];
    while !read.contains("event: ready") {
        let count = tokio::time::timeout(Duration::from_secs(10), connection.read(&mut buffer))
            .await
            .expect("the stream within the deadline")
            .unwrap();
        assert!(count > 0, "the server closed the stream: {read}");
        read.push_str(&String::from_utf8_lossy(&buffer[..count]));
    }
    (connection, read)
}

#[test]
fn over_tcp_a_stream_lets_the_server_drain_once_its_client_goes_or_the_engine_shuts_down() {
    for target in targets("axum_tcp") {
        let engine = engine(&target);
        runtime().block_on(async {
            let routes = router(Arc::clone(&engine), streaming(50)).unwrap();

            // The client goes away: the server finds the connection gone at
            // the next heartbeat, drops the body and so can drain.
            let (address, stop, server) = served(routes.clone()).await;
            let (connection, read) = open_stream(address).await;
            assert!(read.starts_with("HTTP/1.1 200 OK\r\n"), "{read}");
            assert!(read.contains("content-type: text/event-stream"), "{read}");
            assert!(read.contains(": open"), "{read}");
            drop(connection);
            stop.send(()).unwrap();
            tokio::time::timeout(Duration::from_secs(10), server)
                .await
                .expect("the server drained once its client went")
                .unwrap();

            // The engine shuts down: the stream ends, the response with it.
            let (address, stop, server) = served(routes).await;
            let (mut connection, _) = open_stream(address).await;
            stop.send(()).unwrap();
            blocking(&engine, Engine::shutdown).await;
            let mut rest = Vec::new();
            tokio::time::timeout(Duration::from_secs(10), connection.read_to_end(&mut rest))
                .await
                .expect("the stream ended at the shutdown")
                .unwrap();
            // The chunked body's last chunk.
            assert!(String::from_utf8_lossy(&rest).ends_with("0\r\n\r\n"));
            tokio::time::timeout(Duration::from_secs(10), server)
                .await
                .expect("the server drained once the stream ended")
                .unwrap();
        });
    }
}
