//! The engine's calls by name with JSON arguments (`calls`), which the
//! bindings and the scenario replayer make: what JSON can say that Rust's
//! types cannot (a page size, a sequence or a version that is no integer,
//! a malformed principal) refused in the TypeScript engine's words, and a
//! call's result as the TypeScript method returns it; on SQLite and, with
//! the variable set, on Postgres.

mod support;

use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::calls::{call, principal_from_json, CALLS};
use superschematic_engine::{Engine, EngineErrorCode, Error, Principal, RunnerOptions};
use support::{alice, order_document, targets};

fn refused(result: superschematic_engine::Result<Option<Value>>) -> (EngineErrorCode, String) {
    match result {
        Err(Error::Engine(error)) => (error.code(), error.message().to_owned()),
        other => panic!("expected a refusal, got {other:?}"),
    }
}

#[test]
fn a_call_takes_json_arguments_and_returns_what_the_typescript_method_returns() {
    for target in targets("a_call_takes_json_arguments") {
        let engine = target.open();
        let alice = alice();
        let principal = Some(&alice);
        call(&engine, "schemas.define", principal, &[order_document()]).unwrap();
        assert_eq!(
            call(
                &engine,
                "schemas.publish",
                principal,
                &[json!("Order"), json!({})]
            )
            .unwrap(),
            Some(json!({"namespace": "default", "name": "Order", "version": 1, "published": true}))
        );
        let created = call(
            &engine,
            "instances.create",
            principal,
            &[
                json!("Order"),
                json!({"title": "Desk"}),
                json!({"id": "o1"}),
            ],
        )
        .unwrap()
        .unwrap();
        assert_eq!(
            (created["id"].as_str(), created["seq"].as_i64()),
            (Some("o1"), Some(1))
        );
        // undefined is no result.
        assert_eq!(
            call(
                &engine,
                "instances.get",
                principal,
                &[json!("Order"), json!("o2")]
            )
            .unwrap(),
            None
        );
        assert_eq!(
            call(&engine, "schemas.draft", principal, &[json!("Order")]).unwrap(),
            None
        );
        assert_eq!(
            call(
                &engine,
                "instances.delete",
                principal,
                &[json!("Order"), json!("o1")]
            )
            .unwrap(),
            Some(json!(true))
        );
        let page = call(&engine, "events.read", principal, &[json!({"limit": 2})])
            .unwrap()
            .unwrap();
        assert_eq!(page["events"].as_array().unwrap().len(), 2);
        assert_eq!(page["more"], json!(true));
        assert!(
            call(&engine, "events.head", None, &[])
                .unwrap()
                .unwrap()
                .as_i64()
                .unwrap()
                > 0
        );
        assert!(matches!(
            call(&engine, "runner.nothing", principal, &[]),
            Err(Error::Usage(_))
        ));
        assert!(CALLS.contains(&"instances.lookup"));
    }
}

// opened is an engine with a runner principal and Order published.
fn opened(target: &support::Target) -> Arc<Engine> {
    let mut options = target.options();
    options.runner = Some(RunnerOptions::new(Principal::new(
        "runner",
        Vec::<String>::new(),
    )));
    let engine = Engine::open_shared(options).unwrap();
    let alice = alice();
    call(&engine, "schemas.define", Some(&alice), &[order_document()]).unwrap();
    call(&engine, "schemas.publish", Some(&alice), &[json!("Order")]).unwrap();
    engine
}

#[test]
fn the_runner_and_the_tools_are_calls() {
    for target in targets("the_runner_and_the_tools_are_calls") {
        let engine = opened(&target);
        let alice = alice();
        let principal = Some(&alice);
        // The runner's calls.
        assert_eq!(
            call(&engine, "runner.runDue", None, &[]).unwrap(),
            Some(json!({"handled": 0, "skipped": 0, "failed": 0, "scheduled": 0}))
        );
        assert_eq!(
            call(&engine, "runner.running", None, &[]).unwrap(),
            Some(json!(false))
        );
        let status = call(&engine, "runner.status", None, &[]).unwrap().unwrap();
        assert_eq!(status["principal"], "runner");

        // The tools': the manifest and a call by handle.
        let manifest = call(&engine, "tools.manifest", principal, &[json!({})])
            .unwrap()
            .unwrap();
        assert!(manifest["tools"]
            .as_array()
            .unwrap()
            .iter()
            .any(|tool| tool["mcp"]["handle"] == "order_create"));
        let created = call(
            &engine,
            "tools.call",
            principal,
            &[
                json!("order_create"),
                json!({"id": "o1", "data": {"title": "Desk"}}),
            ],
        )
        .unwrap()
        .unwrap();
        assert_eq!(created["id"], "o1");
        assert_eq!(
            refused(call(
                &engine,
                "tools.call",
                principal,
                &[json!("order_nothing")]
            ))
            .0,
            EngineErrorCode::NotFound
        );
    }
}

