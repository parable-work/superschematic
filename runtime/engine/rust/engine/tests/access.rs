//! The access policy, the cases of `test/access.test.ts`: every entry point
//! asks it before reading or writing, a refusal is forbidden and changes
//! nothing, the policy is asked before anything is looked up, lists show
//! only what the principal may read, a call needs a principal with a
//! subject, and a calling service (D37) reaches the policy and the log; on
//! SQLite and, with the variable set, on Postgres. The policies here are
//! Rust policies, as a deployment's engine crate compiles one; the rules a
//! deployment configures are `tests/policy.rs`'s. A policy's answer is a
//! bool, so "only true allows" is the type's; the replay holds the answer
//! tables to it.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::{
    AccessPolicy, AccessRequest, CreateOptions, DefineOptions, Engine, EngineErrorCode,
    EventsAfter, GetOptions, ListOptions, NamespaceOptions, Policy, Principal, PrincipalService,
    ReadEvents, SchemaTarget, ValidateOptions, WriteOptions,
};
use support::{alice, code, order_document, refusal, schema_document, targets, with, Target};

fn note_document() -> Value {
    schema_document(
        "Note",
        json!([{"name": "body", "typeRef": {"name": "string"}}]),
        json!({}),
    )
}

fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(
        subject,
        permissions
            .iter()
            .map(|permission| (*permission).to_owned()),
    )
}

fn owner() -> Principal {
    principal(
        "owner",
        &[
            "Order.define",
            "Order.publish",
            "Order.read",
            "Order.write",
            "Note.define",
            "Note.publish",
            "Note.read",
            "Note.write",
        ],
    )
}

// A policy a deployment might write: a permission per schema and action,
// such as `Order.read`, held exactly; a service whose deployable is listed
// may also write orders.
struct ByPermission {
    services: Vec<String>,
    asked: Option<Arc<Mutex<Vec<Principal>>>>,
}

impl ByPermission {
    fn exact() -> Self {
        ByPermission {
            services: Vec::new(),
            asked: None,
        }
    }
}

impl AccessPolicy for ByPermission {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        if let Some(asked) = &self.asked {
            asked.lock().unwrap().push(request.principal().clone());
        }
        let principal = request.principal();
        let wanted = format!(
            "{}.{}",
            request.schema().unwrap_or("undefined"),
            request.action()
        );
        principal.permissions.contains(&wanted)
            || (request.schema() == Some("Order")
                && principal
                    .service
                    .as_ref()
                    .is_some_and(|service| self.services.contains(&service.deployable)))
    }
}

