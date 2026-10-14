//! The HTTP corpus through the engine's axum router (the `axum` feature,
//! D58): the transport a Rust server's mount is held to the recorded
//! answers by, as Go's `httptest` replay holds `enginehttp`.

use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, BodyDataStream, HttpBody as _};
use axum::http::{HeaderName, HeaderValue, Request as HttpRequest, StatusCode};
use axum::response::Response as HttpResponse;
use axum::Router;
use futures_util::StreamExt;
use tokio::runtime::Runtime;
use tower::ServiceExt;

use superschematic_engine::api::{self, Pull, Request};
use superschematic_engine::axum::{router, Options, Serves};
use superschematic_engine::Engine;

use crate::http::{Answer, Answered, Frames, MountView, Transport};

/// The engine's axum router as the replayer's transport. Each exchange
/// builds the app it is sent to as a server mounts it: a router per mount,
/// serving the routes of its kind under its prefix, with its options and
/// body limit, merged, and a fallback that answers 404 as the app's
/// notFound does. The request goes through it in process (tower's
/// `oneshot`) on a runtime of the transport's own, its recorded caller in
/// its extensions (the router's default,
/// [`FromExtensions`](superschematic_engine::axum::FromExtensions)) and the
/// replayer's request id in its `X-Request-ID`. A stream's body is pulled a chunk at a
/// time, as a client reads it, and dropped, as a client goes away, once
/// the case is over.
pub struct AxumRouter {
    runtime: Arc<Runtime>,
}

impl AxumRouter {
    pub fn new() -> Self {
        AxumRouter {
            runtime: Arc::new(
                tokio::runtime::Builder::new_multi_thread()
                    .worker_threads(2)
                    .enable_time()
                    .build()
                    .expect("a runtime for the axum transport"),
            ),
        }
    }
}

impl Default for AxumRouter {
    fn default() -> Self {
        AxumRouter::new()
    }
}

impl Transport for AxumRouter {
    fn serve(
        &self,
        engine: &Arc<Engine>,
        mounts: &[MountView<'_>],
        _served: Option<usize>,
        request: Request,
    ) -> Answer {
        let id = request.request_id.clone();
        let app = match app_of(engine, mounts) {
            Ok(app) => app,
            Err(refused) => return failed(&id, &format!("the router refused the app: {refused}")),
        };
        let sent = match http_request(request) {
            Ok(sent) => sent,
            Err(refused) => return failed(&id, &format!("no HTTP request: {refused}")),
        };
        // A clone goes in, so the app's last handle drops here, off the
        // runtime.
        let Ok(response) = self.runtime.block_on(app.clone().oneshot(sent));
        answer_of(response, &self.runtime)
    }

    fn limits_bodies(&self) -> bool {
        true
    }
}

fn app_of(engine: &Arc<Engine>, mounts: &[MountView<'_>]) -> Result<Router, String> {
    let mut app = Router::new();
    for mount in mounts {
        let options = Options::new(mount.prefix)
            .api(mount.options.clone())
            .serves(if mount.mcp { Serves::Mcp } else { Serves::Http })
            .body_limit(mount.body_limit);
        app = app.merge(router(Arc::clone(engine), options).map_err(|error| error.to_string())?);
    }
    Ok(app.fallback(not_found))
}

// not_found is the app's notFound: a path no mount serves.
async fn not_found(request: axum::extract::Request) -> HttpResponse {
    let id = request
        .headers()
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_owned();
    response_of(api::Problem::not_found("Not Found").response(&id))
}

fn response_of(response: api::Response) -> HttpResponse {
    let bytes = response.bytes().to_vec();
    let mut answered = HttpResponse::new(Body::from(bytes));
    *answered.status_mut() =
        StatusCode::from_u16(response.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    for (name, value) in &response.headers {
        if let (Ok(name), Ok(value)) = (
            HeaderName::from_bytes(name.as_bytes()),
            HeaderValue::from_str(value),
        ) {
            answered.headers_mut().append(name, value);
        }
    }
    answered
}

// http_request is the recorded request as a client sends it: the
// replayer's request id in its X-Request-ID, its caller in its extensions.
fn http_request(request: Request) -> Result<HttpRequest<Body>, String> {
    let uri = if request.query.is_empty() {
        request.path.clone()
    } else {
        format!("{}?{}", request.path, request.query)
    };
    let mut builder = HttpRequest::builder()
        .method(request.method.as_str())
        .uri(uri);
    for (name, value) in &request.headers {
        if !name.eq_ignore_ascii_case("x-request-id") {
            builder = builder.header(name.as_str(), value.as_str());
        }
    }
    builder = builder.header("x-request-id", request.request_id.as_str());
    let mut sent = builder
        .body(request.body.map_or_else(Body::empty, Body::from))
        .map_err(|error| error.to_string())?;
    sent.extensions_mut().insert(request.caller);
    Ok(sent)
}

// answer_of reads the router's response as the replayer compares it: a
// body of no known length that is an event stream is pulled as one.
fn answer_of(response: HttpResponse, runtime: &Arc<Runtime>) -> Answer {
    let status = response.status().as_u16();
    let headers: Vec<(String, String)> = response
        .headers()
        .iter()
        .map(|(name, value)| {
            (
                name.as_str().to_owned(),
                String::from_utf8_lossy(value.as_bytes()).into_owned(),
            )
        })
        .collect();
    let streams = response
        .headers()
        .get("content-type")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|kind| kind.starts_with("text/event-stream"))
        && response.body().size_hint().exact().is_none();
    let body = response.into_body();
    let body = if streams {
        Answered::Stream(Box::new(BodyFrames {
            chunks: body.into_data_stream(),
            runtime: Arc::clone(runtime),
        }))
    } else {
        match runtime.block_on(axum::body::to_bytes(body, usize::MAX)) {
            Ok(bytes) => Answered::Bytes(bytes.to_vec()),
            Err(error) => {
                Answered::Bytes(format!("the body could not be read: {error}").into_bytes())
            }
        }
    };
    Answer {
        status,
        headers,
        body,
    }
}

fn failed(id: &str, detail: &str) -> Answer {
    api::Problem::new(500, detail, "internal_error")
        .response(id)
        .into()
}

/// A stream's body as a client reads it: each pull its next chunk, waiting
/// up to the pull's timeout.
struct BodyFrames {
    chunks: BodyDataStream,
    runtime: Arc<Runtime>,
}

impl Frames for BodyFrames {
    fn next(&mut self, timeout: Duration) -> Pull {
        let chunks = &mut self.chunks;
        match self
            .runtime
            .block_on(async { tokio::time::timeout(timeout, chunks.next()).await })
        {
            Err(_) => Pull::Pending,
            Ok(Some(Ok(bytes))) => Pull::Data(bytes.to_vec()),
            Ok(Some(Err(_)) | None) => Pull::End,
        }
    }
}
