//! Trace export against an OTLP/HTTP receiver in the test's process (D67):
//! a server span named by its route and the child of its `traceparent`, a
//! call's client span and the `traceparent` it carries, a job's span and a
//! worker message's, each flushed when the telemetry drops, the variables
//! over the defaults, the refusals, and nothing at all with export off.
//!
//! Export reads the process's environment and sets the global propagator,
//! so the tests run one at a time.

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex, PoisonError};

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::{HeaderMap, Request, StatusCode};
use axum::routing::{get, post};
use axum::Router;
use opentelemetry_proto::tonic::collector::trace::v1::ExportTraceServiceRequest;
use opentelemetry_proto::tonic::common::v1::any_value::Value as AnyValue;
use opentelemetry_proto::tonic::common::v1::KeyValue;
use prost::Message as _;
use superschematic_http_runtime::logging::{self, json_lines_as};
use superschematic_http_runtime::telemetry::{self, Defaults, ServerLayer, Telemetry};
use superschematic_http_runtime::OperationInfo;
use tokio::sync::MutexGuard;
use tower::{Layer as _, ServiceExt as _};

/// The variables a test clears, so the developer's shell does not turn
/// export on or change it.
const VARIABLES: [&str; 11] = [
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
    "OTEL_EXPORTER_OTLP_PROTOCOL",
    "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
    "OTEL_EXPORTER_OTLP_HEADERS",
    "OTEL_SERVICE_NAME",
    "OTEL_RESOURCE_ATTRIBUTES",
    "OTEL_TRACES_SAMPLER",
    "OTEL_TRACES_SAMPLER_ARG",
    "OTEL_PROPAGATORS",
    "OTEL_SDK_DISABLED",
];

static SERIAL: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

/// Holds the tests apart, with the variables cleared.
async fn serial() -> MutexGuard<'static, ()> {
    let guard = SERIAL.lock().await;
    clear();
    guard
}

/// [`serial`] for a test that runs on no runtime.
fn serial_blocking() -> MutexGuard<'static, ()> {
    let guard = SERIAL.blocking_lock();
    clear();
    guard
}

fn clear() {
    for name in VARIABLES {
        std::env::remove_var(name);
    }
}

const DEFAULTS: Defaults<'static> = Defaults {
    stack: "shop-stack",
    kind: logging::SERVER,
    name: "shop-stock",
};

/// A span the receiver took, its IDs in lower-case hex.
#[derive(Clone, Debug)]
struct Span {
    trace_id: String,
    span_id: String,
    parent_span_id: String,
    name: String,
    kind: i32,
    status: i32,
    attributes: HashMap<String, String>,
    resource: HashMap<String, String>,
}

const KIND_INTERNAL: i32 = 1;
const KIND_SERVER: i32 = 2;
const KIND_CLIENT: i32 = 3;
#[cfg(feature = "postgres")]
const KIND_CONSUMER: i32 = 5;
const STATUS_ERROR: i32 = 2;

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn attributes(kvs: &[KeyValue]) -> HashMap<String, String> {
    kvs.iter()
        .map(|kv| {
            let value = match kv.value.as_ref().and_then(|v| v.value.as_ref()) {
                Some(AnyValue::StringValue(s)) => s.clone(),
                Some(AnyValue::IntValue(i)) => i.to_string(),
                Some(AnyValue::BoolValue(b)) => b.to_string(),
                Some(AnyValue::DoubleValue(d)) => d.to_string(),
                other => format!("{other:?}"),
            };
            (kv.key.clone(), value)
        })
        .collect()
}

/// An OTLP/HTTP receiver on a thread and runtime of its own, so it answers
/// while a test's thread blocks on the flush.
struct Receiver {
    url: String,
    spans: Arc<Mutex<Vec<Span>>>,
}

impl Receiver {
    fn spans(&self) -> Vec<Span> {
        self.spans
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
    }

