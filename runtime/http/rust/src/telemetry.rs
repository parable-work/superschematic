//! Trace export of a stack's Rust server, job or worker over OTLP (D67;
//! section 8.6 of `docs/stack-model.md`), with the `otel` feature. The
//! generated `main.rs` calls [`start`] once, in place of
//! [`logging::init_json_as`](crate::logging::init_json_as), with what it
//! knows of its deployable, and everything else comes from OpenTelemetry's
//! own environment variables:
//!
//! - `OTEL_EXPORTER_OTLP_ENDPOINT`, or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`,
//!   turns export on. Without either, [`start`] installs the JSON-line log
//!   alone: no exporter, no background work, and no OpenTelemetry layer, so
//!   [`ServerLayer`] calls through, [`transport`] is `None` and [`job`]
//!   only awaits.
//! - `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TIMEOUT` and their
//!   per-signal forms reach the exporter, which reads them itself.
//! - `OTEL_EXPORTER_OTLP_PROTOCOL`, and its traces form, may only name
//!   `http/protobuf`, the one protocol the runtime exports over.
//! - `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` override the
//!   defaults the entrypoint gives.
//! - `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG` pick the sampler,
//!   `parentbased_always_on` unless set.
//! - `OTEL_PROPAGATORS` picks the propagators, `tracecontext` and `baggage`
//!   unless set.
//! - `OTEL_SDK_DISABLED=true` turns export off whatever else is set.
//!
//! A value the runtime does not support is a [`TelemetryError`] naming the
//! variable, as a refused config field stops a server.
//!
//! With export on, the log's subscriber gains `tracing-opentelemetry`'s
//! layer, so a span the runtime opens, and a span the implementation opens
//! inside it, is an OpenTelemetry span. The layer takes no event, and none
//! of the runtime's spans that live as long as the process, an API's
//! ([`api_span`](crate::logging::api_span)) or a worker's loop. The
//! runtime's spans carry their OpenTelemetry attributes beside the fields
//! of `tracing`, so the log's lines are the same with export on or off.
//! [`ServerLayer`] makes a server span per request but `/healthz` and
//! `/readyz`, the child of an incoming `traceparent`, named by its method
//! and the template of the operation whose route takes it; [`transport`]
//! a client span per call a `calls` edge's SDK client makes, whose context
//! the call carries in `traceparent` and `tracestate`; [`job`] a span per
//! run of a job; and the worker loop a span per message. [`Telemetry`]
//! flushes the spans still buffered when it is dropped, so a job's run
//! reaches the collector before the process exits.
//!
//! The exporter sends through the `http-client` feature's `reqwest`, on a
//! current-thread runtime of its own, from the batch processor's thread, so
//! export runs on whatever runtime the deployable runs.

use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, PoisonError};
use std::task::{Context, Poll};
use std::time::Duration;

use async_trait::async_trait;
use bytes::Bytes;
use http::{Request, Response};
use opentelemetry::propagation::{TextMapCompositePropagator, TextMapPropagator};
use opentelemetry::trace::{Status, TracerProvider as _};
use opentelemetry::{global, KeyValue};
use opentelemetry_http::{HeaderExtractor, HeaderInjector, HttpClient, HttpError};
use opentelemetry_otlp::{Protocol, WithExportConfig as _, WithHttpConfig as _};
use opentelemetry_sdk::propagation::{BaggagePropagator, TraceContextPropagator};
use opentelemetry_sdk::resource::{EnvResourceDetector, TelemetryResourceDetector};
use opentelemetry_sdk::trace::{Sampler, SdkTracerProvider};
use opentelemetry_sdk::Resource;
use tower_layer::Layer;
use tower_service::Service;
use tracing::{Dispatch, Instrument as _, Metadata};
use tracing_opentelemetry::OpenTelemetrySpanExt as _;
use tracing_subscriber::filter::filter_fn;
use tracing_subscriber::layer::{Layer as _, SubscriberExt as _};