fn seeded(target: &Target, policy: impl AccessPolicy + 'static) -> Engine {
    let engine = target.open_with(|options| options.policy = Some(Policy::custom(policy)));
    for document in [order_document(), note_document()] {
        engine
            .schemas()
            .define(&owner(), &document, DefineOptions::default())
            .unwrap();
        engine
            .schemas()
            .publish(
                &owner(),
                document["name"].as_str().unwrap(),
                SchemaTarget::default(),
            )
            .unwrap();
    }
    for (schema, data, id) in [
        ("Order", json!({"title": "Desk"}), "o1"),
        ("Note", json!({"body": "hello"}), "n1"),
    ] {
        engine
            .instances()
            .create(
                &owner(),
                schema,
                &data,
                CreateOptions {
                    id: Some(id),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
    }
    engine
}

// Records every question as `subject action namespace/schema`, and allows
// it.
#[derive(Clone, Default)]
struct Recording {
    asked: Arc<Mutex<Vec<String>>>,
}

impl AccessPolicy for Recording {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        self.asked.lock().unwrap().push(format!(
            "{} {} {}/{}",
            request.principal().subject,
            request.action(),
            request.namespace(),
            request.schema().unwrap_or("undefined")
        ));
        true
    }
}

/// A call of the engine, true when it succeeded.
type Call<'a> = Box<dyn Fn() -> bool + 'a>;

#[test]
fn every_entry_point_asks_the_policy_with_its_action_namespace_and_schema() {
    for target in targets("every_entry_point_asks_the_policy") {
        let recording = Recording::default();
        let policy = recording.clone();
        let engine = target.open_with(|options| {
            options.policy = Some(Policy::custom(policy));
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            };
        });
        let east = Some("east");
        let at = SchemaTarget { namespace: east };
        let a = alice();
        let calls: Vec<(&str, Call<'_>)> = vec![
            (
                "alice define east/Order",
                Box::new(|| {
                    engine
                        .schemas()
                        .define(
                            &a,
                            order_document(),
                            DefineOptions {
                                namespace: east,
                                source: None,
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice publish east/Order",
                Box::new(|| engine.schemas().publish(&a, "Order", at).is_ok()),
            ),
            (
                "alice read east/Order",
                Box::new(|| engine.schemas().live(&a, "Order", at).is_ok()),
            ),
            (
                "alice read east/Order",
                Box::new(|| engine.schemas().draft(&a, "Order", at).is_ok()),
            ),
            (
                "alice read east/Order",
                Box::new(|| engine.schemas().version(&a, "Order", 1, at).is_ok()),
            ),
            (
                "alice read east/Order",
                Box::new(|| engine.schemas().list(&a, at).is_ok()),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .schemas()
                        .validate(
                            &a,
                            "Order",
                            &json!({"title": "Desk"}),
                            ValidateOptions {
                                namespace: east,
                                version: None,
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .schemas()
                        .validator(
                            &a,
                            "Order",
                            ValidateOptions {
                                namespace: east,
                                version: None,
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice write east/Order",
                Box::new(|| {
                    engine
                        .instances()
                        .create(
                            &a,
                            "Order",
                            &json!({"title": "Desk"}),
                            CreateOptions {
                                id: Some("o1"),
                                namespace: east,
                                behaviors: None,
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .instances()
                        .get(
                            &a,
                            "Order",
                            "o1",
                            GetOptions {
                                namespace: east,
                                value_refs: false,
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .instances()
                        .list(
                            &a,
                            "Order",
                            ListOptions {
                                namespace: east,
                                ..ListOptions::default()
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice write east/Order",
                Box::new(|| {
                    engine
                        .instances()
                        .update(
                            &a,
                            "Order",
                            "o1",
                            &json!({"title": "Lamp"}),
                            WriteOptions {
                                namespace: east,
                                ..WriteOptions::default()
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice write east/Order",
                Box::new(|| {
                    engine
                        .instances()
                        .delete(
                            &a,
                            "Order",
                            "o1",
                            WriteOptions {
                                namespace: east,
                                ..WriteOptions::default()
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .events()
                        .read(
                            &a,
                            &ReadEvents {
                                namespace: east,
                                ..ReadEvents::default()
                            },
                        )
                        .is_ok()
                }),
            ),
            (
                "alice read east/Order",
                Box::new(|| {
                    engine
                        .events()
                        .read(
                            &a,
                            &ReadEvents {
                                namespace: east,
                                schema: Some("Order"),
                                ..ReadEvents::default()
                            },
                        )
                        .is_ok()
                }),
            ),
        ];
        for (expected, call) in calls {
            recording.asked.lock().unwrap().clear();
            assert!(call(), "{expected}");
            assert_eq!(*recording.asked.lock().unwrap(), [expected], "{expected}");
        }
    }
}

#[test]
fn a_refused_call_is_forbidden_and_changes_nothing() {
    for target in targets("a_refused_call_is_forbidden_and_changes_nothing") {
        let engine = seeded(&target, ByPermission::exact());
        let reader = principal("reader", &["Order.read"]);
        let events_before = engine
            .events()
            .read(&owner(), &ReadEvents::default())
            .unwrap()
            .events
            .len();
        let any = SchemaTarget::default();
        let refusals = [
            (
                "reader may not define Order in namespace default",
                refusal(engine.schemas().define(
                    &reader,
                    with(&order_document(), "description", json!("x")),
                    DefineOptions::default(),
                )),
            ),
            (
                "reader may not publish Order in namespace default",
                refusal(engine.schemas().publish(&reader, "Order", any)),
            ),
            (
                "reader may not write Order in namespace default",
                refusal(engine.instances().create(
                    &reader,
                    "Order",
                    &json!({"title": "Lamp"}),
                    CreateOptions::default(),
                )),
            ),
            (
                "reader may not write Order in namespace default",
                refusal(engine.instances().update(
                    &reader,
                    "Order",
                    "o1",
                    &json!({"title": "Lamp"}),
                    WriteOptions::default(),
                )),
            ),
            (
                "reader may not write Order in namespace default",
                refusal(
                    engine
                        .instances()
                        .delete(&reader, "Order", "o1", WriteOptions::default()),
                ),
            ),
            (
                "reader may not read Note in namespace default",
                refusal(
                    engine
                        .instances()
                        .get(&reader, "Note", "n1", GetOptions::default()),
                ),
            ),
            (
                "reader may not read Note in namespace default",
                refusal(engine.schemas().live(&reader, "Note", any)),
            ),
            (
                "reader may not read Note in namespace default",
                refusal(engine.events().read(
                    &reader,
                    &ReadEvents {
                        schema: Some("Note"),
                        ..ReadEvents::default()
                    },
                )),
            ),
        ];
        for (message, refused) in refusals {
            assert_eq!(
                (refused.code(), refused.message(), refused.status()),
                (EngineErrorCode::Forbidden, message, 403)
            );
        }
        assert_eq!(
            engine.schemas().draft(&owner(), "Order", any).unwrap(),
            None
        );
        let order = engine
            .instances()
            .get(&owner(), "Order", "o1", GetOptions::default())
            .unwrap()
            .unwrap();
        assert_eq!(Value::Object(order.data), json!({"title": "Desk"}));
        assert_eq!(
            engine
                .instances()
                .list(&owner(), "Order", ListOptions::default())
                .unwrap()
                .items
                .len(),
            1
        );
        assert_eq!(
            engine
                .events()
                .read(&owner(), &ReadEvents::default())
                .unwrap()
                .events
                .len(),
            events_before
        );
        // What the reader may do still works.
        assert_eq!(
            engine
                .instances()
                .get(&reader, "Order", "o1", GetOptions::default())
                .unwrap()
                .unwrap()
                .data["title"],
            "Desk"
        );
    }
}

#[test]
fn the_policy_is_asked_before_the_engine_looks_anything_up() {
    for target in targets("the_policy_is_asked_before_the_engine_looks_anything_up") {
        let engine = seeded(&target, ByPermission::exact());
        let stranger = principal("stranger", &[]);
        assert_eq!(
            code(
                engine
                    .instances()
                    .get(&stranger, "Order", "missing", GetOptions::default())
            ),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(
                engine
                    .instances()
                    .get(&stranger, "Unknown", "o1", GetOptions::default())
            ),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn lists_show_only_what_the_principal_may_read() {
    for target in targets("lists_show_only_what_the_principal_may_read") {
        let engine = seeded(&target, ByPermission::exact());
        let reader = principal("reader", &["Order.read"]);
        assert_eq!(
            engine
                .schemas()
                .list(&reader, SchemaTarget::default())
                .unwrap()
                .into_iter()
                .map(|summary| summary.name)
                .collect::<Vec<_>>(),
            ["Order"]
        );
        assert_eq!(
            engine
                .events()
                .read(&reader, &ReadEvents::default())
                .unwrap()
                .events
                .iter()
                .map(|event| (event.kind.as_str(), event.schema.as_str()))
                .collect::<Vec<_>>(),
            [
                ("define", "Order"),
                ("publish", "Order"),
                ("create", "Order")
            ]
        );
        // A read from the head asks nothing it may not ask.
        assert_eq!(
            engine
                .events()
                .read(
                    &reader,
                    &ReadEvents {
                        after: Some(EventsAfter::Head),
                        ..ReadEvents::default()
                    }
                )
                .unwrap()
                .events,
            []
        );
    }
}

#[test]
fn a_call_needs_a_principal_with_a_subject() {
    for target in targets("a_call_needs_a_principal_with_a_subject") {
        let engine = target.open();
        let nobody = principal("", &[]);
        for refused in [
            refusal(engine.schemas().list(&nobody, SchemaTarget::default())),
            refusal(
                engine
                    .instances()
                    .get(&nobody, "Order", "o1", GetOptions::default()),
            ),
            refusal(engine.events().read(&nobody, &ReadEvents::default())),
            refusal(engine.values().get(&nobody, &"a".repeat(64), None)),
        ] {
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::InvalidArgument,
                    "an engine call needs a principal with a subject"
                )
            );
        }
    }
}

#[test]
fn what_a_call_writes_records_its_principal_as_the_actor() {
    for target in targets("what_a_call_writes_records_its_principal_as_the_actor") {
        let engine = seeded(&target, ByPermission::exact());
        let writer = principal("writer", &["Order.write", "Order.read"]);
        let updated = engine
            .instances()
            .update(
                &writer,
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            (updated.created_by.as_str(), updated.updated_by.as_str()),
            ("owner", "writer")
        );
        let live = engine
            .schemas()
            .live(&owner(), "Order", SchemaTarget::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            (live.published_by.as_deref(), live.defined_by.as_deref()),
            (Some("owner"), Some("owner"))
        );
        let events = engine
            .events()
            .read(
                &owner(),
                &ReadEvents {
                    schema: Some("Order"),
                    instance_id: Some("o1"),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events;
        assert_eq!(
            events
                .iter()
                .map(|event| event.actor.as_str())
                .collect::<Vec<_>>(),
            ["owner", "writer"]
        );
    }
}

#[test]
fn a_calling_service_reaches_the_policy_beside_the_end_user_it_acts_for_and_the_log_records_both() {
    for target in targets("a_calling_service_reaches_the_policy") {
        let asked = Arc::new(Mutex::new(Vec::new()));
        let engine = seeded(
            &target,
            ByPermission {
                services: Vec::new(),
                asked: Some(Arc::clone(&asked)),
            },
        );
        let for_bob =
            principal("bob", &["Order.write", "Order.read"]).with_service(PrincipalService {
                deployable: "worker".into(),
                serves: vec!["jobs".into()],
                subject: "sa-1".into(),
                stands_in: false,
            });
        let updated = engine
            .instances()
            .update(
                &for_bob,
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(updated.updated_by, "bob");
        assert_eq!(asked.lock().unwrap().last(), Some(&for_bob));
        assert!(!for_bob.stands_in());
        let events = engine
            .events()
            .read(
                &owner(),
                &ReadEvents {
                    schema: Some("Order"),
                    instance_id: Some("o1"),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events;
        assert_eq!(
            (events[1].actor.as_str(), events[1].service.as_deref()),
            ("bob", Some("worker"))
        );
        assert_eq!(
            serde_json::to_value(&events[1]).unwrap()["service"],
            "worker"
        );
    }
}

#[test]
fn a_service_with_no_end_user_stands_in_for_one_its_own_subject_no_permissions_and_the_policy_decides(
) {
    for target in targets("a_service_with_no_end_user_stands_in_for_one") {
        // The policy admits the worker deployable to write orders, as a
        // deployment admits a service along its edge; services hold no
        // permissions, so a permission rule alone admits none.
        let engine = seeded(
            &target,
            ByPermission {
                services: vec!["worker".into()],
                asked: None,
            },
        );
        let worker = Principal::service("worker", &["jobs".into()], "sa-1");
        assert_eq!(
            serde_json::to_value(&worker).unwrap(),
            json!({"subject": "service:worker", "permissions": [], "service": {"deployable": "worker", "serves": ["jobs"], "subject": "sa-1", "standsIn": true}})
        );
        assert!(worker.stands_in());
        let updated = engine
            .instances()
            .update(
                &worker,
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(updated.updated_by, "service:worker");
        let events = engine
            .events()
            .read(
                &owner(),
                &ReadEvents {
                    schema: Some("Order"),
                    instance_id: Some("o1"),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events;
        assert_eq!(
            (events[1].actor.as_str(), events[1].service.as_deref()),
            ("service:worker", Some("worker"))
        );
        let crawler = Principal::service("crawler", &[], "sa-2");
        assert_eq!(
            code(
                engine
                    .instances()
                    .get(&crawler, "Order", "o1", GetOptions::default())
            ),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn a_principals_service_is_checked_well_formed_and_holding_no_permission_when_it_stands_in() {
    for target in targets("a_principals_service_is_checked") {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), order_document(), DefineOptions::default())
            .unwrap();
        engine
            .schemas()
            .publish(&alice(), "Order", SchemaTarget::default())
            .unwrap();
        let mut standing = Principal::service("worker", &[], "sa-1");
        standing.permissions.push("Order.read".into());
        let nameless = principal("bob", &[]).with_service(PrincipalService {
            deployable: String::new(),
            serves: vec![],
            subject: "sa-1".into(),
            stands_in: true,
        });
        for malformed in [standing, nameless] {
            assert_eq!(
                code(
                    engine
                        .instances()
                        .get(&malformed, "Order", "o1", GetOptions::default())
                ),
                EngineErrorCode::InvalidArgument
            );
        }
        let acting = principal("bob", &["x"]).with_service(PrincipalService {
            deployable: "worker".into(),
            serves: vec![],
            subject: "sa-1".into(),
            stands_in: false,
        });
        assert_eq!(
            engine
                .instances()
                .get(&acting, "Order", "o1", GetOptions::default())
                .unwrap(),
            None
        );
    }
}
