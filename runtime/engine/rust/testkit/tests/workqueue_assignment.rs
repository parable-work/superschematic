//! Assignment (D16), who an instance is assigned to, on the Rust engine
//! (D58): the cases of the work queue's `test/assignment.test.ts` (a
//! principal takes an unassigned instance for itself or gives it up;
//! assigning, reassigning and unassigning anyone else need the config's
//! permission; while an instance is assigned, only its assignee takes its
//! lease; and each refusal carries its code), on SQLite and, with the
//! variable set, on Postgres.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, EngineErrorCode, Error, Principal};
use support::{refusal, targets, veto, Target};
use workqueue_support::{
    define_refusal, job_flow, jobs_document, principal, publish, Instances, T0,
};

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn lead() -> Principal {
    principal("lena", &["jobs.assign"])
}

fn jobs(engine: &Engine) -> Instances<'_> {
    Instances {
        engine,
        schema: "Job",
    }
}

// world opens an engine whose Job composes Workflow, Lease and Assignment
// with the config given (none for null), with job j1 unassigned.
fn world(target: &Target, config: Value) -> Engine {
    let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(T0));
    let assignment = if config.is_null() {
        json!({"name": "Assignment"})
    } else {
        json!({"name": "Assignment", "config": config})
    };
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease"},
            assignment
        ])),
    );
    jobs(&engine).create("j1", json!({"title": "Build"}));
    engine
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    jobs(engine).invoke_on(who, "j1", operation, params, None)
}

fn assignee_of(engine: &Engine) -> Option<Value> {
    jobs(engine)
        .behavior("j1", "Assignment")
        .get("assignee")
        .cloned()
}

#[test]
fn a_principal_assigns_an_unassigned_instance_to_itself_and_unassigns_itself() {
    for target in targets("assignment_self") {
        let engine = world(&target, Value::Null);
        assert_eq!(assignee_of(&engine), None);
        assert_eq!(
            invoke(&engine, &worker(), "assign", json!({"to": "wren"})).unwrap(),
            json!({"assignee": "wren", "assignedAt": T0, "assignedBy": "wren"})
        );
        assert_eq!(assignee_of(&engine), Some(json!("wren")));
        let (_, _, reason, code, _) =
            veto(invoke(&engine, &worker(), "assign", json!({"to": "wren"})));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            (
                "it is already assigned to the caller",
                Some("already_assigned")
            )
        );
        assert_eq!(
            invoke(&engine, &worker(), "unassign", json!({})).unwrap(),
            json!({"assignee": "wren"})
        );
        assert_eq!(assignee_of(&engine), None);
        let (_, _, reason, code, _) = veto(invoke(&engine, &worker(), "unassign", json!({})));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            ("it is not assigned", Some("not_assigned"))
        );
        let events: Vec<Value> = jobs(&engine)
            .events("j1")
            .iter()
            .skip(1)
            .map(|event| {
                json!([
                    event.actor,
                    event.change["operation"],
                    event.change["patch"]
                ])
            })
            .collect();
        assert_eq!(
            events,
            [
                json!(["wren", "assign", {"behaviors": {"Assignment": {"assignee": "wren"}}}]),
                json!(["wren", "unassign", {"behaviors": {"Assignment": {"assignee": null}}}])
            ]
        );
    }
}

#[test]
fn without_a_permission_in_the_config_no_one_assigns_another_principal() {
    for target in targets("assignment_unconfigured") {
        let engine = world(&target, Value::Null);
        let (_, _, reason, code, _) =
            veto(invoke(&engine, &worker(), "assign", json!({"to": "otto"})));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            (
                "assigning another principal needs a permission, and its config names none",
                Some("not_configured")
            )
        );
        invoke(&engine, &worker(), "assign", json!({"to": "wren"})).unwrap();
        assert_eq!(
            veto(invoke(&engine, &other(), "assign", json!({"to": "otto"}))).2,
            "reassigning an instance assigned to another principal needs a permission, and its config names none"
        );
        assert_eq!(
            veto(invoke(&engine, &other(), "unassign", json!({}))).2,
            "unassigning another principal needs a permission, and its config names none"
        );
        assert_eq!(assignee_of(&engine), Some(json!("wren")));
    }
}