use crate::logging::{self, JsonLines};
use crate::OperationInfo;

/// The one OTLP protocol the runtime exports over.
pub const PROTOCOL: &str = "http/protobuf";

/// The resource attribute that names the deployable's kind.
pub const KIND_ATTRIBUTE: &str = "superschematic.deployable.kind";

/// The span attribute that names the job a job's span runs.
pub const JOB_ATTRIBUTE: &str = "superschematic.job";

/// The target of the runtime's spans, which the OpenTelemetry layer takes.
const TARGET: &str = module_path!();

/// The instrumentation scope of the spans.
const SCOPE: &str = "superschematic-http-runtime";

/// How long dropping [`Telemetry`] waits for the spans still buffered to
/// reach the collector.
const FLUSH_TIMEOUT: Duration = Duration::from_secs(5);

/// How long one export may take when `OTEL_EXPORTER_OTLP_TIMEOUT` does not
/// say: OpenTelemetry's default.
const EXPORT_TIMEOUT: Duration = Duration::from_secs(10);

/// The paths [`ServerLayer`] makes no span for: the platform's probes.
const HEALTH_PATHS: [&str; 2] = ["/healthz", "/readyz"];

/// The runtime's spans that live as long as the process, which the
/// OpenTelemetry layer leaves out, by target and name: they would hold
/// every request's span as a child of one span hours long.
const PROCESS_SPANS: [(&str, &str); 2] = [
    ("superschematic_http_runtime::logging", "api"),
    ("superschematic_http_runtime::worker", "worker"),
];

/// Whether [`start`] turned export on.
static ENABLED: AtomicBool = AtomicBool::new(false);

/// What the generated entrypoint knows of its deployable: the log's fields
/// and the resource's defaults, which `OTEL_SERVICE_NAME` and
/// `OTEL_RESOURCE_ATTRIBUTES` override.
#[derive(Clone, Copy, Debug)]
pub struct Defaults<'a> {
    /// The stack's name: `service.namespace`, and `stack` on each line.
    pub stack: &'a str,
    /// The deployable's kind, [`logging::SERVER`], [`logging::JOB`] or
    /// [`logging::WORKER`]: `superschematic.deployable.kind`, and the field
    /// that names the deployable on each line.
    pub kind: &'a str,
    /// The deployable's name, the default `service.name`.
    pub name: &'a str,
}

/// Why trace export could not start.
#[derive(Debug)]
pub enum TelemetryError {
    /// Variables hold values the runtime does not support, each named.
    Config(Vec<String>),
    /// The exporter could not be built.
    Exporter(String),
    /// The process already has a subscriber.
    Subscriber(tracing::dispatcher::SetGlobalDefaultError),
}

impl fmt::Display for TelemetryError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            TelemetryError::Config(problems) => f.write_str(&problems.join("; ")),
            TelemetryError::Exporter(err) => write!(f, "the OTLP exporter: {err}"),
            TelemetryError::Subscriber(err) => write!(f, "install the logger: {err}"),
        }
    }
}

impl std::error::Error for TelemetryError {}

/// Trace export while it runs: dropping it, or [`Telemetry::shutdown`],
/// flushes the spans still buffered and stops the exporter.
#[must_use = "dropping it stops trace export"]
pub struct Telemetry {
    provider: Option<SdkTracerProvider>,
}

impl fmt::Debug for Telemetry {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Telemetry")
            .field("enabled", &self.provider.is_some())
            .finish()
    }
}

impl Telemetry {
    /// Whether export is on.
    pub fn enabled(&self) -> bool {
        self.provider.is_some()
    }

    /// Flushes the spans still buffered, within five seconds, and stops the
    /// exporter. It blocks the thread it runs on.
    pub fn shutdown(mut self) {
        self.stop();
    }

