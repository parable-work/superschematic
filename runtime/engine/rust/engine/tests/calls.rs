//! The engine's calls by name with JSON arguments (`calls`), which the
//! bindings and the scenario replayer make: what JSON can say that Rust's
//! types cannot (a page size, a sequence or a version that is no integer,
//! a malformed principal) refused in the TypeScript engine's words, and a
//! call's result as the TypeScript method returns it; on SQLite and, with
//! the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::calls::{call, principal_from_json, CALLS};
use superschematic_engine::{EngineErrorCode, Error};
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
            call(&engine, "runner.runDue", principal, &[]),
            Err(Error::Usage(_))
        ));
        assert!(CALLS.contains(&"instances.lookup"));
    }
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
