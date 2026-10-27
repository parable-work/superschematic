//! Acting for a subject (D77, primitive 4): a principal that carries
//! `actingFor: { subject }`, the person an agent or a service works for.
//! `checkPrincipal` refuses an empty subject, the principal's own, and one
//! beside the end user a service brings; a service standing in may act for
//! one. Every event the principal writes, an instance's create, update,
//! delete, operation and beside operation, records the subject as `for`
//! beside `actor`, and the runner's events for a reaction carry none. The
//! policy sees it: `"permission"` refuses it unless the principal holds
//! `actFor`, and a `{ rules }` policy decides it only by a rule that names
//! `actingFor`. Over HTTP and MCP the `Acting-For` header sets it when the
//! mount honors it, and is ignored when it does not. On SQLite and, with
//! the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::api::{
    handle, ActingForHeader, ApiOptions, Caller, Request, Response, ServiceCaller,
};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineEvent,
    EngineOptions, Error, Policy, Principal, ReadEvents, RunnerOptions, SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{Comments, Links, Reactions, Workflow};
use support::{alice, refusal, schema_document, targets, Target};

/// An event's kind, operation, actor, `for` and service.
type Wrote = (
    String,
    Option<String>,
    String,
    Option<String>,
    Option<String>,
);

fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Links)
        .with(Reactions)
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// The agent, with every permission on the two schemas.
fn agent() -> Principal {
    Principal::new("agent:reviewer", ["Task", "Project"])
}

fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    let engine = target.open_core_with(|options| {
        options.behaviors = behaviors();
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
        configure(options);
    });
    for document in [project(), task()] {
        engine
            .schemas()
            .define(&Principal::system(), &document, DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(
                &Principal::system(),
                document["name"].as_str().unwrap(),
                SchemaTarget::default(),
            )
            .expect("publish");
    }
    engine
}