    fn stop(&mut self) {
        let Some(provider) = self.provider.take() else {
            return;
        };
        if let Err(err) = provider.shutdown_with_timeout(FLUSH_TIMEOUT) {
            tracing::warn!(error = %err, "flush the traces");
        }
        ENABLED.store(false, Ordering::SeqCst);
        global::set_text_map_propagator(TextMapCompositePropagator::new(Vec::new()));
    }
}

impl Drop for Telemetry {
    fn drop(&mut self) {
        self.stop();
    }
}

/// Installs the deployable's log, [`logging::json_lines_as`], for the
/// process, with the OpenTelemetry layer beside it when the environment
/// turns export on. It fails when a variable holds a value the runtime does
/// not support, or when the process already has a subscriber.
pub fn start(defaults: Defaults<'_>) -> Result<Telemetry, TelemetryError> {
    let json = logging::json_lines_as(defaults.stack, defaults.kind, defaults.name);
    let (telemetry, dispatch) = dispatch(defaults, json)?;
    tracing::dispatcher::set_global_default(dispatch).map_err(TelemetryError::Subscriber)?;
    if telemetry.enabled() {
        tracing::info!(endpoint = %endpoint(&env), protocol = PROTOCOL, "exporting traces");
    }
    Ok(telemetry)
}

/// [`start`] without installing the subscriber: the [`Dispatch`] of `json`
/// and, with export on, the OpenTelemetry layer, for a test or a process
/// that installs its subscriber itself.
pub fn dispatch(
    defaults: Defaults<'_>,
    json: JsonLines,
) -> Result<(Telemetry, Dispatch), TelemetryError> {
    let config = read_config(&env)?;
    let Some(config) = config else {
        let dispatch = Dispatch::new(tracing_subscriber::registry().with(json));
        return Ok((Telemetry { provider: None }, dispatch));
    };
    let mut resource = Resource::builder_empty()
        .with_detector(Box::new(TelemetryResourceDetector))
        .with_attributes([
            KeyValue::new("service.name", defaults.name.to_string()),
            KeyValue::new("service.namespace", defaults.stack.to_string()),
            KeyValue::new(KIND_ATTRIBUTE, defaults.kind.to_string()),
        ])
        .with_detector(Box::new(EnvResourceDetector::new()));
    if let Some(name) = env("OTEL_SERVICE_NAME") {
        resource = resource.with_service_name(name);
    }
    let exporter = opentelemetry_otlp::SpanExporter::builder()
        .with_http()
        .with_protocol(Protocol::HttpBinary)
        .with_http_client(ExportClient::new(config.timeout)?)
        .build()
        .map_err(|err| TelemetryError::Exporter(err.to_string()))?;
    let provider = SdkTracerProvider::builder()
        .with_batch_exporter(exporter)
        .with_sampler(config.sampler)
        .with_resource(resource.build())
        .build();
    let otel = tracing_opentelemetry::layer()
        .with_tracer(provider.tracer(SCOPE))
        .with_location(false)
        .with_threads(false)
        .with_tracked_inactivity(false)
        .with_level(false)
        .with_target(false)
        .with_filter(filter_fn(takes));
    let dispatch = Dispatch::new(tracing_subscriber::registry().with(json).with(otel));
    global::set_text_map_propagator(config.propagator);
    ENABLED.store(true, Ordering::SeqCst);
    Ok((
        Telemetry {
            provider: Some(provider),
        },
        dispatch,
    ))
}

/// Whether [`start`] turned export on.
pub fn enabled() -> bool {
    ENABLED.load(Ordering::SeqCst)
}

/// Whether the OpenTelemetry layer takes a callsite: a span, but the
/// runtime's spans that live as long as the process.
fn takes(metadata: &Metadata<'_>) -> bool {
    metadata.is_span()
        && !PROCESS_SPANS
            .iter()
            .any(|(target, name)| metadata.target() == *target && metadata.name() == *name)
}

/// A set variable's value, trimmed; `None` when it is unset or blank.
fn env(name: &str) -> Option<String> {
    std::env::var(name)
        .ok()
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
}