#[test]
fn with_the_configs_permission_a_principal_assigns_anyone_reassigns_and_unassigns() {
    for target in targets("assignment_permitted") {
        let engine = world(&target, json!({"permission": "jobs.assign"}));
        let refused = refusal(invoke(&engine, &worker(), "assign", json!({"to": "otto"})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "wren may not assign Job j1: assigning another principal needs permission jobs.assign"
            )
        );
        assert_eq!(
            invoke(&engine, &lead(), "assign", json!({"to": "otto"})).unwrap(),
            json!({"assignee": "otto", "assignedAt": T0, "assignedBy": "lena"})
        );
        assert_eq!(
            refusal(invoke(&engine, &worker(), "assign", json!({"to": "wren"}))).code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            refusal(invoke(&engine, &worker(), "unassign", json!({}))).code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            veto(invoke(&engine, &lead(), "assign", json!({"to": "otto"}))).2,
            "it is already assigned to that principal"
        );
        invoke(&engine, &lead(), "assign", json!({"to": "wren"})).unwrap();
        assert_eq!(assignee_of(&engine), Some(json!("wren")));
        assert_eq!(
            invoke(&engine, &lead(), "unassign", json!({})).unwrap(),
            json!({"assignee": "wren"})
        );
        assert_eq!(assignee_of(&engine), None);
    }
}

#[test]
fn while_assigned_only_the_assignee_takes_the_lease() {
    for target in targets("assignment_lease") {
        let engine = world(&target, json!({"permission": "jobs.assign"}));
        invoke(&engine, &lead(), "assign", json!({"to": "wren"})).unwrap();
        let (behavior, action, reason, code, _) =
            veto(invoke(&engine, &other(), "acquire", json!({})));
        assert_eq!(
            (
                behavior.as_str(),
                action.as_str(),
                reason.as_str(),
                code.as_deref()
            ),
            (
                "Assignment",
                "acquire",
                "it is assigned to another principal, who alone may take it",
                Some("assigned_to_another")
            )
        );
        assert_eq!(
            invoke(&engine, &worker(), "acquire", json!({})).unwrap()["token"],
            1
        );
        jobs(&engine)
            .invoke_on(&worker(), "j1", "release", json!({}), Some(1))
            .unwrap();
        invoke(&engine, &worker(), "unassign", json!({})).unwrap();
        assert_eq!(
            invoke(&engine, &other(), "acquire", json!({})).unwrap()["token"],
            3
        );
    }
}

#[test]
fn while_another_principal_holds_the_lease_leases_guard_refuses_an_assignment_like_any_other_write()
{
    for target in targets("assignment_leased") {
        let engine = world(&target, json!({"permission": "jobs.assign"}));
        invoke(&engine, &worker(), "acquire", json!({})).unwrap();
        assert_eq!(
            veto(invoke(&engine, &lead(), "assign", json!({"to": "otto"}))).0,
            "Lease"
        );
        assert_eq!(
            invoke(&engine, &worker(), "assign", json!({"to": "wren"})).unwrap(),
            json!({"assignee": "wren", "assignedAt": T0, "assignedBy": "wren"})
        );
    }
}

#[test]
fn its_permission_may_change_and_it_can_be_added_to_a_schema_with_instances_and_not_removed() {
    for target in targets("assignment_config_change") {
        let engine = target.open_workqueue();
        let flow = json!({"name": "Workflow", "config": job_flow()});
        publish(&engine, &jobs_document(json!([flow])));
        jobs(&engine).create("j1", json!({"title": "Before"}));
        publish(
            &engine,
            &jobs_document(json!([flow, {"name": "Assignment"}])),
        );
        assert_eq!(assignee_of(&engine), None);
        publish(
            &engine,
            &jobs_document(
                json!([flow, {"name": "Assignment", "config": {"permission": "jobs.assign"}}]),
            ),
        );
        let refused = define_refusal(&engine, &jobs_document(json!([flow])));
        assert!(
            refused.contains("behavior Assignment cannot be removed from type Job, which has instances: the assignments its instances hold would stay behind"),
            "{refused}"
        );
    }
}