fn schema(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

fn flow(states: [&str; 3]) -> Value {
    json!({"name": "Workflow", "config": {
        "states": states,
        "transitions": [{"from": states[0], "to": states[1]}, {"from": states[1], "to": states[2]}]
    }})
}

// A task that starts moves its project to active, as the runner.
fn project() -> Value {
    schema("Project", json!([flow(["planned", "active", "done"])]))
}

fn task() -> Value {
    schema(
        "Task",
        json!([
            flow(["todo", "doing", "done"]),
            {"name": "Comments", "config": {}},
            {"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}},
            {"name": "Reactions", "config": {"rules": [
                {"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}}
            ]}}
        ]),
    )
}

fn create(engine: &Engine, who: &Principal, schema: &str, id: &str) -> Result<(), Error> {
    engine
        .instances()
        .create(
            who,
            schema,
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .map(|_| ())
}

fn invoke(engine: &Engine, who: &Principal, id: &str, operation: &str, params: Value) -> Value {
    engine
        .instances()
        .invoke(who, "Task", id, operation, &params, WriteOptions::default())
        .expect("invoke")
}

fn events(engine: &Engine, schema: &str) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &Principal::system(),
            &ReadEvents {
                schema: Some(schema),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

// Each event as (kind, operation, actor, for, service).
fn who_wrote(events: &[EngineEvent]) -> Vec<Wrote> {
    events
        .iter()
        .filter(|event| event.instance_id.is_some())
        .map(|event| {
            (
                event.kind.clone(),
                event.change["operation"].as_str().map(str::to_owned),
                event.actor.clone(),
                event.for_subject.clone(),
                event.service.clone(),
            )
        })
        .collect()
}

fn row(kind: &str, operation: Option<&str>, actor: &str, for_subject: Option<&str>) -> Wrote {
    (
        kind.to_owned(),
        operation.map(str::to_owned),
        actor.to_owned(),
        for_subject.map(str::to_owned),
        None,
    )
}

#[test]
fn every_event_a_principal_acting_for_a_subject_writes_records_for_and_the_runners_none() {
    for target in targets("acting_for_events") {
        let engine = open_with(&target, |_| {});
        let for_alice = agent().acting_for("alice");
        create(&engine, &alice(), "Project", "p1").unwrap();
        create(&engine, &for_alice, "Task", "t1").unwrap();
        create(&engine, &for_alice, "Task", "t2").unwrap();
        engine
            .instances()
            .update(
                &for_alice,
                "Task",
                "t1",
                &json!({"title": "Review the change"}),
                WriteOptions::default(),
            )
            .expect("update");
        invoke(
            &engine,
            &for_alice,
            "t1",
            "link",
            json!({"name": "project", "id": "p1"}),
        );
        invoke(&engine, &for_alice, "t1", "post", json!({"body": "On it."}));
        invoke(
            &engine,
            &for_alice,
            "t1",
            "transition",
            json!({"to": "doing"}),
        );
        engine
            .instances()
            .delete(&for_alice, "Task", "t2", WriteOptions::default())
            .expect("delete");
        assert_eq!(
            who_wrote(&events(&engine, "Task")),
            [
                row("create", None, "agent:reviewer", Some("alice")),
                row("create", None, "agent:reviewer", Some("alice")),
                row("update", None, "agent:reviewer", Some("alice")),
                row("operation", Some("link"), "agent:reviewer", Some("alice")),
                row("operation", Some("post"), "agent:reviewer", Some("alice")),
                row(
                    "operation",
                    Some("transition"),
                    "agent:reviewer",
                    Some("alice")
                ),
                row("delete", None, "agent:reviewer", Some("alice")),
            ]
        );
        let beside = events(&engine, "Task")
            .into_iter()
            .find(|event| event.beside)
            .expect("the post's beside event");
        assert_eq!(
            (
                serde_json::to_value(&beside).unwrap()["for"].clone(),
                beside.seq
            ),
            (json!("alice"), None)
        );
        // The runner's move of the project, a reaction to the agent's
        // transition, is the runner's own: it acts for no one.
        engine.runner().run_due().expect("run due");
        let project = events(&engine, "Project");
        assert_eq!(
            who_wrote(&project),
            [
                row("create", None, "alice", None),
                row("operation", Some("transition"), "runner", None),
            ]
        );
        assert!(project.last().unwrap().cause.is_some());
        assert_eq!(
            serde_json::to_value(project.last().unwrap())
                .unwrap()
                .get("for"),
            None
        );
        // A runner whose principal acts for someone is refused.
        let refused = Engine::open({
            let mut options = target.core_options();
            options.runner = Some(RunnerOptions::new(runner().acting_for("alice")));
            options
        })
        .unwrap_err();
        assert_eq!(
            refused.to_string(),
            "runner.principal acts for no one: reactions and schedules write as the runner"
        );
    }
}

#[test]
fn check_principal_refuses_an_empty_subject_its_own_and_one_beside_a_services_end_user() {
    for target in targets("acting_for_check_principal") {
        let engine = open_with(&target, |_| {});
        let service = |stands_in: bool| superschematic_engine::PrincipalService {
            deployable: "billing".into(),
            serves: vec!["api".into()],
            subject: "sa@issuer".into(),
            stands_in,
        };
        for (who, message) in [
            (
                agent().acting_for(""),
                "a principal's actingFor is { subject }, a subject that is not empty".to_owned(),
            ),
            (
                agent().acting_for("agent:reviewer"),
                "agent:reviewer cannot act for itself: actingFor names another subject".to_owned(),
            ),
            (
                Principal::new("bob", ["Task"])
                    .with_service(service(false))
                    .acting_for("alice"),
                "service billing brings end user bob, so the call cannot act for alice too: only a service standing in acts for a subject".to_owned(),
            ),
        ] {
            let refused = refusal(create(&engine, &who, "Task", "t1"));
            assert_eq!(
                (refused.code(), refused.message().to_owned()),
                (EngineErrorCode::InvalidArgument, message)
            );
        }
        // A service standing in (D37) may act for a subject.
        let standing =
            Principal::service("billing", &["api".into()], "sa@issuer").acting_for("alice");
        create(&engine, &standing, "Task", "t1").unwrap();
        assert_eq!(
            who_wrote(&events(&engine, "Task")),
            [(
                "create".to_owned(),
                None,
                "service:billing".to_owned(),
                Some("alice".to_owned()),
                Some("billing".to_owned())
            )]
        );
    }
}

#[test]
fn the_permission_policy_refuses_acting_for_unless_the_principal_holds_act_for() {
    for target in targets("acting_for_permission") {
        let engine = open_with(&target, |options| {
            options.policy = Some(Policy::permission())
        });
        let refused = refusal(create(&engine, &agent().acting_for("alice"), "Task", "t1"));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "agent:reviewer may not write Task in namespace default"
            )
        );
        create(&engine, &agent(), "Task", "t1").unwrap();
        let trusted =
            Principal::new("agent:reviewer", ["Task.write", "actFor"]).acting_for("alice");
        create(&engine, &trusted, "Task", "t2").unwrap();
        // actFor adds nothing to the principal's own permissions.
        assert_eq!(
            refusal(create(&engine, &trusted, "Project", "p1")).code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            refusal(
                engine
                    .instances()
                    .get(&trusted, "Task", "t2", Default::default())
            )
            .code(),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn a_rule_decides_a_principal_acting_for_a_subject_only_when_it_names_acting_for() {
    for target in targets("acting_for_rules") {
        let policy =
            |rules: Value| Policy::from_json(&json!({ "rules": rules })).expect("the policy reads");
        let engine = open_with(&target, |options| {
            options.policy = Some(policy(json!([
                {"subject": "agent:reviewer", "allow": true},
                {"subject": "system", "allow": true}
            ])))
        });
        create(&engine, &agent(), "Task", "t1").unwrap();
        assert_eq!(
            refusal(create(&engine, &agent().acting_for("alice"), "Task", "t2")).code(),
            EngineErrorCode::Forbidden,
            "a rule written before actingFor matches no principal acting for a subject"
        );
        drop(engine);
        let engine = open_with(&target, |options| {
            options.policy = Some(policy(json!([
                {"actingFor": ["alice"], "require": "{schema}.{action}"},
                {"actingFor": true, "action": "read", "allow": true},
                {"allow": true}
            ])))
        });
        create(&engine, &agent().acting_for("alice"), "Task", "t3").unwrap();
        assert_eq!(
            refusal(create(&engine, &agent().acting_for("bob"), "Task", "t4")).code(),
            EngineErrorCode::Forbidden
        );
        assert!(engine
            .instances()
            .get(&agent().acting_for("bob"), "Task", "t3", Default::default())
            .unwrap()
            .is_some());
    }
}

fn post(path: &str, caller: Caller, body: &Value, acting_for: Option<&str>) -> Request {
    let mut headers = vec![("content-type".to_owned(), "application/json".to_owned())];
    if let Some(subject) = acting_for {
        headers.push(("Acting-For".to_owned(), subject.to_owned()));
    }
    Request {
        method: "POST".to_owned(),
        prefix: "/api".to_owned(),
        path: format!("/api{path}"),
        headers,
        body: Some(body.to_string().into_bytes()),
        caller,
        request_id: "r1".to_owned(),
        ..Request::default()
    }
}

fn by(principal: Principal) -> Caller {
    Caller {
        principal: Some(principal),
        ..Caller::default()
    }
}

fn body_of(response: &Response) -> Value {
    serde_json::from_slice(response.bytes()).expect("a JSON body")
}

// create_over_http creates a task over HTTP and returns the response's
// status and code, and the for its event recorded.
fn create_over_http(
    engine: &Engine,
    options: &ApiOptions,
    caller: Caller,
    id: &str,
    acting_for: Option<&str>,
) -> (u16, Value, Option<String>) {
    let response = handle(
        engine,
        options,
        &post(
            "/namespaces/default/schemas/Task/instances",
            caller,
            &json!({"id": id, "data": {"title": id}}),
            acting_for,
        ),
    );
    let recorded = events(engine, "Task")
        .into_iter()
        .find(|event| event.instance_id.as_deref() == Some(id))
        .and_then(|event| event.for_subject);
    (
        response.status,
        body_of(&response)["code"].clone(),
        recorded,
    )
}

#[test]
fn the_acting_for_header_is_ignored_unless_the_mount_honors_it() {
    for target in targets("acting_for_header") {
        let engine = open_with(&target, |_| {});
        // Ignored by default: answered as if the header were not there.
        let ignoring = ApiOptions::default();
        assert_eq!(
            create_over_http(&engine, &ignoring, by(agent()), "t1", Some("alice")),
            (201, Value::Null, None)
        );
        // Honored: the principal acts for the header's subject.
        let honoring = ApiOptions {
            acting_for: ActingForHeader::Honor,
            ..ApiOptions::default()
        };
        assert_eq!(
            create_over_http(&engine, &honoring, by(agent()), "t2", Some("  alice ")),
            (201, Value::Null, Some("alice".to_owned()))
        );
        assert_eq!(
            create_over_http(&engine, &honoring, by(agent()), "t3", None),
            (201, Value::Null, None)
        );
        assert_eq!(
            create_over_http(&engine, &honoring, by(agent()), "t4", Some(" ")),
            (201, Value::Null, None),
            "an empty header names no one"
        );
        // checkPrincipal refuses a subject that is the caller's own, and one
        // beside the end user a service brings; a service standing in may.
        assert_eq!(
            create_over_http(
                &engine,
                &honoring,
                by(agent()),
                "t5",
                Some("agent:reviewer")
            ),
            (400, json!("invalid_argument"), None)
        );
        let billing = ServiceCaller {
            deployable: "billing".into(),
            serves: vec!["api".into()],
            subject: "sa@issuer".into(),
        };
        assert_eq!(
            create_over_http(
                &engine,
                &honoring,
                Caller {
                    principal: Some(Principal::new("bob", ["Task"])),
                    service: Some(billing.clone()),
                    refused: None,
                },
                "t6",
                Some("alice")
            ),
            (400, json!("invalid_argument"), None)
        );
        assert_eq!(
            create_over_http(
                &engine,
                &honoring,
                Caller {
                    principal: None,
                    service: Some(billing),
                    refused: None,
                },
                "t7",
                Some("alice")
            ),
            (201, Value::Null, Some("alice".to_owned()))
        );
        // A function decides per request, seeing the principal the engine
        // established and the subject.
        let deciding = ApiOptions {
            acting_for: ActingForHeader::decide(|principal, subject, request| {
                principal.subject == "agent:reviewer"
                    && subject != "carol"
                    && request.header("acting-for").is_some()
            }),
            ..ApiOptions::default()
        };
        assert_eq!(
            create_over_http(&engine, &deciding, by(agent()), "t8", Some("alice")),
            (201, Value::Null, Some("alice".to_owned()))
        );
        assert_eq!(
            create_over_http(&engine, &deciding, by(agent()), "t9", Some("carol")),
            (201, Value::Null, None)
        );
        // The policy still decides what the honored principal may do.
        drop(engine);
        let engine = open_with(&target, |options| {
            options.policy = Some(Policy::permission())
        });
        assert_eq!(
            create_over_http(&engine, &honoring, by(agent()), "t10", Some("alice")),
            (403, json!("forbidden"), None)
        );
        assert_eq!(
            create_over_http(&engine, &ignoring, by(agent()), "t11", Some("alice")),
            (201, Value::Null, None)
        );
    }
}

#[test]
fn the_mcp_endpoint_honors_the_header_as_the_routes_do() {
    for target in targets("acting_for_mcp") {
        let engine = open_with(&target, |_| {});
        let call = |options: &ApiOptions, id: &str| {
            let message = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "task_create", "arguments": {"id": id, "data": {"title": id}}}});
            let mut request = post(
                "/namespaces/default/mcp",
                by(agent()),
                &message,
                Some("alice"),
            );
            request.headers.push((
                "accept".to_owned(),
                "application/json, text/event-stream".to_owned(),
            ));
            request
                .headers
                .push(("mcp-protocol-version".to_owned(), "2025-06-18".to_owned()));
            let response = handle(&engine, options, &request);
            assert_eq!(response.status, 200);
            let text = String::from_utf8(response.bytes().to_vec()).unwrap();
            let answer: Value = text
                .lines()
                .find_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str(data).unwrap())
                .unwrap();
            assert!(
                answer["result"]["structuredContent"]["id"] == json!(id),
                "{answer}"
            );
            events(&engine, "Task")
                .into_iter()
                .find(|event| event.instance_id.as_deref() == Some(id))
                .and_then(|event| event.for_subject)
        };
        assert_eq!(call(&ApiOptions::default(), "t1"), None);
        let honoring = ApiOptions {
            acting_for: ActingForHeader::Honor,
            ..ApiOptions::default()
        };
        assert_eq!(call(&honoring, "t2"), Some("alice".to_owned()));
    }
}
