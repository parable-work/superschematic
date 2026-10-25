//! The Rust deriver (D74), `superschematic_engine::derive::run_deriver`,
//! over a background engine with the core's behaviors: it fills a
//! schema's stale Derived fields, settles a panicking answer as a failure,
//! wakes on a commit, and returns once its shutdown resolves. On SQLite
//! and, with the variable set, on Postgres. The axum feature brings the
//! background engine.

#![cfg(feature = "axum")]

mod support;

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::background::{Background, BackgroundOptions};
use superschematic_engine::derive::{run_deriver, DerivationAnswer, DeriverOptions};
use superschematic_engine::{CreateOptions, DefineOptions, Engine, Principal, SchemaTarget};
use support::{alice, schema_document, targets};

fn deriver() -> Principal {
    Principal::new("deriver", ["tickets.derive"])
}

fn ticket_document() -> Value {
    let mut document = schema_document(
        "Ticket",
        json!([
            {"name": "subject", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "category", "typeRef": {"name": "string"}},
            {"name": "summary", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"]["Ticket"]["behaviors"] = json!([{"name": "Derived", "config": {
        "fields": {
            "category": {"from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard"},
            "summary": {"from": ["body"], "instruction": "Summarize it.", "model": "simple"}
        },
        "permission": "tickets.derive",
        "maxAttempts": 1
    }}]);
    document
}

fn create(engine: &Engine, id: &str, body: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Ticket",
            &json!({"subject": "Card", "body": body}),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

fn data_of(engine: &Engine, id: &str, field: &str) -> Value {
    engine
        .instances()
        .get(&alice(), "Ticket", id, Default::default())
        .expect("get")
        .and_then(|record| record.data.get(field).cloned())
        .unwrap_or(Value::Null)
}

async fn until(check: impl Fn() -> bool) {
    let end = Instant::now() + Duration::from_secs(5);
    while !check() {
        assert!(Instant::now() < end, "timed out");
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
}

#[test]
fn the_deriver_fills_stale_fields_settles_a_panic_as_a_failure_wakes_on_a_commit_and_stops() {
    for target in targets("the_deriver_fills_stale_fields") {
        let runtime = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .enable_all()
            .build()
            .expect("a runtime");
        let target = Arc::new(target);
        runtime.block_on(async {
            let opened = Arc::clone(&target);
            let background = Background::open(
                move || Ok(opened.core_options()),
                BackgroundOptions::default(),
            );
            let engine = background.wait().await.expect("the engine opens");
            engine
                .schemas()
                .define(&alice(), ticket_document(), DefineOptions::default())
                .expect("define");
            engine
                .schemas()
                .publish(&alice(), "Ticket", SchemaTarget::default())
                .expect("publish");
            create(&engine, "t1", "Refused at checkout.");

            let seen = Arc::new(Mutex::new(Vec::new()));
            let heard = Arc::clone(&seen);
            let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
            let running = tokio::spawn(run_deriver(
                background.clone(),
                deriver(),
                vec!["Ticket".to_owned()],
                move |_, item| {
                    let heard = Arc::clone(&heard);
                    async move {
                        heard
                            .lock()
                            .unwrap()
                            .push(format!("{}.{}", item.id, item.field));
                        if item.id == "t2" && item.field == "summary" {
                            panic!("the model refused");
                        }
                        if item.field == "category" {
                            DerivationAnswer::Value {
                                value: json!("billing"),
                                confidence: Some(0.9),
                                usage: None,
                            }
                        } else {
                            DerivationAnswer::value(format!(
                                "About: {}",
                                item.inputs["body"].as_str().unwrap_or_default()
                            ))
                        }
                    }
                },
                DeriverOptions {
                    idle: Duration::from_secs(60),
                    ..DeriverOptions::default()
                },
                async {
                    let _ = stopped.await;
                },
            ));
            until(|| data_of(&engine, "t1", "summary") != Value::Null).await;
            assert_eq!(data_of(&engine, "t1", "category"), "billing");

            // A create commits, which wakes the loop well before its idle wait.
            create(&engine, "t2", "Locked out.");
            until(|| data_of(&engine, "t2", "category") != Value::Null).await;
            let failed = || {
                engine
                    .instances()
                    .invoke_schema(&deriver(), "Ticket", "failedDerivations", &json!({}), None)
                    .expect("failedDerivations")["items"]
                    .clone()
            };
            until(|| failed().as_array().is_some_and(|items| items.len() == 1)).await;
            assert_eq!(
                failed(),
                json!([{"id": "t2", "field": "summary", "attempts": 1,
                        "failure": {"class": "panicked", "message": "the answer panicked"}}])
            );
            stop.send(()).expect("the deriver runs");
            running
                .await
                .expect("joined")
                .expect("the deriver stops cleanly");
            let mut seen = seen.lock().unwrap().clone();
            seen.sort();
            assert_eq!(
                seen,
                ["t1.category", "t1.summary", "t2.category", "t2.summary"]
            );
            background.shutdown().await;
        });
    }
}