/// The endpoint export sends to, as its variable names it.
fn endpoint(env: &dyn Fn(&str) -> Option<String>) -> String {
    env("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
        .or_else(|| env("OTEL_EXPORTER_OTLP_ENDPOINT"))
        .unwrap_or_default()
}

/// What [`dispatch`] reads from the environment when export is on.
struct Config {
    sampler: Sampler,
    propagator: TextMapCompositePropagator,
    timeout: Duration,
}

/// Reads the variables the module lists: `None` with export off, and each
/// value the runtime does not support refused, naming its variable.
fn read_config(env: &dyn Fn(&str) -> Option<String>) -> Result<Option<Config>, TelemetryError> {
    let (variable, endpoint) = match env("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") {
        Some(endpoint) => ("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", endpoint),
        None => match env("OTEL_EXPORTER_OTLP_ENDPOINT") {
            Some(endpoint) => ("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint),
            None => return Ok(None),
        },
    };
    if env("OTEL_SDK_DISABLED").is_some_and(|value| value.eq_ignore_ascii_case("true")) {
        return Ok(None);
    }
    let mut problems = Vec::new();
    let url_ok = endpoint.parse::<http::Uri>().ok().is_some_and(|uri| {
        matches!(uri.scheme_str(), Some("http" | "https"))
            && uri.host().is_some_and(|host| !host.is_empty())
    });
    if !url_ok {
        problems.push(format!(
            "{variable} is {endpoint:?}: it is no http or https URL"
        ));
    }
    for name in [
        "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
        "OTEL_EXPORTER_OTLP_PROTOCOL",
    ] {
        if let Some(value) = env(name) {
            if value != PROTOCOL {
                problems.push(format!(
                    "{name} is {value:?}; the runtime exports over {PROTOCOL} alone"
                ));
            }
        }
    }
    if let Some(value) = env("OTEL_RESOURCE_ATTRIBUTES") {
        for pair in value.split(',').filter(|pair| !pair.trim().is_empty()) {
            if !pair
                .split_once('=')
                .is_some_and(|(key, _)| !key.trim().is_empty())
            {
                problems.push(format!(
                    "OTEL_RESOURCE_ATTRIBUTES holds {pair:?}, which is no key=value pair"
                ));
            }
        }
    }
    let sampler = read_sampler(env, &mut problems);
    let propagator = read_propagators(env, &mut problems);
    let timeout = [
        "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT",
        "OTEL_EXPORTER_OTLP_TIMEOUT",
    ]
    .into_iter()
    .find_map(|name| env(name).and_then(|value| value.parse::<u64>().ok()))
    .map_or(EXPORT_TIMEOUT, Duration::from_millis);
    if !problems.is_empty() {
        return Err(TelemetryError::Config(problems));
    }
    Ok(Some(Config {
        sampler,
        propagator,
        timeout,
    }))
}

fn read_sampler(env: &dyn Fn(&str) -> Option<String>, problems: &mut Vec<String>) -> Sampler {
    let mut ratio = || match env("OTEL_TRACES_SAMPLER_ARG") {
        None => 1.0,
        Some(arg) => match arg.parse::<f64>() {
            Ok(r) if (0.0..=1.0).contains(&r) => r,
            _ => {
                problems.push(format!(
                    "OTEL_TRACES_SAMPLER_ARG is {arg:?}; it is a number from 0 to 1"
                ));
                1.0
            }
        },
    };
    let name = env("OTEL_TRACES_SAMPLER").unwrap_or_default();
    match name.as_str() {
        "" | "parentbased_always_on" => Sampler::ParentBased(Box::new(Sampler::AlwaysOn)),
        "always_on" => Sampler::AlwaysOn,
        "always_off" => Sampler::AlwaysOff,
        "parentbased_always_off" => Sampler::ParentBased(Box::new(Sampler::AlwaysOff)),
        "traceidratio" => Sampler::TraceIdRatioBased(ratio()),
        "parentbased_traceidratio" => {
            Sampler::ParentBased(Box::new(Sampler::TraceIdRatioBased(ratio())))
        }
        _ => {
            problems.push(format!(
                "OTEL_TRACES_SAMPLER is {name:?}; the runtime samples with always_on, always_off, traceidratio, parentbased_always_on, parentbased_always_off or parentbased_traceidratio"
            ));
            Sampler::AlwaysOff
        }
    }
}

fn read_propagators(
    env: &dyn Fn(&str) -> Option<String>,
    problems: &mut Vec<String>,
) -> TextMapCompositePropagator {
    let value = env("OTEL_PROPAGATORS").unwrap_or_else(|| "tracecontext,baggage".to_string());
    let mut names: Vec<&str> = Vec::new();
    for name in value
        .split(',')
        .map(str::trim)
        .filter(|name| !name.is_empty())
    {
        if !names.contains(&name) {
            names.push(name);
        }
    }
    let mut propagators: Vec<Box<dyn TextMapPropagator + Send + Sync>> = Vec::new();
    for name in &names {
        match *name {
            "tracecontext" => propagators.push(Box::new(TraceContextPropagator::new())),
            "baggage" => propagators.push(Box::new(BaggagePropagator::new())),
            "none" => {}
            other => problems.push(format!(
                "OTEL_PROPAGATORS names {other:?}; the runtime propagates tracecontext and baggage, or none"
            )),
        }
    }
    if names.contains(&"none") {
        propagators.clear();
    }
    TextMapCompositePropagator::new(propagators)
}

/// The exporter's HTTP client: `reqwest`, on a current-thread runtime of
/// its own, which the batch processor's thread blocks on for each export.
struct ExportClient {
    client: reqwest::Client,
    runtime: Mutex<Option<tokio::runtime::Runtime>>,
}

impl ExportClient {
    fn new(timeout: Duration) -> Result<Self, TelemetryError> {
        let client = reqwest::Client::builder()
            .timeout(timeout)
            .build()
            .map_err(|err| TelemetryError::Exporter(err.to_string()))?;
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|err| TelemetryError::Exporter(err.to_string()))?;
        Ok(ExportClient {
            client,
            runtime: Mutex::new(Some(runtime)),
        })
    }
}

impl fmt::Debug for ExportClient {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ExportClient").finish_non_exhaustive()
    }
}

