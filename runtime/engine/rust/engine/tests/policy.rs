//! The access policy as data (D58): the policies of the two examples that
//! run the TypeScript engine by hand, `examples/engine-notes/src/auth.ts`
//! and `examples/engine-jobs/src/auth.ts`, written as rule tables, answer
//! every question as the TypeScript functions do. Each function is
//! transcribed here as a Rust policy, line for line, and both are asked
//! every question the engine asks, for every caller the example knows and
//! a few it does not.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    has_any_permission, AccessPolicy, AccessRequest, DottedPermissions, Engine, Error,
    ListingAction, NamespaceOperation, Policy, PolicyConfig, Principal, PrincipalService,
    RulePolicy, SchemaAction,
};
use support::targets;

/// engine-notes' policy as a rule table.
fn notes_rules() -> Value {
    json!({"rules": [
        // Making, archiving and listing namespaces: the example makes none
        // while it runs.
        {"action": "manage", "allow": false},
        {"action": ["define", "publish"], "require": "schemas.{action}"},
        // A reader may comment on a note and propose a change to it, but
        // not change it.
        {"action": "write", "operation": ["comment", "propose"], "require": "{schema}.read"},
        {"require": "{schema}.{action}"}
    ]})
}

/// engine-jobs' policy as a rule table.
fn jobs_rules() -> Value {
    json!({"rules": [
        {"action": "manage", "allow": false},
        {"action": ["define", "publish"], "require": "schemas.{action}"},
        // A worker may do the work, and nothing else that writes.
        {
            "action": "write",
            "operation": ["claimNext", "claim", "refresh", "heartbeat", "release", "recordUsage", "recordAttempt", "transition", "beat"],
            "require": ["{schema}.work", "{schema}.write"]
        },
        {"require": "{schema}.{action}"}
    ]})
}

// The TypeScript request's members as the functions destructure them.
struct Asked<'a> {
    principal: &'a Principal,
    action: &'a str,
    schema: Option<&'a str>,
    operation: Option<&'a str>,
}

fn asked<'a>(request: &'a AccessRequest<'a>) -> Asked<'a> {
    Asked {
        principal: request.principal(),
        action: request.action(),
        schema: request.schema(),
        operation: request.operation(),
    }
}

// `${schema}` of an absent schema is "undefined" in JavaScript.
fn schema_text(schema: Option<&str>) -> &str {
    schema.unwrap_or("undefined")
}

fn holds(principal: &Principal, required: &[String]) -> bool {
    has_any_permission(&principal.permissions, required)
}

/// engine-notes' `policy`, transcribed.
struct NotesFunction;

impl AccessPolicy for NotesFunction {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let Asked {
            principal,
            action,
            schema,
            operation,
        } = asked(request);
        if action == "manage" {
            return false;
        }
        if action == "define" || action == "publish" {
            return holds(principal, &[format!("schemas.{action}")]);
        }
        if action == "write" && (operation == Some("comment") || operation == Some("propose")) {
            return holds(principal, &[format!("{}.read", schema_text(schema))]);
        }
        holds(principal, &[format!("{}.{action}", schema_text(schema))])
    }
}

/// engine-jobs' `policy`, transcribed.
struct JobsFunction;

const WORK_OPERATIONS: [&str; 9] = [
    "claimNext",
    "claim",
    "refresh",
    "heartbeat",
    "release",
    "recordUsage",
    "recordAttempt",
    "transition",
    "beat",
];

impl AccessPolicy for JobsFunction {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let Asked {
            principal,
            action,
            schema,
            operation,
        } = asked(request);
        if action == "manage" {
            return false;
        }
        if action == "define" || action == "publish" {
            return holds(principal, &[format!("schemas.{action}")]);
        }
        if action == "write" && operation.is_some_and(|name| WORK_OPERATIONS.contains(&name)) {
            let schema = schema_text(schema);
            return holds(
                principal,
                &[format!("{schema}.work"), format!("{schema}.write")],
            );
        }
        holds(principal, &[format!("{}.{action}", schema_text(schema))])
    }
}

fn rules(policy: Value) -> RulePolicy {
    RulePolicy::new(
        &PolicyConfig::from_json(&policy).expect("the rule table reads"),
        std::sync::Arc::new(DottedPermissions),
    )
}

