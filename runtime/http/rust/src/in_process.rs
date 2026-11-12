//! A call from one API of a process to another, with no socket between
//! them (D116): the transport a generated SDK's client sends through when
//! its callee is served by the same binary.
//!
//! The client builds each request as it does for a server across the
//! network: the path under its base URL, the end user it forwards and its
//! service credential, which is the caller's loopback credential (section
//! 9.9 of `docs/stack-model.md`). [`transport`] hands the request to the
//! callee's `handle` and gives the client its answer, so the callee finds
//! the route, authenticates the caller and admits by the route's rule as
//! it does for a request from a listener: a service clause holds as it
//! does between two servers. Nothing is dialed, and the base URL's host is
//! never resolved.

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use bytes::Bytes;
use http::{Request, Response};

use crate::error::ApiError;
use crate::response::{problem_body, PROBLEM_CONTENT_TYPE};

/// What a transport's call answers with, as a generated SDK's client
/// awaits it.
pub type ResponseFuture =
    Pin<Box<dyn Future<Output = Result<reqwest::Response, reqwest::Error>> + Send>>;

/// A generated SDK's `Transport`: what sends each request the client
/// builds, in place of `reqwest::Client::execute`.
pub type Transport = Arc<dyn Fn(reqwest::Client, reqwest::Request) -> ResponseFuture + Send + Sync>;

/// The transport of a client whose callee `handle` answers in the process.
/// `base` is the path of the client's base URL, where the binary's origin
/// serves the callee (`/api/audit-api`): each request's path has it taken
/// off before `handle` sees it, as the origin takes it off a request from
/// a listener (D91).
pub fn transport<F, Fut>(base: &str, handle: F) -> Transport
where
    F: Fn(Request<Bytes>) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Response<Bytes>> + Send + 'static,
{
    let base: Arc<str> = Arc::from(base.trim_end_matches('/'));
    let handle = Arc::new(handle);
    Arc::new(move |_client, request| {
        let base = Arc::clone(&base);
        let handle = Arc::clone(&handle);
        Box::pin(async move {
            let response = match whole(&base, &request) {
                Ok(request) => handle(request).await,
                Err(err) => problem(&err),
            };
            Ok(reqwest::Response::from(response.map(reqwest::Body::from)))
        })
    })
}

/// The client's request as `handle` takes one: its method, its path under
/// `base` with its query, its headers and its body, read whole.
fn whole(base: &str, request: &reqwest::Request) -> Result<Request<Bytes>, ApiError> {
    let url = request.url();
    let path = url.path();
    let path = match path.strip_prefix(base) {
        Some("") => "/",
        Some(rest) if rest.starts_with('/') => rest,
        _ => path,
    };
    let target = match url.query() {
        Some(query) => format!("{path}?{query}"),
        None => path.to_owned(),
    };
    let body = match request.body() {
        None => Bytes::new(),
        Some(body) => match body.as_bytes() {
            Some(bytes) => Bytes::copy_from_slice(bytes),
            // A streamed body is a file's, which `handle` does not route:
            // the router's listener alone serves an upload (D116).
            None => {
                return Err(ApiError::internal(
                    "the request streams its body, and a call in the process carries a body it holds whole",
                ))
            }
        },
    };
    let mut whole = Request::builder()
        .method(request.method().clone())
        .uri(target)
        .body(body)
        .map_err(|err| ApiError::internal(format!("the request is no request: {err}")))?;
    *whole.headers_mut() = request.headers().clone();
    Ok(whole)
}

/// A problem as a whole response, for a request `handle` never saw.
fn problem(err: &ApiError) -> Response<Bytes> {
    let body = problem_body(err, None).to_string();
    let mut response = Response::new(Bytes::from(body));
    *response.status_mut() = err.status;
    response.headers_mut().insert(
        http::header::CONTENT_TYPE,
        http::HeaderValue::from_static(PROBLEM_CONTENT_TYPE),
    );
    response
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::StatusCode;

    /// A handle that answers with what it was asked.
    async fn echo(request: Request<Bytes>) -> Response<Bytes> {
        let credential = request
            .headers()
            .get("service-authorization")
            .and_then(|value| value.to_str().ok())
            .unwrap_or("none")
            .to_owned();
        let line = format!(
            "{} {} {} {}",
            request.method(),
            request.uri(),
            credential,
            String::from_utf8_lossy(request.body())
        );
        let mut response = Response::new(Bytes::from(line));
        *response.status_mut() = StatusCode::ACCEPTED;
        response
    }

    #[tokio::test]
    async fn a_call_reaches_the_callees_handle_with_its_path_under_the_base() {
        let transport = transport("/api/audit-api", echo);
        let client = reqwest::Client::new();
        let request = client
            .post("http://127.0.0.1:1/api/audit-api/api/v1/records?limit=2")
            .header("Service-Authorization", "Bearer token")
            .body("{\"note\":1}")
            .build()
            .expect("a request");
        // Port 1 answers nothing: the call never leaves the process.
        let response = transport(client, request).await.expect("an answer");
        assert_eq!(response.status(), StatusCode::ACCEPTED);
        assert_eq!(
            response.text().await.expect("a body"),
            "POST /api/v1/records?limit=2 Bearer token {\"note\":1}"
        );
    }

    #[tokio::test]
    async fn a_path_outside_the_base_reaches_the_handle_as_it_is() {
        let transport = transport("/api/audit-api/", echo);
        let client = reqwest::Client::new();
        for (url, want) in [
            ("http://localhost/api/audit-api", "GET / none "),
            (
                "http://localhost/api/audit-apis/x",
                "GET /api/audit-apis/x none ",
            ),
        ] {
            let request = client.get(url).build().expect("a request");
            let response = transport(client.clone(), request).await.expect("an answer");
            assert_eq!(response.text().await.expect("a body"), want);
        }
    }
}