    fn only(&self, kind: i32) -> Span {
        let spans: Vec<Span> = self
            .spans()
            .into_iter()
            .filter(|s| s.kind == kind)
            .collect();
        assert_eq!(spans.len(), 1, "spans of kind {kind}: {spans:#?}");
        spans[0].clone()
    }
}

async fn take(
    State(spans): State<Arc<Mutex<Vec<Span>>>>,
    headers: HeaderMap,
    body: Bytes,
) -> StatusCode {
    if headers.get("content-type").and_then(|v| v.to_str().ok()) != Some("application/x-protobuf") {
        return StatusCode::BAD_REQUEST;
    }
    let Ok(export) = ExportTraceServiceRequest::decode(body) else {
        return StatusCode::BAD_REQUEST;
    };
    let mut taken = spans.lock().unwrap_or_else(PoisonError::into_inner);
    for resource_spans in export.resource_spans {
        let resource = attributes(
            resource_spans
                .resource
                .as_ref()
                .map_or(&[][..], |r| &r.attributes[..]),
        );
        for scope_spans in resource_spans.scope_spans {
            for span in scope_spans.spans {
                taken.push(Span {
                    trace_id: hex(&span.trace_id),
                    span_id: hex(&span.span_id),
                    parent_span_id: hex(&span.parent_span_id),
                    name: span.name.clone(),
                    kind: span.kind,
                    status: span.status.as_ref().map_or(0, |s| s.code),
                    attributes: attributes(&span.attributes),
                    resource: resource.clone(),
                });
            }
        }
    }
    StatusCode::OK
}

/// Serves `app` on a thread and runtime of its own; returns its address.
fn serve(app: Router) -> SocketAddr {
    let (sent, address) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("runtime");
        runtime.block_on(async move {
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
                .await
                .expect("bind");
            sent.send(listener.local_addr().expect("address"))
                .expect("send");
            axum::serve(listener, app).await.expect("serve");
        });
    });
    address.recv().expect("the server's address")
}

fn receiver() -> Receiver {
    let spans = Arc::new(Mutex::new(Vec::new()));
    let app = Router::new()
        .route("/v1/traces", post(take))
        .with_state(Arc::clone(&spans));
    Receiver {
        url: format!("http://{}", serve(app)),
        spans,
    }
}

/// Points export at a receiver of the test's own and starts it, with the
/// log's lines kept in `lines`; the dispatcher is the thread's while the
/// guard lives.
fn start(
    defaults: Defaults<'_>,
) -> (
    Receiver,
    Telemetry,
    tracing::dispatcher::DefaultGuard,
    Arc<Mutex<Vec<String>>>,
) {
    let receiver = receiver();
    std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", &receiver.url);
    let lines = Arc::new(Mutex::new(Vec::new()));
    let kept = Arc::clone(&lines);
    let json =
        json_lines_as(defaults.stack, defaults.kind, defaults.name).with_writer(move |line| {
            kept.lock()
                .unwrap_or_else(PoisonError::into_inner)
                .push(line.to_string());
        });
    let (telemetry, dispatch) = telemetry::dispatch(defaults, json).expect("telemetry");
    let guard = tracing::dispatcher::set_default(&dispatch);
    (receiver, telemetry, guard, lines)
}

const OPERATIONS: &[OperationInfo] = &[
    OperationInfo {
        namespace: "Orders",
        name: "getOrder",
        method: "GET",
        path: "/api/orders/{id}",
        requires_auth: false,
        permissions: &[],
        require_ownership: false,
        manual: false,
    },
    OperationInfo {
        namespace: "Orders",
        name: "mine",
        method: "GET",
        path: "/api/orders/mine",
        requires_auth: false,
        permissions: &[],
        require_ownership: false,
        manual: false,
    },
];

const TRACEPARENT: &str = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";

async fn send(app: Router, path: &str) -> StatusCode {
    let request = Request::get(path)
        .header("traceparent", TRACEPARENT)
        .body(Body::empty())
        .expect("request");
    ServerLayer::new(&[OPERATIONS])
        .layer(app)
        .oneshot(request)
        .await
        .expect("infallible")
        .status()
}