impl Drop for ExportClient {
    fn drop(&mut self) {
        let runtime = self
            .runtime
            .get_mut()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
        // Dropping a runtime blocks, which a task of another runtime may not.
        if let Some(runtime) = runtime {
            runtime.shutdown_background();
        }
    }
}

#[async_trait]
impl HttpClient for ExportClient {
    async fn send_bytes(&self, request: Request<Bytes>) -> Result<Response<Bytes>, HttpError> {
        let request = reqwest::Request::try_from(request)?;
        let client = self.client.clone();
        let send = async move {
            let response = client.execute(request).await?;
            let mut out = Response::builder().status(response.status());
            if let Some(headers) = out.headers_mut() {
                *headers = response.headers().clone();
            }
            let body = response.bytes().await?;
            Ok::<_, HttpError>(out.body(body)?)
        };
        let runtime = self.runtime.lock().unwrap_or_else(PoisonError::into_inner);
        match runtime.as_ref() {
            Some(runtime) => runtime.block_on(send),
            None => Err("the exporter is stopped".into()),
        }
    }
}

/// The layer of a server's app that makes a server span per request but
/// `/healthz` and `/readyz` (D67), the child of an incoming `traceparent`,
/// named by its method and the path template of the operation whose route
/// takes it, the most literal of them, such as `GET /api/orders/{id}`, or
/// its method alone when none does. With export off it calls through.
#[derive(Clone, Debug)]
pub struct ServerLayer {
    routes: Arc<[Route]>,
}

