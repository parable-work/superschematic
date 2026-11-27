//! The wake against a platform's wake and token endpoints in the test's
//! process, as Go's wake tests run against theirs.

use super::*;
use axum::http::{HeaderMap, StatusCode};
use axum::routing::{get, post};
use axum::Router;
use std::sync::atomic::{AtomicUsize, Ordering};

/// A wake a platform was sent: the job it names, and its bearer.
type Woken = (String, Option<String>);

/// A platform's wakes and its token endpoint: it counts the requests of
/// each, and records the bearer each wake was sent.
#[derive(Clone, Default)]
struct Platform {
    tokens: Arc<AtomicUsize>,
    wakes: Arc<Mutex<Vec<Woken>>>,
}

impl Platform {
    async fn serve(&self) -> String {
        let (tokens, wakes) = (Arc::clone(&self.tokens), Arc::clone(&self.wakes));
        let refused = Arc::clone(&self.wakes);
        let app = Router::new()
            .route(
                "/token",
                get(move |headers: HeaderMap| {
                    let tokens = Arc::clone(&tokens);
                    async move {
                        if headers.get("metadata-flavor").and_then(|v| v.to_str().ok())
                            != Some("Google")
                        {
                            return Err(StatusCode::FORBIDDEN);
                        }
                        let n = tokens.fetch_add(1, Ordering::SeqCst) + 1;
                        Ok(format!(
                            r#"{{"access_token":"token-{n}","expires_in":3600}}"#
                        ))
                    }
                }),
            )
            .route("/notoken", get(|| async { r#"{"expires_in":3600}"# }))
            .route(
                "/jobs/{job}",
                post(
                    move |axum::extract::Path(job): axum::extract::Path<String>,
                          headers: HeaderMap,
                          body: String| {
                        let wakes = Arc::clone(&wakes);
                        async move {
                            assert_eq!(body, "{}");
                            let bearer = headers
                                .get("authorization")
                                .and_then(|v| v.to_str().ok())
                                .map(str::to_string);
                            wakes.lock().unwrap().push((job, bearer));
                            StatusCode::OK
                        }
                    },
                ),
            )
            .route(
                "/refuse",
                post(move || {
                    let refused = Arc::clone(&refused);
                    async move {
                        refused.lock().unwrap().push(("refused".into(), None));
                        StatusCode::FORBIDDEN
                    }
                }),
            );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        format!("http://{address}")
    }

    fn woken(&self) -> Vec<Woken> {
        self.wakes.lock().unwrap().clone()
    }
}

fn wake(url: String, token: Option<&str>) -> Wake {
    Wake {
        url,
        token: token.map(|url| Token {
            url: url.to_string(),
            headers: HashMap::from([("Metadata-Flavor".to_string(), "Google".to_string())]),
        }),
    }
}

#[tokio::test]
async fn an_enqueue_wakes_its_queues_workers_once_in_the_interval_with_the_token() {
    let platform = Platform::default();
    let base = platform.serve().await;
    let token = format!("{base}/token");
    let waker = Waker::new(HashMap::from([
        (
            "OrderPlaced".to_string(),
            vec![wake(format!("{base}/jobs/fulfil:run"), Some(&token))],
        ),
        (
            "Ping".to_string(),
            vec![
                wake(format!("{base}/jobs/ping-a:run"), None),
                wake(format!("{base}/jobs/ping-b:run"), None),
            ],
        ),
    ]))
    .with_interval(Duration::from_millis(400));
    waker.enqueued("OrderPlaced", Duration::ZERO);
    waker.enqueued("OrderPlaced", Duration::ZERO);
    waker.enqueued("Ping", Duration::ZERO);
    // A queue with no wake sends nothing.
    waker.enqueued("Unwoken", Duration::ZERO);
    waker.settled().await;
    let mut woken = platform.woken();
    woken.sort();
    assert_eq!(
        woken,
        [
            ("fulfil:run".to_string(), Some("Bearer token-1".to_string())),
            ("ping-a:run".to_string(), None),
            ("ping-b:run".to_string(), None),
        ]
    );
    // Once the interval has passed the queue is woken again, with the
    // token the waker still holds; but not for a message due past the
    // horizon, which the tick finds first.
    tokio::time::sleep(Duration::from_millis(450)).await;
    waker.enqueued("OrderPlaced", HORIZON + Duration::from_secs(1));
    waker.settled().await;
    assert_eq!(
        platform.woken().len(),
        3,
        "a message due past the horizon sends no wake"
    );
    waker.enqueued("OrderPlaced", Duration::from_secs(60));
    waker.settled().await;
    assert_eq!(
        platform.woken().last().cloned(),
        Some(("fulfil:run".to_string(), Some("Bearer token-1".to_string())))
    );
    assert_eq!(platform.tokens.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn a_failed_wake_is_logged_and_nothing_else() {
    let platform = Platform::default();
    let base = platform.serve().await;
    let waker = Waker::new(HashMap::from([
        (
            "Refused".to_string(),
            vec![wake(format!("{base}/refuse"), None)],
        ),
        (
            "Untokened".to_string(),
            vec![wake(
                format!("{base}/jobs/x:run"),
                Some(&format!("{base}/notoken")),
            )],
        ),
        (
            "Unreachable".to_string(),
            vec![wake("http://127.0.0.1:1/jobs/none:run".to_string(), None)],
        ),
    ]));
    // Each returns at once, whatever its wake does.
    for queue in ["Refused", "Untokened", "Unreachable"] {
        waker.enqueued(queue, Duration::ZERO);
    }
    waker.settled().await;
    assert_eq!(platform.woken(), [("refused".to_string(), None)]);
    assert_eq!(
        send(
            waker.client.clone(),
            &waker.tokens,
            &wake(format!("{base}/refuse"), None)
        )
        .await,
        Err("the wake answered 403 Forbidden".to_string())
    );
    assert_eq!(
        send(
            waker.client.clone(),
            &waker.tokens,
            &wake(
                format!("{base}/jobs/x:run"),
                Some(&format!("{base}/notoken"))
            )
        )
        .await,
        Err("the wake's token: the token request answered no access_token".to_string())
    );
}

#[test]
fn an_enqueue_outside_a_runtime_sends_nothing() {
    let waker = Waker::new(HashMap::from([(
        "Ping".to_string(),
        vec![wake("http://127.0.0.1:1/jobs/ping:run".to_string(), None)],
    )]));
    waker.enqueued("Ping", Duration::ZERO);
    assert!(waker.last.lock().unwrap().is_empty());
}

#[test]
fn the_variable_holds_lists_of_wakes_by_queue() {
    assert_eq!(parse(""), Ok(HashMap::new()));
    assert_eq!(parse("  "), Ok(HashMap::new()));
    let wakes = parse(
        r#"{"OrderPlaced":[{"url":"https://run.example/jobs/fulfil:run","token":{"url":"http://metadata/token","headers":{"Metadata-Flavor":"Google"}}}]}"#,
    )
    .unwrap();
    assert_eq!(
        wakes["OrderPlaced"],
        [wake(
            "https://run.example/jobs/fulfil:run".to_string(),
            Some("http://metadata/token")
        )]
    );
    assert!(parse("[]")
        .unwrap_err()
        .starts_with("QUEUE_WAKES is no JSON object of lists of wakes by queue"));
    assert_eq!(
        parse(r#"{"Ping":[{"token":{"url":"x"}}]}"#),
        Err("QUEUE_WAKES: a wake of queue Ping has no url".to_string())
    );
    assert_eq!(
        parse(r#"{"Ping":[{"url":"https://run.example","token":{}}]}"#),
        Err("QUEUE_WAKES: a wake of queue Ping names a token with no url".to_string())
    );
}