#[tokio::test]
async fn with_no_endpoint_nothing_starts() {
    let _serial = serial().await;
    let (telemetry, dispatch) = telemetry::dispatch(
        DEFAULTS,
        json_lines_as("shop-stack", logging::SERVER, "shop-stock"),
    )
    .expect("telemetry");
    let _guard = tracing::dispatcher::set_default(&dispatch);
    assert!(!telemetry.enabled());
    assert!(!telemetry::enabled());
    assert!(
        telemetry::transport().is_none(),
        "no transport wraps a call"
    );
    let answer: Result<u8, String> = telemetry::job("Nightly", async { Ok(7) }).await;
    assert_eq!(answer, Ok(7));
    let seen = Arc::new(Mutex::new(None));
    let saw = Arc::clone(&seen);
    let app = Router::new().route(
        "/api/orders/{id}",
        get(move |headers: HeaderMap| {
            *saw.lock().unwrap_or_else(PoisonError::into_inner) =
                Some(tracing::Span::current().is_none());
            let _ = headers;
            async { StatusCode::OK }
        }),
    );
    assert_eq!(send(app, "/api/orders/1").await, StatusCode::OK);
    assert_eq!(
        *seen.lock().unwrap_or_else(PoisonError::into_inner),
        Some(true),
        "the request runs in no span"
    );
}

#[tokio::test]
async fn disabled_starts_nothing() {
    let _serial = serial().await;
    let receiver = receiver();
    std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", &receiver.url);
    std::env::set_var("OTEL_SDK_DISABLED", "TRUE");
    let (telemetry, _dispatch) = telemetry::dispatch(
        DEFAULTS,
        json_lines_as("shop-stack", logging::SERVER, "shop-stock"),
    )
    .expect("telemetry");
    assert!(!telemetry.enabled());
    drop(telemetry);
    assert!(receiver.spans().is_empty());
}

#[tokio::test]
async fn a_request_is_a_server_span_named_by_its_route() {
    let _serial = serial().await;
    let (receiver, telemetry, guard, lines) = start(DEFAULTS);
    let app = Router::new()
        .route(
            "/api/orders/{id}",
            get(|| async {
                tracing::info!(order = 42, "handled");
                StatusCode::NO_CONTENT
            }),
        )
        .route("/api/orders/mine", get(|| async { StatusCode::OK }))
        .route("/healthz", get(|| async { StatusCode::OK }));
    assert_eq!(
        send(app.clone(), "/api/orders/42").await,
        StatusCode::NO_CONTENT
    );
    assert_eq!(send(app.clone(), "/api/orders/mine").await, StatusCode::OK);
    assert_eq!(send(app.clone(), "/healthz").await, StatusCode::OK);
    assert_eq!(send(app, "/api/nothing").await, StatusCode::NOT_FOUND);
    drop(guard);
    drop(telemetry);

    let spans = receiver.spans();
    assert_eq!(spans.len(), 3, "the probe makes no span: {spans:#?}");
    let by_path = |path: &str| {
        spans
            .iter()
            .find(|s| s.attributes.get("url.path").map(String::as_str) == Some(path))
            .cloned()
            .unwrap_or_else(|| panic!("no span of {path}: {spans:#?}"))
    };
    let span = by_path("/api/orders/42");
    assert_eq!(span.name, "GET /api/orders/{id}");
    assert_eq!(span.kind, KIND_SERVER);
    assert_eq!(span.attributes["http.route"], "/api/orders/{id}");
    assert_eq!(span.attributes["http.response.status_code"], "204");
    assert_eq!(span.trace_id, "4bf92f3577b34da6a3ce929d0e0e4736");
    assert_eq!(span.parent_span_id, "00f067aa0ba902b7");
    assert_eq!(span.resource["service.name"], "shop-stock");
    assert_eq!(span.resource["service.namespace"], "shop-stack");
    assert_eq!(span.resource["superschematic.deployable.kind"], "server");
    assert_eq!(
        by_path("/api/orders/mine").name,
        "GET /api/orders/mine",
        "the most literal route names it"
    );
    assert_eq!(by_path("/api/nothing").name, "GET");

    // The log's line in the span carries none of OpenTelemetry's fields.
    let lines = lines.lock().unwrap_or_else(PoisonError::into_inner);
    let handled: Vec<&String> = lines
        .iter()
        .filter(|line| line.contains("\"handled\""))
        .collect();
    assert_eq!(handled.len(), 1, "{lines:#?}");
    assert!(!handled[0].contains("otel."), "{}", handled[0]);
}