impl ServerLayer {
    /// The layer of a server whose APIs' operations are `apis`, each the
    /// generated `operations::ALL`.
    pub fn new(apis: &[&'static [OperationInfo]]) -> Self {
        let routes: Vec<Route> = apis
            .iter()
            .flat_map(|operations| operations.iter())
            .map(Route::of)
            .collect();
        ServerLayer {
            routes: routes.into(),
        }
    }

    fn route(&self, method: &str, path: &str) -> Option<&'static str> {
        self.routes
            .iter()
            .filter(|route| route.takes(method, path))
            .max_by_key(|route| route.literals)
            .map(|route| route.template)
    }
}

impl<S> Layer<S> for ServerLayer {
    type Service = ServerService<S>;

    fn layer(&self, inner: S) -> Self::Service {
        ServerService {
            inner,
            layer: self.clone(),
        }
    }
}

/// The service [`ServerLayer`] wraps around a server's app.
#[derive(Clone, Debug)]
pub struct ServerService<S> {
    inner: S,
    layer: ServerLayer,
}

/// The future of a request that [`ServerService`] serves.
pub type ResponseFuture<R, E> = Pin<Box<dyn Future<Output = Result<R, E>> + Send>>;

impl<S, B, R> Service<Request<B>> for ServerService<S>
where
    S: Service<Request<B>, Response = Response<R>>,
    S::Future: Send + 'static,
    S::Error: 'static,
    R: 'static,
{
    type Response = Response<R>;
    type Error = S::Error;
    type Future = ResponseFuture<Response<R>, S::Error>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, request: Request<B>) -> Self::Future {
        let path = request.uri().path();
        if !enabled() || HEALTH_PATHS.contains(&path) {
            return Box::pin(self.inner.call(request));
        }
        let method = request.method().as_str().to_string();
        let route = self.layer.route(&method, path);
        let name = route.map_or_else(|| method.clone(), |route| format!("{method} {route}"));
        let span =
            tracing::info_span!(target: TARGET, "request", otel.name = %name, otel.kind = "server");
        let parent = global::get_text_map_propagator(|propagator| {
            propagator.extract(&HeaderExtractor(request.headers()))
        });
        let _ = span.set_parent(parent);
        span.set_attribute("http.request.method", method);
        span.set_attribute("url.path", path.to_string());
        if let Some(scheme) = request.uri().scheme_str() {
            span.set_attribute("url.scheme", scheme.to_string());
        }
        if let Some(route) = route {
            span.set_attribute("http.route", route);
        }
        let served = self.inner.call(request).instrument(span.clone());
        Box::pin(async move {
            let result = served.await;
            if let Ok(response) = &result {
                let status = response.status().as_u16();
                span.set_attribute("http.response.status_code", i64::from(status));
                if status >= 500 {
                    span.set_status(Status::error(""));
                }
            }
            result
        })
    }
}

/// An operation's route, as [`ServerLayer`] matches it: the method, and
/// the path segment by segment, a `{param}` segment taking any one segment.
#[derive(Clone, Debug)]
struct Route {
    method: &'static str,
    template: &'static str,
    literals: usize,
}

impl Route {
    fn of(operation: &OperationInfo) -> Self {
        Route {
            method: operation.method,
            template: operation.path,
            literals: operation
                .path
                .split('/')
                .filter(|part| !is_param(part))
                .count(),
        }
    }

    fn takes(&self, method: &str, path: &str) -> bool {
        if !self.method.eq_ignore_ascii_case(method) {
            return false;
        }
        let mut parts = path.split('/');
        for segment in self.template.split('/') {
            match parts.next() {
                Some(part) if is_param(segment) && !part.is_empty() => {}
                Some(part) if !is_param(segment) && part == segment => {}
                _ => return false,
            }
        }
        parts.next().is_none()
    }
}

fn is_param(segment: &str) -> bool {
    segment.len() > 2 && segment.starts_with('{') && segment.ends_with('}')
}

