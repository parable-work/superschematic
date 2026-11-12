//! A request in, a response out, with no transport under it (D116): what
//! a generated API crate's `Api::handle` answers a request through, and
//! the `block_on` a host with no async runtime drives it with.
//!
//! `handle` gives a router a request it holds whole and collects the
//! router's answer: the route is found, the caller authenticated and
//! admitted, each argument decoded and checked, the operation run and the
//! envelope or the problem written by the handlers the router's own
//! listener runs, so a request is decoded by one path however it arrived.
//! What needs a socket, a clock or a store shared between requests stays
//! the host's: the listener, TLS, the body's limit, timeouts, rate limits,
//! CORS and draining ([`crate::RouterOptions::transport`]).

use std::future::Future;
use std::pin::pin;
use std::sync::Arc;
use std::task::{Context, Poll, Wake, Waker};
use std::thread::{self, Thread};

use axum::body::Body;
use axum::Router;
use bytes::Bytes;
use http::{Request, Response};
use http_body_util::BodyExt;
use tower_service::Service;

use crate::error::ApiError;
use crate::response::{problem_body, PROBLEM_CONTENT_TYPE};

/// Answers `request` through `router`, with no transport under either:
/// the request's body is read already, and the response's is whole.
pub async fn handle(router: &Router, request: Request<Bytes>) -> Response<Bytes> {
    let mut router = router.clone();
    let request = request.map(Body::from);
    // A router is ready whenever it is called, and its error is one it
    // cannot have.
    let response = match router.call(request).await {
        Ok(response) => response,
        Err(never) => match never {},
    };
    let (parts, body) = response.into_parts();
    match body.collect().await {
        Ok(collected) => Response::from_parts(parts, collected.to_bytes()),
        Err(err) => problem(&ApiError::internal(format!(
            "the response's body could not be read: {err}"
        ))),
    }
}

/// A problem as a whole response, for an answer the router did not write.
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

/// Wakes the thread that polls a future in [`block_on`].
struct Unpark(Thread);

impl Wake for Unpark {
    fn wake(self: Arc<Self>) {
        self.0.unpark();
    }

    fn wake_by_ref(self: &Arc<Self>) {
        self.0.unpark();
    }
}

/// Runs `future` to its end on the calling thread, which sleeps while the
/// future waits and wakes when it is woken: what a host with no async
/// runtime drives `handle` with (D116). It is no reactor: a future that
/// waits on a socket or a timer of tokio's never wakes here. An API whose
/// databases are all SQLite files, and that calls no other server, awaits
/// nothing that needs one.
pub fn block_on<F: Future>(future: F) -> F::Output {
    let mut future = pin!(future);
    let waker = Waker::from(Arc::new(Unpark(thread::current())));
    let mut context = Context::from_waker(&waker);
    loop {
        match future.as_mut().poll(&mut context) {
            Poll::Ready(output) => return output,
            // A wake between the poll and the park leaves the thread's
            // token set, so the park returns at once.
            Poll::Pending => thread::park(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::routing::{get, post};
    use http::StatusCode;
    use std::sync::mpsc;

    #[test]
    fn block_on_runs_a_future_that_is_woken_from_another_thread() {
        let (sender, receiver) = tokio::sync::oneshot::channel::<u32>();
        let (started, wait) = mpsc::channel::<()>();
        let worker = thread::spawn(move || {
            wait.recv().expect("the future is polled");
            thread::sleep(std::time::Duration::from_millis(20));
            sender.send(7).expect("the receiver waits");
        });
        let value = block_on(async move {
            started.send(()).expect("the worker waits");
            receiver.await.expect("a value")
        });
        assert_eq!(value, 7);
        worker.join().expect("the worker ends");
        assert_eq!(block_on(async { 1 + 1 }), 2);
    }

    #[test]
    fn handle_answers_a_whole_request_with_a_whole_response() {
        let router = Router::new()
            .route("/api/ping", get(|| async { "pong" }))
            .route(
                "/api/echo",
                post(|body: Bytes| async move { (StatusCode::CREATED, body) }),
            );
        let request = |method: &str, path: &str, body: &'static str| {
            Request::builder()
                .method(method)
                .uri(path)
                .body(Bytes::from_static(body.as_bytes()))
                .expect("a request")
        };
        // No runtime drives the router: block_on polls it where it stands.
        let pong = block_on(handle(&router, request("GET", "/api/ping", "")));
        assert_eq!(
            (pong.status(), pong.body().as_ref()),
            (StatusCode::OK, &b"pong"[..])
        );
        let echo = block_on(handle(&router, request("POST", "/api/echo", "hello")));
        assert_eq!(
            (echo.status(), echo.body().as_ref()),
            (StatusCode::CREATED, &b"hello"[..])
        );
        let missing = block_on(handle(&router, request("GET", "/api/none", "")));
        assert_eq!(missing.status(), StatusCode::NOT_FOUND);
    }
}