#[tokio::test]
async fn a_call_carries_the_trace_in_a_client_span() {
    let _serial = serial().await;
    let (receiver, telemetry, guard, _lines) = start(DEFAULTS);
    let seen = Arc::new(Mutex::new(None::<String>));
    let saw = Arc::clone(&seen);
    let downstream = serve(Router::new().route(
        "/api/stock",
        post(move |headers: HeaderMap| {
            *saw.lock().unwrap_or_else(PoisonError::into_inner) = headers
                .get("traceparent")
                .and_then(|v| v.to_str().ok())
                .map(str::to_string);
            async { StatusCode::OK }
        }),
    ));
    let transport = telemetry::transport().expect("a transport with export on");
    let app = Router::new().route(
        "/api/orders/{id}",
        get(move || {
            let transport = Arc::clone(&transport);
            async move {
                let client = reqwest::Client::new();
                let request = client
                    .post(format!("http://{downstream}/api/stock"))
                    .build()
                    .expect("request");
                match transport(client, request).await {
                    Ok(response) => response.status(),
                    Err(_) => StatusCode::BAD_GATEWAY,
                }
            }
        }),
    );
    assert_eq!(send(app, "/api/orders/7").await, StatusCode::OK);
    drop(guard);
    drop(telemetry);

    let server = receiver.only(KIND_SERVER);
    let call = receiver.only(KIND_CLIENT);
    assert_eq!(call.name, "POST");
    assert_eq!(call.trace_id, server.trace_id);
    assert_eq!(
        call.parent_span_id, server.span_id,
        "the call is the request's child"
    );
    let traceparent = seen
        .lock()
        .unwrap_or_else(PoisonError::into_inner)
        .clone()
        .expect("the call carried a traceparent");
    let parts: Vec<&str> = traceparent.split('-').collect();
    assert_eq!(parts.len(), 4, "{traceparent}");
    assert_eq!(parts[1], call.trace_id);
    assert_eq!(
        parts[2], call.span_id,
        "the callee's parent is the client span"
    );
}

#[tokio::test]
async fn the_variables_override_the_defaults() {
    let _serial = serial().await;
    std::env::set_var("OTEL_SERVICE_NAME", "stock");
    std::env::set_var(
        "OTEL_RESOURCE_ATTRIBUTES",
        "service.namespace=shop,deployment.environment.name=Dev",
    );
    let (receiver, telemetry, guard, _lines) = start(Defaults {
        stack: "shop-stack",
        kind: logging::JOB,
        name: "shop-stock-order-restocks",
    });
    let ran: Result<(), String> = telemetry::job("OrderRestocks", async { Ok(()) }).await;
    assert!(ran.is_ok());
    drop(guard);
    drop(telemetry);
    let span = receiver.only(KIND_INTERNAL);
    assert_eq!(span.resource["service.name"], "stock");
    assert_eq!(span.resource["service.namespace"], "shop");
    assert_eq!(span.resource["deployment.environment.name"], "Dev");
    assert_eq!(span.resource["superschematic.deployable.kind"], "job");
}