#[test]
fn the_http_api_is_a_call_and_a_stream_is_pulled_by_its_id() {
    for target in targets("the_http_api_is_a_call") {
        let engine = opened(&target);
        let alice = alice();
        let principal = Some(&alice);
        call(
            &engine,
            "instances.create",
            principal,
            &[
                json!("Order"),
                json!({"title": "Desk"}),
                json!({"id": "o1"}),
            ],
        )
        .unwrap();
        // The HTTP API: routes, and a request's response as JSON.
        assert_eq!(
            call(&engine, "api.routes", None, &[])
                .unwrap()
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            31
        );
        let caller = json!({"principal": {"subject": "alice", "permissions": []}});
        let answered = call(
            &engine,
            "api.handle",
            None,
            &[json!({
                "method": "GET",
                "prefix": "/api",
                "path": "/api/namespaces/default/schemas/Order/instances/o1",
                "caller": caller,
                "requestId": "r1",
            })],
        )
        .unwrap()
        .unwrap();
        assert_eq!(answered["status"], 200);
        assert!(answered["headers"]
            .as_array()
            .unwrap()
            .contains(&json!(["etag", "\"1\""])));
        let body: Value = serde_json::from_str(answered["body"].as_str().unwrap()).unwrap();
        assert_eq!(body["data"]["id"], "o1");
        assert_eq!(answered["stream"], Value::Null);

        // An event stream the API opens is pulled and closed by its id.
        let streamed = call(
            &engine,
            "api.handle",
            None,
            &[
                json!({
                    "method": "GET",
                    "path": "/namespaces/default/events",
                    "headers": [["accept", "text/event-stream"]],
                    "caller": caller,
                }),
                json!({"stream": {"heartbeatMs": 60000}}),
            ],
        )
        .unwrap()
        .unwrap();
        let id = streamed["stream"].clone();
        assert!(id.is_u64(), "{streamed}");
        assert_eq!(
            call(&engine, "stream.next", None, &[id.clone(), json!(100)]).unwrap(),
            Some(json!({"data": ": open\n\n"}))
        );
        let replay = call(&engine, "stream.next", None, &[id.clone(), json!(100)])
            .unwrap()
            .unwrap();
        assert!(
            replay["data"].as_str().unwrap().contains("event: ready"),
            "{replay}"
        );
        assert_eq!(
            call(&engine, "stream.next", None, &[id.clone(), json!(1)]).unwrap(),
            Some(json!({"pending": true}))
        );
        assert_eq!(
            call(&engine, "stream.close", None, std::slice::from_ref(&id)).unwrap(),
            None
        );
        assert_eq!(
            refused(call(&engine, "stream.next", None, &[id, json!(1)])),
            (
                EngineErrorCode::InvalidArgument,
                "no event stream 1 is open".to_owned()
            )
        );

        // stream.open reads as its principal, its first page now: a read
        // the engine refuses is the read's error.
        let opened = call(
            &engine,
            "stream.open",
            principal,
            &[json!({"after": "head"}), json!({"pageSize": 10})],
        )
        .unwrap()
        .unwrap();
        let first = call(&engine, "stream.next", None, &[opened.clone(), json!(100)])
            .unwrap()
            .unwrap();
        assert_eq!(first, json!({"data": ": open\n\n"}));
        assert_eq!(
            refused(call(
                &engine,
                "stream.open",
                principal,
                &[json!({"namespace": "nowhere"})]
            ))
            .0,
            EngineErrorCode::UnknownNamespace
        );

        // close shuts the engine: its streams end, and its calls are
        // refused.
        assert_eq!(call(&engine, "close", None, &[]).unwrap(), None);
        let mut ended = None;
        for _ in 0..50 {
            let pulled = call(&engine, "stream.next", None, &[opened.clone(), json!(20)])
                .unwrap()
                .unwrap();
            if pulled == json!({"end": true}) {
                ended = Some(pulled);
                break;
            }
        }
        assert!(ended.is_some(), "the stream ended at the close");
        assert!(call(&engine, "events.read", principal, &[json!({})]).is_err());
    }
}

