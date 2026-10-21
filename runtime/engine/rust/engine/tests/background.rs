//! An engine a server opens in the background (the `background` feature,
//! D58), on SQLite and, with the variable set, on Postgres: it keeps
//! trying after each failed attempt, telling the host each failure and the
//! wait before the next, runs its setup before it hands the engine out,
//! answers its routes 503 `unavailable` until then and serves them after,
//! reports its readiness, and shuts down once, its engine closed.

#![cfg(feature = "axum")]

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::body::Body;
use axum::http::{Request, StatusCode};
use serde_json::{json, Value};
use tower::ServiceExt;

use superschematic_engine::api::Caller;
use superschematic_engine::axum::{background_router, Options};
use superschematic_engine::background::{Background, BackgroundOptions, NotOpen};
use superschematic_engine::{Error, Principal};
use support::{order_document, targets};

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .expect("a runtime")
}

async fn get(router: &axum::Router, path: &str) -> (StatusCode, Value) {
    let response = router
        .clone()
        .oneshot(Request::get(path).body(Body::empty()).unwrap())
        .await
        .expect("a response");
    let status = response.status();
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .expect("the body");
    (
        status,
        serde_json::from_slice(&bytes).unwrap_or(Value::Null),
    )
}

#[test]
fn an_engine_opens_in_the_background_after_failures_and_serves_once_set_up() {
    for target in targets("an_engine_opens_in_the_background_after_failures_and_serves_once_set_up")
    {
        let target = Arc::new(target);
        runtime().block_on(async {
            // The first two attempts fail, as a database that is not up
            // yet does; the setup deploys the order schema.
            let attempts = Arc::new(AtomicUsize::new(0));
            let failures = Arc::new(Mutex::new(Vec::<(String, Duration)>::new()));
            let opened = Arc::new(AtomicUsize::new(0));
            let (counted, told, open_seen, options_of) = (
                Arc::clone(&attempts),
                Arc::clone(&failures),
                Arc::clone(&opened),
                Arc::clone(&target),
            );
            let background = Background::open(
                move || {
                    if counted.fetch_add(1, Ordering::SeqCst) < 2 {
                        return Err(Error::Usage("the database is not up yet".to_owned()));
                    }
                    Ok(options_of.options())
                },
                BackgroundOptions::default()
                    .retry(Duration::from_millis(10), Duration::from_millis(15))
                    .setup(|engine| {
                        engine.deploy(&[order_document()], None)?;
                        Ok(())
                    })
                    .on_error(move |error, wait| {
                        told.lock().unwrap().push((error.to_string(), wait));
                    })
                    .on_open(move |_| {
                        open_seen.fetch_add(1, Ordering::SeqCst);
                    }),
            );
            let router = background_router(
                background.clone(),
                Options::new("/engine/shop").callers(|_: &axum::http::request::Parts| Caller {
                    principal: Some(Principal::new("alice", Vec::<String>::new())),
                    ..Caller::default()
                }),
            )
            .expect("the router");

            let engine = background.wait().await.expect("the engine opens");
            assert_eq!(attempts.load(Ordering::SeqCst), 3);
            assert_eq!(opened.load(Ordering::SeqCst), 1);
            let told = failures.lock().unwrap().clone();
            let waits: Vec<Duration> = told.iter().map(|(_, wait)| *wait).collect();
            assert_eq!(
                waits,
                [Duration::from_millis(10), Duration::from_millis(15)]
            );
            for (message, _) in &told {
                assert!(message.contains("the database is not up yet"), "{message}");
            }
            assert!(background.ready().await.is_ok());
            let (status, body) =
                get(&router, "/engine/shop/namespaces/default/schemas/Order").await;
            assert_eq!(status, StatusCode::OK, "{body}");
            assert!(body.to_string().contains(r#""name":"Order""#), "{body}");

            drop(engine);
            background.shutdown().await;
            assert_eq!(background.engine().err(), Some(NotOpen::Closed));
            assert_eq!(background.wait().await.err(), Some(NotOpen::Closed));
            assert!(background.ready().await.is_err());
            // The router holds the engine it served, now closed.
            let (status, body) =
                get(&router, "/engine/shop/namespaces/default/schemas/Order").await;
            assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE, "{body}");
            assert_eq!(body["code"], "unavailable");
        });
    }
}

#[test]
fn the_routes_answer_unavailable_until_the_engine_opens() {
    runtime().block_on(async {
        let background = Background::open(
            || Err(Error::Usage("the database is not up yet".to_owned())),
            BackgroundOptions::default().retry(Duration::from_millis(5), Duration::from_millis(5)),
        );
        let router = background_router(background.clone(), Options::new("/engine/shop"))
            .expect("the router");
        let (status, body) = get(&router, "/engine/shop/namespaces/default/schemas").await;
        assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(body["code"], "unavailable");
        assert_eq!(body["detail"], "The engine is not available");
        assert_eq!(body["status"], 503);
        // A path no route has is not the router's.
        let (status, _) = get(&router, "/engine/shop/nowhere").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        tokio::time::sleep(Duration::from_millis(20)).await;
        match background.engine() {
            Err(NotOpen::Opening(Some(last))) => {
                assert!(last.contains("the database is not up yet"), "{last}")
            }
            other => panic!("the engine is {other:?}"),
        }
        let ready = background.ready().await.expect_err("not ready");
        assert!(ready.starts_with("the engine is not open yet: "), "{ready}");
        background.shutdown().await;
        assert_eq!(background.engine().err(), Some(NotOpen::Closed));
        // A prefix axum cannot route is refused.
        assert!(background_router(background, Options::new("/engine/{db}")).is_err());
    });
}

#[test]
fn a_setup_that_fails_is_tried_again() {
    for target in targets("a_setup_that_fails_is_tried_again") {
        let target = Arc::new(target);
        runtime().block_on(async {
            let setups = Arc::new(AtomicUsize::new(0));
            let (counted, options_of) = (Arc::clone(&setups), Arc::clone(&target));
            let background = Background::open(
                move || Ok(options_of.options()),
                BackgroundOptions::default()
                    .retry(Duration::from_millis(5), Duration::from_millis(5))
                    .setup(move |engine| {
                        if counted.fetch_add(1, Ordering::SeqCst) == 0 {
                            // An invalid document: the deploy refuses it.
                            engine.deploy(
                                &[json!({"kind": "General", "name": "Bad", "types": {"Bad": {}}})],
                                None,
                            )?;
                        }
                        Ok(())
                    }),
            );
            background.wait().await.expect("the engine opens");
            assert_eq!(setups.load(Ordering::SeqCst), 2);
            background.shutdown().await;
        });
    }
}