#[tokio::test]
async fn a_job_run_is_a_span_flushed_when_the_telemetry_drops() {
    let _serial = serial().await;
    let (receiver, telemetry, guard, _lines) = start(Defaults {
        stack: "shop-stack",
        kind: logging::JOB,
        name: "shop-stock-order-restocks",
    });
    let ran: Result<(), String> = telemetry::job("OrderRestocks", async {
        Err("the supplier is down".to_string())
    })
    .await;
    assert!(ran.is_err());
    drop(guard);
    // The batch is due five seconds after the span ends: only the drop
    // sends it, before it returns, as a job's process exits after it.
    assert!(receiver.spans().is_empty());
    telemetry.shutdown();
    let span = receiver.only(KIND_INTERNAL);
    assert_eq!(span.name, "OrderRestocks");
    assert_eq!(span.attributes["superschematic.job"], "OrderRestocks");
    assert_eq!(span.status, STATUS_ERROR);
}

#[cfg(feature = "postgres")]
#[tokio::test]
async fn each_message_is_a_consumer_span() {
    use std::collections::VecDeque;
    use std::time::Duration;

    use superschematic_http_runtime::queue::{Claim, QueueError};
    use superschematic_http_runtime::worker::{run_worker, Options, WorkerQueue};
    use superschematic_http_runtime::{BoxError, Shutdown};

    struct Memory {
        ready: Mutex<VecDeque<Claim<String>>>,
        settled: Arc<Mutex<Vec<String>>>,
    }

    #[async_trait::async_trait]
    impl WorkerQueue for Memory {
        type Message = String;
        fn name(&self) -> &str {
            "StockLow"
        }
        fn lease(&self) -> Duration {
            Duration::from_secs(60)
        }
        async fn claim(
            &self,
            n: usize,
            _lease: Duration,
        ) -> Result<Vec<Claim<String>>, QueueError> {
            let mut ready = self.ready.lock().unwrap_or_else(PoisonError::into_inner);
            let n = n.min(ready.len());
            Ok(ready.drain(..n).collect())
        }
        async fn extend(
            &self,
            _id: &str,
            _token: &str,
            _lease: Duration,
        ) -> Result<(), QueueError> {
            Ok(())
        }
        async fn complete(&self, id: &str, _token: &str) -> Result<(), QueueError> {
            self.settled
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .push(id.to_string());
            Ok(())
        }
        async fn fail(&self, id: &str, _token: &str, _cause: &str) -> Result<bool, QueueError> {
            self.settled
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .push(id.to_string());
            Ok(false)
        }
        async fn release(&self, _id: &str, _token: &str) -> Result<(), QueueError> {
            Ok(())
        }
    }

    let _serial = serial().await;
    let (receiver, telemetry, guard, _lines) = start(Defaults {
        stack: "shop-stack",
        kind: logging::WORKER,
        name: "shop-stock-reorder-stock",
    });
    let settled = Arc::new(Mutex::new(Vec::new()));
    let claim = |id: &str, message: &str| Claim {
        id: id.to_string(),
        token: format!("token-{id}"),
        attempt: 1,
        message: message.to_string(),
    };
    let queue = Memory {
        ready: Mutex::new(VecDeque::from([claim("m-1", "ok"), claim("m-2", "fail")])),
        settled: Arc::clone(&settled),
    };
    let shutdown = Shutdown::new();
    let stop = shutdown.clone();
    let watched = Arc::clone(&settled);
    tokio::spawn(async move {
        while watched.lock().unwrap_or_else(PoisonError::into_inner).len() < 2 {
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
        stop.cancel();
    });
    let handle = |message: String| async move {
        if message == "fail" {
            return Err::<(), BoxError>("no supplier".into());
        }
        Ok(())
    };
    let options = Options {
        concurrency: 2,
        idle: Duration::from_millis(5),
        ..Options::default()
    };
    run_worker(queue, handle, options, shutdown)
        .await
        .expect("the worker ran");
    drop(guard);
    drop(telemetry);

    let spans: Vec<Span> = receiver
        .spans()
        .into_iter()
        .filter(|s| s.kind == KIND_CONSUMER)
        .collect();
    assert_eq!(spans.len(), 2, "one span per message: {spans:#?}");
    for span in &spans {
        assert_eq!(span.name, "process StockLow");
        assert_eq!(
            span.parent_span_id, "",
            "a message's span is a trace's root"
        );
        assert_eq!(span.attributes["messaging.destination.name"], "StockLow");
    }
    let failed = spans
        .iter()
        .find(|s| s.attributes["messaging.message.id"] == "m-2")
        .expect("m-2's span");
    assert_eq!(failed.status, STATUS_ERROR);
}

#[test]
fn unsupported_values_are_refused_naming_the_variable() {
    let _serial = serial_blocking();
    let cases = [
        (
            "OTEL_EXPORTER_OTLP_PROTOCOL",
            "grpc",
            "OTEL_EXPORTER_OTLP_PROTOCOL is \"grpc\"",
        ),
        (
            "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
            "http/json",
            "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL is \"http/json\"",
        ),
        (
            "OTEL_PROPAGATORS",
            "tracecontext,b3",
            "OTEL_PROPAGATORS names \"b3\"",
        ),
        (
            "OTEL_TRACES_SAMPLER",
            "jaeger_remote",
            "OTEL_TRACES_SAMPLER is \"jaeger_remote\"",
        ),
        (
            "OTEL_EXPORTER_OTLP_ENDPOINT",
            "localhost:4318",
            "OTEL_EXPORTER_OTLP_ENDPOINT is \"localhost:4318\"",
        ),
        (
            "OTEL_RESOURCE_ATTRIBUTES",
            "stack",
            "OTEL_RESOURCE_ATTRIBUTES holds \"stack\"",
        ),
    ];
    for (name, value, want) in cases {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318");
        std::env::set_var(name, value);
        let err = telemetry::dispatch(DEFAULTS, json_lines_as("s", logging::SERVER, "n"))
            .expect_err(name)
            .to_string();
        assert!(err.contains(want), "{err}");
        assert!(!telemetry::enabled());
        std::env::remove_var(name);
    }
    std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318");
    std::env::set_var("OTEL_TRACES_SAMPLER", "traceidratio");
    std::env::set_var("OTEL_TRACES_SAMPLER_ARG", "2");
    let err = telemetry::dispatch(DEFAULTS, json_lines_as("s", logging::SERVER, "n"))
        .expect_err("a ratio of 2")
        .to_string();
    assert!(err.contains("OTEL_TRACES_SAMPLER_ARG is \"2\""), "{err}");
}

#[tokio::test]
async fn the_sampler_and_the_propagators_are_read() {
    let _serial = serial().await;
    std::env::set_var("OTEL_TRACES_SAMPLER", "always_off");
    std::env::set_var("OTEL_PROPAGATORS", "none");
    let (receiver, telemetry, guard, _lines) = start(DEFAULTS);
    let seen = Arc::new(Mutex::new(Some(String::new())));
    let saw = Arc::clone(&seen);
    let downstream = serve(Router::new().route(
        "/",
        get(move |headers: HeaderMap| {
            *saw.lock().unwrap_or_else(PoisonError::into_inner) = headers
                .get("traceparent")
                .and_then(|v| v.to_str().ok())
                .map(str::to_string);
            async { StatusCode::OK }
        }),
    ));
    let client = reqwest::Client::new();
    let request = client
        .get(format!("http://{downstream}/"))
        .build()
        .expect("request");
    let transport = telemetry::transport().expect("a transport with export on");
    let status = transport(client, request).await.expect("the call").status();
    assert_eq!(status, StatusCode::OK);
    let ran: Result<(), String> = telemetry::job("OrderRestocks", async { Ok(()) }).await;
    assert!(ran.is_ok());
    drop(guard);
    drop(telemetry);
    assert_eq!(
        *seen.lock().unwrap_or_else(PoisonError::into_inner),
        None,
        "none propagates nothing"
    );
    assert!(receiver.spans().is_empty(), "always_off samples nothing");
}