// Keeps names a tool filter keeps: the tools of an Order that only read.
struct ReadsOnly;

impl superschematic_engine::ToolFilter for ReadsOnly {
    fn keeps(&self, _: &Principal, tool: &superschematic_engine::ToolSummary, _: &str) -> bool {
        !tool.writes
    }
}

#[test]
fn a_bindings_http_takes_a_body_of_bytes_and_a_mounts_filter_narrows_the_tools() {
    for target in targets("a_bindings_http_takes_a_body_of_bytes") {
        let engine = opened(&target);
        let caller = json!({"principal": {"subject": "alice", "permissions": []}});
        let reply = superschematic_engine::calls::http(
            &engine,
            &json!({
                "method": "POST",
                "prefix": "/api",
                "path": "/api/namespaces/default/schemas/Order/instances",
                "headers": [["content-type", "application/json"]],
                "caller": caller,
                "requestId": "r1",
            }),
            Some(br#"{"id":"o1","data":{"title":"Desk"}}"#.to_vec()),
            None,
        )
        .unwrap();
        assert_eq!(reply.status, 201);
        assert!(reply.headers.contains(&(
            "location".to_owned(),
            "/api/namespaces/default/schemas/Order/instances/o1".to_owned()
        )));
        let body: Value = serde_json::from_slice(reply.body.as_deref().unwrap()).unwrap();
        assert_eq!(body["data"]["id"], "o1");
        assert!(reply.stream.is_none());
        // The tools route and a tools call through the mount's filter.
        let filter: Arc<dyn superschematic_engine::ToolFilter> = Arc::new(ReadsOnly);
        let tools = superschematic_engine::calls::http(
            &engine,
            &json!({"method": "GET", "path": "/namespaces/default/tools", "caller": caller}),
            None,
            Some(Arc::clone(&filter)),
        )
        .unwrap();
        let manifest: Value = serde_json::from_slice(tools.body.as_deref().unwrap()).unwrap();
        let handles: Vec<&str> = manifest["data"]["tools"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|tool| tool["mcp"]["hidden"] != Value::Bool(true))
            .filter_map(|tool| tool["mcp"]["handle"].as_str())
            .collect();
        assert!(
            handles.contains(&"order_get") && !handles.contains(&"order_create"),
            "{handles:?}"
        );
        let alice = alice();
        let refused = superschematic_engine::calls::call_with(
            &engine,
            "tools.call",
            Some(&alice),
            &[json!("order_create"), json!({"data": {"title": "Lamp"}})],
            Some(filter.as_ref()),
        );
        assert_eq!(refused_code(refused), EngineErrorCode::NotFound);
    }
}

fn refused_code(result: superschematic_engine::Result<Option<Value>>) -> EngineErrorCode {
    refused(result).0
}

#[test]
fn what_json_says_and_rusts_types_do_not_is_refused_in_the_typescript_engines_words() {
    for target in targets("what_json_says_and_rusts_types_do_not_is_refused") {
        let engine = target.open();
        let alice = alice();
        let principal = Some(&alice);
        call(&engine, "schemas.define", principal, &[order_document()]).unwrap();
        call(&engine, "schemas.publish", principal, &[json!("Order")]).unwrap();
        call(
            &engine,
            "instances.create",
            principal,
            &[
                json!("Order"),
                json!({"title": "Desk"}),
                json!({"id": "o1"}),
            ],
        )
        .unwrap();
        let cases = [
            ("instances.list", vec![json!("Order"), json!({"limit": 1.5})], "a page size is an integer from 1 to 500, got 1.5"),
            ("instances.update", vec![json!("Order"), json!("o1"), json!({}), json!({"expectedSeq": 1.5})], "an expected sequence is a non-negative integer, got 1.5"),
            ("instances.delete", vec![json!("Order"), json!("o1"), json!({"expectedSeq": -1})], "an expected sequence is a non-negative integer, got -1"),
            ("schemas.version", vec![json!("Order"), json!(1.5)], "a schema version is a positive integer, got 1.5"),
            ("events.read", vec![json!({"after": "tail"})], "an event cursor is a non-negative integer or head, got tail"),
            ("events.read", vec![json!({"limit": "2"})], "a page size is an integer from 1 to 500, got 2"),
            ("events.read", vec![json!({"exclude": "heartbeat"})], "exclude is a list of operation names"),
            ("events.read", vec![json!({"kinds": ["create", 3]})], "kinds: 3 is not one of create, update, delete, operation, publish, define"),
            ("namespaces.create", vec![json!(7)], "a namespace name is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters, not 7"),
        ];
        for (method, args, message) in cases {
            assert_eq!(
                refused(call(&engine, method, principal, &args)),
                (EngineErrorCode::InvalidArgument, message.to_owned()),
                "{method}"
            );
        }
        assert_eq!(
            refused(call(
                &engine,
                "instances.get",
                None,
                &[json!("Order"), json!("o1")]
            )),
            (
                EngineErrorCode::InvalidArgument,
                "an engine call needs a principal with a subject".to_owned()
            )
        );
    }
}

#[test]
fn a_principal_passed_as_json_is_checked_as_the_typescript_engine_checks_it() {
    let service =
        json!({"deployable": "worker", "serves": [], "subject": "sa-1", "standsIn": true});
    for (given, message) in [
        (
            json!(null),
            "an engine call needs a principal with a subject",
        ),
        (json!({}), "an engine call needs a principal with a subject"),
        (
            json!({"subject": "", "permissions": []}),
            "an engine call needs a principal with a subject",
        ),
        (
            json!({"subject": "service:worker", "permissions": ["Order.read"], "service": service}),
            "service worker stands in for an end user and holds no permissions (D37)",
        ),
        (
            json!({"subject": "bob", "permissions": [], "service": {"deployable": "", "serves": [], "subject": "sa-1", "standsIn": false}}),
            "a principal's service is { deployable, serves, subject, standsIn }",
        ),
        (
            json!({"subject": "bob", "permissions": [], "service": {"deployable": "worker", "serves": "jobs", "subject": "sa-1", "standsIn": false}}),
            "a principal's service is { deployable, serves, subject, standsIn }",
        ),
        (
            json!({"subject": "bob", "permissions": [], "service": {"deployable": "worker", "serves": [], "subject": "sa-1"}}),
            "a principal's service is { deployable, serves, subject, standsIn }",
        ),
        (
            json!({"subject": "bob", "permissions": [], "service": null}),
            "a principal's service is { deployable, serves, subject, standsIn }",
        ),
    ] {
        assert_eq!(
            principal_from_json(&given).unwrap_err().message(),
            message,
            "{given}"
        );
    }
    let principal = principal_from_json(&json!({"subject": "bob", "permissions": ["x"], "service": {"deployable": "worker", "serves": ["jobs"], "subject": "sa-1", "standsIn": false}})).unwrap();
    assert_eq!(principal.service.unwrap().deployable, "worker");
}

// A server's calls: deploy defines and publishes its documents as the
// engine's own principal, once, and health is what its readiness reads,
// answering on a closed engine too.
#[test]
fn deploy_publishes_a_document_once_and_health_reads_the_engine() {
    for target in targets("deploy_publishes_a_document_once") {
        let engine = target.open();
        let deployed = call(
            &engine,
            "deploy",
            None,
            &[json!([order_document()]), json!({"namespace": "default"})],
        )
        .unwrap();
        assert_eq!(
            deployed,
            Some(
                json!([{"name": "Order", "namespace": "default", "version": 1, "defined": true, "published": true}])
            )
        );
        let again = call(&engine, "deploy", None, &[json!([order_document()])]).unwrap();
        assert_eq!(
            again,
            Some(
                json!([{"name": "Order", "namespace": "default", "version": 1, "defined": false, "published": false}])
            )
        );
        assert!(matches!(
            call(&engine, "deploy", None, &[json!({})]),
            Err(Error::Usage(message)) if message.contains("a list of schema documents")
        ));
        let health = call(&engine, "health", None, &[]).unwrap().unwrap();
        assert_eq!(
            (&health["ready"], &health["closed"], &health["poisoned"]),
            (&json!(true), &json!(false), &json!(false))
        );
        engine.shutdown();
        let closed = call(&engine, "health", None, &[]).unwrap().unwrap();
        assert_eq!(
            (&closed["ready"], &closed["closed"]),
            (&json!(false), &json!(true))
        );
        assert!(CALLS.contains(&"deploy") && CALLS.contains(&"health"));
    }
}