/// Every question the engine asks, for each principal: every schema
/// action on each schema, with and without each operation, every manage
/// operation on each namespace, and the listing questions.
fn questions<'a>(
    principals: &'a [Principal],
    schemas: &'a [&'a str],
    operations: &'a [&'a str],
) -> Vec<AccessRequest<'a>> {
    let mut out = Vec::new();
    for principal in principals {
        for namespace in ["default", "acme"] {
            for schema in schemas {
                for action in [
                    SchemaAction::Read,
                    SchemaAction::Write,
                    SchemaAction::Define,
                    SchemaAction::Publish,
                ] {
                    out.push(AccessRequest::Schema {
                        principal,
                        action,
                        namespace,
                        schema,
                        operation: None,
                    });
                    for operation in operations {
                        out.push(AccessRequest::Schema {
                            principal,
                            action,
                            namespace,
                            schema,
                            operation: Some(operation),
                        });
                    }
                }
            }
            for operation in [
                NamespaceOperation::Create,
                NamespaceOperation::Archive,
                NamespaceOperation::Unarchive,
                NamespaceOperation::List,
            ] {
                out.push(AccessRequest::Namespace {
                    principal,
                    namespace,
                    operation,
                });
                out.push(AccessRequest::Listing {
                    principal,
                    action: ListingAction::Manage,
                    namespace,
                    operation: Some(operation),
                });
            }
            out.push(AccessRequest::Listing {
                principal,
                action: ListingAction::Define,
                namespace,
                operation: None,
            });
        }
    }
    out
}

fn describe(request: &AccessRequest<'_>) -> String {
    request
        .to_json(json!(request.principal().subject))
        .to_string()
}

fn assert_same(function: &dyn AccessPolicy, table: &RulePolicy, asked: &[AccessRequest<'_>]) {
    let mut allowed = 0;
    for request in asked {
        let want = function.allows(request);
        assert_eq!(table.allows(request), want, "{}", describe(request));
        allowed += usize::from(want);
    }
    // The matrix holds questions of both answers.
    assert!(
        allowed > 0 && allowed < asked.len(),
        "{allowed} of {}",
        asked.len()
    );
}

fn worker_service(principal: Principal, deployable: &str) -> Principal {
    principal.with_service(PrincipalService {
        deployable: deployable.into(),
        serves: vec!["jobs".into()],
        subject: "sa-1".into(),
        stands_in: false,
    })
}

#[test]
fn engine_notes_policy_is_a_rule_table() {
    let principals = [
        Principal::new("alice", ["notes.read", "notes.write"]),
        Principal::new("bob", ["notes"]),
        Principal::new("carol", ["notes.read"]),
        Principal::new("dana", ["schemas"]),
        Principal::new("erin", ["schemas.define", "notes.publish", "notes.review"]),
        Principal::new("nobody", Vec::<String>::new()),
        worker_service(Principal::new("frank", ["notes.read"]), "editor"),
        Principal::service("crawler", &[], "sa-2"),
    ];
    let operations = ["comment", "propose", "transition", "approve"];
    let asked = questions(&principals, &["notes", "Note", "drafts"], &operations);
    assert_same(&NotesFunction, &rules(notes_rules()), &asked);
}

#[test]
fn engine_jobs_policy_is_a_rule_table() {
    let principals = [
        Principal::new("operator", ["schemas", "jobs", "workers", "batches"]),
        Principal::new(
            "worker-1",
            ["jobs.read", "jobs.work", "workers.read", "workers.work"],
        ),
        Principal::new(
            "runner",
            [
                "jobs.read",
                "jobs.write",
                "jobs.override",
                "workers.read",
                "workers.write",
                "batches.read",
                "batches.write",
            ],
        ),
        Principal::new("auditor", ["jobs.read", "schemas.publish"]),
        Principal::new("nobody", Vec::<String>::new()),
        worker_service(Principal::new("worker-2", ["jobs.work"]), "worker"),
    ];
    let mut operations: Vec<&str> = WORK_OPERATIONS.to_vec();
    operations.extend(["override", "cancel", "comment"]);
    let asked = questions(&principals, &["jobs", "workers", "batches"], &operations);
    assert_same(&JobsFunction, &rules(jobs_rules()), &asked);
}

#[test]
fn the_tables_read_back_as_written() {
    for table in [notes_rules(), jobs_rules()] {
        let config: PolicyConfig = serde_json::from_value(table.clone()).unwrap();
        assert_eq!(serde_json::to_value(config).unwrap(), table);
    }
}

#[test]
fn an_engine_needs_a_policy_it_can_read() {
    for target in targets("an_engine_needs_a_policy_it_can_read") {
        let mut options = target.options();
        options.policy = None;
        assert!(matches!(
            Engine::open(options),
            Err(Error::Usage(message)) if message == "an engine needs an access policy (policy); pass allowAll for tests and local use"
        ));
        assert!(matches!(
            Policy::from_json(&json!({"rules": [{"action": "admin", "allow": true}]})),
            Err(Error::Usage(message)) if message.starts_with("policy: rules[0].action")
        ));
        let mut options = target.options();
        options.policy = Some(Policy::from_json(&json!("permission")).unwrap());
        Engine::open(options).unwrap();
    }
}