/// What a `calls` edge's SDK client sends a request with in place of
/// `reqwest::Client::execute`: the generated SDKs' `Transport`.
pub type Transport = Arc<
    dyn Fn(reqwest::Client, reqwest::Request) -> ResponseFuture<reqwest::Response, reqwest::Error>
        + Send
        + Sync,
>;

/// The transport of a `calls` edge's SDK client (D67): a client span per
/// call, the child of the span the call is made in, whose context the call
/// carries in `traceparent` and `tracestate`. With export off it is `None`,
/// and the client sends as it would without one.
pub fn transport() -> Option<Transport> {
    if !enabled() {
        return None;
    }
    Some(Arc::new(|client, request| Box::pin(send(client, request))))
}

/// Sends `request` with `client` in a client span.
pub async fn send(
    client: reqwest::Client,
    mut request: reqwest::Request,
) -> Result<reqwest::Response, reqwest::Error> {
    let method = request.method().as_str().to_string();
    let span =
        tracing::info_span!(target: TARGET, "call", otel.name = %method, otel.kind = "client");
    span.set_attribute("http.request.method", method);
    span.set_attribute("url.full", request.url().to_string());
    if let Some(host) = request.url().host_str() {
        span.set_attribute("server.address", host.to_string());
    }
    if let Some(port) = request.url().port_or_known_default() {
        span.set_attribute("server.port", i64::from(port));
    }
    let context = span.context();
    global::get_text_map_propagator(|propagator| {
        propagator.inject_context(&context, &mut HeaderInjector(request.headers_mut()));
    });
    let result = client.execute(request).instrument(span.clone()).await;
    match &result {
        Ok(response) => {
            let status = response.status().as_u16();
            span.set_attribute("http.response.status_code", i64::from(status));
            if status >= 500 {
                span.set_status(Status::error(""));
            }
        }
        Err(err) => span.set_status(Status::error(err.to_string())),
    }
    result
}

/// Runs a job's method, `run`, in a span named after its `@job` class,
/// `name`, whose status records the run's error (D67). With export off it
/// only awaits `run`.
pub async fn job<T, E: fmt::Display>(
    name: &str,
    run: impl Future<Output = Result<T, E>>,
) -> Result<T, E> {
    if !enabled() {
        return run.await;
    }
    let span = tracing::info_span!(target: TARGET, "job", otel.name = name, otel.kind = "internal");
    span.set_attribute(JOB_ATTRIBUTE, name.to_string());
    let result = run.instrument(span.clone()).await;
    if let Err(err) = &result {
        span.set_status(Status::error(err.to_string()));
    }
    result
}

/// The span of a worker's handling of a message of `queue` (D67),
/// `process <queue>`: [`Span::none`](tracing::Span::none) with export off.
/// It opens in the worker loop's span, whose `queue` its lines keep, and
/// which the OpenTelemetry layer leaves out, so it is the root of a trace
/// of its own.
#[cfg(feature = "postgres")]
pub(crate) fn message_span(queue: &str, id: &str, attempt: u32) -> tracing::Span {
    if !enabled() {
        return tracing::Span::none();
    }
    let span = tracing::info_span!(target: TARGET, "message", otel.name = %format_args!("process {queue}"), otel.kind = "consumer");
    span.set_attribute("messaging.system", "superschematic");
    span.set_attribute("messaging.operation.name", "process");
    span.set_attribute("messaging.operation.type", "process");
    span.set_attribute("messaging.destination.name", queue.to_string());
    span.set_attribute("messaging.message.id", id.to_string());
    span.set_attribute("superschematic.message.attempt", i64::from(attempt));
    span
}

/// Records a message's failure on its span.
#[cfg(feature = "postgres")]
pub(crate) fn message_failed(span: &tracing::Span, cause: &str) {
    if !span.is_none() {
        span.set_status(Status::error(cause.to_string()));
    }
}
