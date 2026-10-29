//! Links, the core's typed links: the cases of `test/core-links.test.ts`
//! (link and unlink, links a create gives, every required one among them,
//! the targets field, links pinned to a revision, with the target's latest
//! and their staleness, the delete of a target, refused for a required
//! link and cleared for an optional one as the caller, the schema-level
//! listLinked, reads as the caller, and its config rules), on SQLite and,
//! with the variable set, on Postgres. Links pinned to a release of a
//! target whose schema composes Branches run here as those pinned to a
//! revision do. The describe document is milestone 6's; the guidance and
//! create parameters are held to the TypeScript text in the behaviors
//! crate's tests. What a move of a link's target moves on (`revised`, which
//! Reactions and Lease hear), a revision or a release, runs through a test
//! behavior of this file.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::Scope;
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineError, EngineErrorCode, EngineEvent, EngineOptions, Error, Hooks, InstanceRecord, Policy,
    Principal, ReadEvents, Result, SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{
    link_pin, moved_on, target_move, Branches, Comments, Links, Revisions, TargetMove, Workflow,
};
use support::{
    alice, issue, issues_of, refusal, schema_document, targets, veto, RecordingPolicy, Target,
};

fn bob() -> Principal {
    Principal::new("bob", Vec::<String>::new())
}

fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Links)
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

fn task_links() -> Value {
    json!({
        "spec": {"schema": "Spec", "pinned": true},
        "owner": {"schema": "Person", "required": true},
        "related": {"schema": "Task"}
    })
}

// task_links with members changed.
fn task_links_with(members: Value) -> Value {
    let mut links = task_links();
    for (key, value) in members.as_object().expect("an object") {
        links[key] = value.clone();
    }
    links
}

fn tasks(links: Value) -> Value {
    schema(
        "Task",
        json!([{"name": "Links", "config": {"links": links}}]),
    )
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) -> i64 {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish")
        .version
}

fn create(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: Option<&str>,
    behaviors: Option<Value>,
) -> Result<InstanceRecord> {
    engine.instances().create(
        principal,
        schema,
        &json!({"title": id.unwrap_or("untitled")}),
        CreateOptions {
            id,
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

// A policy that allows alice everything and anyone else what the rule
// says.
fn policy(
    rule: impl Fn(&AccessRequest<'_>) -> bool + Send + Sync + 'static,
) -> impl FnOnce(&mut EngineOptions) {
    let policy = RecordingPolicy::new(move |request| {
        request.principal().subject == "alice" || rule(request)
    });
    move |options| options.policy = Some(Policy::custom(policy))
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

// Specs s1 and s2 keep revisions; people p1 and p2; tasks t1 to t3, whose
// required owner their create gives: p1 owns t1, p2 the others.
fn world(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    let engine = open(target, configure);
    publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
    publish(&engine, &schema("Person", json!([])));
    publish(&engine, &tasks(task_links()));
    for id in ["s1", "s2"] {
        create(&engine, &alice(), "Spec", Some(id), None).expect("spec");
    }
    for id in ["p1", "p2"] {
        create(&engine, &alice(), "Person", Some(id), None).expect("person");
    }
    for id in ["t1", "t2", "t3"] {
        let owner = if id == "t1" { "p1" } else { "p2" };
        create(
            &engine,
            &alice(),
            "Task",
            Some(id),
            Some(json!({"Links": {"owner": owner}})),
        )
        .expect("task");
    }
    engine
}

fn invoke(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn link(engine: &Engine, id: &str, params: Value) -> Result<Value> {
    invoke(engine, &alice(), "Task", id, "link", params)
}

fn unlink(engine: &Engine, id: &str, params: Value) -> Result<Value> {
    invoke(engine, &alice(), "Task", id, "unlink", params)
}

fn get(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
}

fn links_of(engine: &Engine, id: &str) -> Option<Value> {
    get(engine, "Task", id)
        .expect("the task")
        .behavior_field("Links", "targets")
        .cloned()
}

fn update(engine: &Engine, schema: &str, id: &str, patch: Value) {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
        .expect("update");
}

fn delete(engine: &Engine, principal: &Principal, schema: &str, id: &str) -> Result<bool> {
    engine
        .instances()
        .delete(principal, schema, id, WriteOptions::default())
}

fn events(engine: &Engine, schema: &str, id: &str) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

fn last_cursor(engine: &Engine) -> i64 {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .last()
        .map_or(0, |event| event.cursor)
}

fn events_after(engine: &Engine, after: i64) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                after: Some(superschematic_engine::EventsAfter::Cursor(after)),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

fn listed(engine: &Engine, schema: &str, params: Value) -> Result<Value> {
    engine
        .instances()
        .invoke_schema(&alice(), schema, "listLinked", &params, Default::default())
}

fn changes(error: &EngineError) -> Vec<String> {
    error
        .changes()
        .iter()
        .map(|change| change.message.clone())
        .collect()
}

#[test]
fn link_points_a_named_link_at_an_instance_and_targets_holds_them_all_by_name() {
    for target in targets("links_link") {
        let engine = world(&target, |_| {});
        assert_eq!(
            links_of(&engine, "t1"),
            Some(json!({"owner": {"schema": "Person", "id": "p1"}}))
        );
        assert_eq!(
            link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link"),
            json!({"name": "owner", "schema": "Person", "id": "p1"})
        );
        assert_eq!(
            link(&engine, "t1", json!({"name": "related", "id": "t2"})).expect("link"),
            json!({"name": "related", "schema": "Task", "id": "t2"})
        );
        assert_eq!(
            links_of(&engine, "t1"),
            Some(
                json!({"owner": {"schema": "Person", "id": "p1"}, "related": {"schema": "Task", "id": "t2"}})
            )
        );
        assert_eq!(
            events(&engine, "Task", "t1")
                .last()
                .expect("an event")
                .change,
            json!({
                "behavior": "Links",
                "operation": "link",
                "params": {"name": "related", "id": "t2"},
                "patch": {"behaviors": {"Links": {"targets": {"related": {"schema": "Task", "id": "t2"}}}}}
            })
        );
        link(&engine, "t1", json!({"name": "owner", "id": "p2"})).expect("move");
        assert_eq!(
            links_of(&engine, "t1"),
            Some(
                json!({"owner": {"schema": "Person", "id": "p2"}, "related": {"schema": "Task", "id": "t2"}})
            )
        );
        // Moved, the link no longer holds p1, which can go.
        assert!(delete(&engine, &alice(), "Person", "p1").expect("delete"));
    }
}

#[test]
fn link_refuses_a_name_the_config_lacks_a_missing_target_and_a_revision_for_a_link_not_pinned() {
    for target in targets("links_refusals") {
        let engine = world(&target, |_| {});
        let issues = |params: Value| issues_of(&refusal(link(&engine, "t1", params)));
        assert_eq!(
            issues(json!({"name": "boss", "id": "p1"})),
            [issue(
                "/name",
                "Task has no link boss (its links: owner, related, spec)"
            )]
        );
        assert_eq!(
            issues(json!({"name": "owner", "id": "p9"})),
            [issue("/id", "Person p9 does not exist")]
        );
        assert_eq!(
            issues(json!({"name": "owner", "id": "p1", "revision": 1})),
            [issue(
                "/revision",
                "link owner is not pinned, so it records no revision"
            )]
        );
        let extra: Vec<String> = issues(json!({"name": "owner", "id": "p1", "extra": 1}))
            .into_iter()
            .map(|(path, _)| path)
            .collect();
        assert_eq!(extra, [""]);
        assert_eq!(
            links_of(&engine, "t1"),
            Some(json!({"owner": {"schema": "Person", "id": "p1"}}))
        );
    }
}

#[test]
fn a_pinned_link_records_the_targets_revision_and_reports_when_the_target_moved_past_it() {
    for target in targets("links_pinned") {
        let engine = world(&target, |_| {});
        let spec_of = |id: &str| links_of(&engine, id).expect("links")["spec"].clone();
        assert_eq!(
            link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link"),
            json!({"name": "spec", "schema": "Spec", "id": "s1", "revision": 1})
        );
        assert_eq!(
            spec_of("t1"),
            json!({"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false})
        );
        update(&engine, "Spec", "s1", json!({"title": "s1, revised"}));
        assert_eq!(
            spec_of("t1"),
            json!({"schema": "Spec", "id": "s1", "revision": 1, "latest": 2, "stale": true})
        );
        assert_eq!(
            get(&engine, "Task", "t1").expect("t1").seq,
            2,
            "read when read: no event on the task"
        );
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        assert_eq!(
            spec_of("t1"),
            json!({"schema": "Spec", "id": "s1", "revision": 2, "latest": 2, "stale": false})
        );
        assert_eq!(
            link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1", "revision": 1})
            )
            .expect("link"),
            json!({"name": "spec", "schema": "Spec", "id": "s1", "revision": 1})
        );
        assert_eq!(spec_of("t1")["stale"], json!(true));
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1", "revision": 3})
            ))),
            [issue("/revision", "Spec s1 has revisions 1 to 2, not 3")]
        );
    }
}

#[test]
fn a_pinned_link_needs_a_target_schema_that_composes_revisions_and_a_target_with_a_revision() {
    for target in targets("links_pinned_needs") {
        let engine = open(&target, |_| {});
        publish(&engine, &schema("Spec", json!([])));
        publish(
            &engine,
            &schema(
                "Task",
                json!([{"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}}}}]),
            ),
        );
        create(&engine, &alice(), "Spec", Some("s1"), None).expect("s1");
        create(&engine, &alice(), "Task", Some("t1"), None).expect("t1");
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1"})
            ))),
            [issue(
                "/name",
                "link spec is pinned to a revision, but Spec does not compose Revisions"
            )]
        );
        // Revisions added to a schema with instances: s1 has none until it
        // changes.
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        let (behavior, action, reason, code, _) =
            veto(link(&engine, "t1", json!({"name": "spec", "id": "s1"})));
        assert_eq!(
            json!([behavior, action, reason, code]),
            json!([
                "Links",
                "link",
                "Spec s1 has no revision to pin yet",
                "no_revision"
            ])
        );
        update(&engine, "Spec", "s1", json!({"title": "s1, revised"}));
        assert_eq!(
            link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link")["revision"],
            json!(1)
        );
    }
}

#[test]
fn unlink_clears_an_optional_link_and_a_required_one_only_moves() {
    for target in targets("links_unlink") {
        let engine = world(&target, |_| {});
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link");
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        assert_eq!(
            unlink(&engine, "t1", json!({"name": "spec"})).expect("unlink"),
            json!({"name": "spec", "schema": "Spec", "id": "s1", "revision": 1})
        );
        let (_, _, reason, code, _) = veto(unlink(&engine, "t1", json!({"name": "owner"})));
        assert_eq!(
            json!([reason, code]),
            json!([
                "link owner is required: it can be moved with link, not unlinked",
                "required_link"
            ])
        );
        assert_eq!(
            issues_of(&refusal(unlink(&engine, "t1", json!({"name": "related"})))),
            [issue("/name", "Task t1 has no link related")]
        );
        assert_eq!(
            links_of(&engine, "t1"),
            Some(json!({"owner": {"schema": "Person", "id": "p1"}}))
        );
    }
}

#[test]
fn the_delete_of_a_required_links_target_is_refused_and_an_optional_ones_clears_the_link() {
    for target in targets("links_target_delete") {
        let engine = world(
            &target,
            policy(|request| matches!(request.schema(), Some("Person" | "Task"))),
        );
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link");
        link(&engine, "t1", json!({"name": "related", "id": "t2"})).expect("link");
        link(&engine, "t3", json!({"name": "related", "id": "t2"})).expect("link");
        let refused = delete(&engine, &bob(), "Person", "p1");
        let error = refusal(refused);
        assert_eq!(
            error.message(),
            "behavior Links vetoes delete of Person p1: an instance of Task links to it through its required link owner"
        );
        let (behavior, action, _, code, _) = veto(delete(&engine, &bob(), "Person", "p1"));
        assert_eq!(
            json!([behavior, action, code]),
            json!(["Links", "delete", "required_target"])
        );
        let after = last_cursor(&engine);
        assert!(delete(&engine, &bob(), "Task", "t2").expect("delete"));
        let seen: Vec<Value> = events_after(&engine, after)
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.instance_id,
                    event.actor,
                    event.change.get("operation")
                ])
            })
            .collect();
        assert_eq!(
            seen,
            [
                json!(["delete", "t2", "bob", null]),
                json!(["operation", "t1", "bob", "unlink"]),
                json!(["operation", "t3", "bob", "unlink"]),
            ]
        );
        assert_eq!(
            links_of(&engine, "t1"),
            Some(json!({"owner": {"schema": "Person", "id": "p1"}}))
        );
        assert_eq!(
            links_of(&engine, "t3"),
            Some(json!({"owner": {"schema": "Person", "id": "p2"}}))
        );
        // Moving the required link frees p1.
        link(&engine, "t1", json!({"name": "owner", "id": "p2"})).expect("move");
        assert!(delete(&engine, &bob(), "Person", "p1").expect("delete"));
    }
}

#[test]
fn the_delete_of_an_optional_target_acts_as_the_caller() {
    for target in targets("links_target_delete_caller") {
        let engine = world(
            &target,
            policy(|request| request.schema() == Some("Spec") || request.action() == "read"),
        );
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        let refused = refusal(delete(&engine, &bob(), "Spec", "s1"));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not call unlink (write) on Task in namespace default"
            )
        );
        assert!(delete(&engine, &bob(), "Spec", "s2").expect("delete"));
    }
}

#[test]
fn list_linked_is_schema_level_with_each_pinned_revision_and_only_the_stale_ones() {
    for target in targets("links_list_linked") {
        let engine = world(&target, |_| {});
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        update(&engine, "Spec", "s1", json!({"title": "v2"}));
        link(&engine, "t2", json!({"name": "spec", "id": "s1"})).expect("link");
        link(&engine, "t3", json!({"name": "spec", "id": "s2"})).expect("link");
        link(&engine, "t3", json!({"name": "related", "id": "t1"})).expect("link");
        let list = |params: Value| listed(&engine, "Task", params).expect("listLinked");
        assert_eq!(
            list(json!({"name": "spec", "id": "s1"})),
            json!({
                "items": [
                    {"id": "t1", "revision": 1, "latest": 2, "stale": true},
                    {"id": "t2", "revision": 2, "latest": 2, "stale": false}
                ],
                "next": null
            })
        );
        assert_eq!(
            list(json!({"name": "spec", "id": "s1", "stale": true})),
            json!({"items": [{"id": "t1", "revision": 1, "latest": 2, "stale": true}], "next": null})
        );
        assert_eq!(
            list(json!({"name": "related", "id": "t1"})),
            json!({"items": [{"id": "t3"}], "next": null})
        );
        assert_eq!(
            list(json!({"name": "related", "id": "t1", "stale": true})),
            json!({"items": [], "next": null})
        );
        let first = list(json!({"name": "spec", "id": "s1", "limit": 1}));
        assert_eq!(
            list(json!({"name": "spec", "id": "s1", "cursor": first["next"]})),
            json!({"items": [{"id": "t2", "revision": 2, "latest": 2, "stale": false}], "next": null})
        );
        assert_eq!(
            refusal(listed(&engine, "Task", json!({"name": "boss", "id": "p1"}))).code(),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            refusal(link_of_list(&engine)).code(),
            EngineErrorCode::NotFound
        );
    }
}

// listLinked is no instance operation.
fn link_of_list(engine: &Engine) -> Result<Value> {
    invoke(
        engine,
        &alice(),
        "Task",
        "t1",
        "listLinked",
        json!({"name": "spec", "id": "s1"}),
    )
}

#[test]
fn pinned_links_are_read_as_the_caller() {
    for target in targets("links_read_as_caller") {
        let engine = world(
            &target,
            policy(|request| matches!(request.schema(), Some("Task" | "Person"))),
        );
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        link(&engine, "t2", json!({"name": "owner", "id": "p1"})).expect("link");
        let read = |id: &str| {
            engine
                .instances()
                .get(&bob(), "Task", id, Default::default())
        };
        assert_eq!(refusal(read("t1")).code(), EngineErrorCode::Forbidden);
        assert_eq!(
            read("t2").expect("get").expect("t2").behaviors["Links"]["targets"],
            json!({"owner": {"schema": "Person", "id": "p1"}})
        );
        assert_eq!(
            refusal(invoke(
                &engine,
                &bob(),
                "Task",
                "t3",
                "link",
                json!({"name": "spec", "id": "s1"})
            ))
            .code(),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn its_config_names_links_in_camel_case_each_to_a_schema_and_it_joins_a_schema_with_instances() {
    for target in targets("links_config") {
        let engine = open(&target, |_| {});
        // The core meta-schema holds the config to the declaration's
        // configSchema.
        let refused = |config: Value| -> Vec<String> {
            refusal(define(
                &engine,
                &schema("Task", json!([{"name": "Links", "config": config}])),
            ))
            .issues()
            .iter()
            .map(|issue| issue.path.clone())
            .collect()
        };
        let at = |config: Value, path: &str| {
            let paths = refused(config.clone());
            assert!(
                paths.iter().any(|known| known == path),
                "{config}: {paths:?}"
            );
        };
        at(json!({}), "/types/Task/behaviors/0/config");
        at(json!({"links": {}}), "/types/Task/behaviors/0/config/links");
        at(
            json!({"links": {"Spec": {"schema": "Spec"}}}),
            "/types/Task/behaviors/0/config/links",
        );
        at(
            json!({"links": {"spec": {}}}),
            "/types/Task/behaviors/0/config/links/spec",
        );
        at(
            json!({"links": {"spec": {"schema": "Spec", "weak": true}}}),
            "/types/Task/behaviors/0/config/links/spec",
        );

        publish(&engine, &schema("Person", json!([])));
        publish(&engine, &schema("Task", json!([])));
        create(&engine, &alice(), "Person", Some("p1"), None).expect("p1");
        create(&engine, &alice(), "Task", Some("t1"), None).expect("t1");
        let change = |links: Value| changes(&refusal(define(&engine, &tasks(links))));
        // t1 holds no link, so Links comes with no required one.
        let added = change(json!({"owner": {"schema": "Person", "required": true}}));
        assert!(
            added[0].contains("cannot be added to type Task, which has instances: the instances that exist hold no link owner, which is required"),
            "{added:?}"
        );
        publish(&engine, &tasks(json!({"owner": {"schema": "Person"}})));
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link");
        // pinned may change and optional links may be added; no link
        // becomes required, as no field does: an instance may not hold it.
        assert!(change(json!({"owner": {"schema": "Person", "required": true}}))[0]
            .contains("link owner becomes required, and an instance the live version accepts may not hold it"));
        assert!(change(
            json!({"owner": {"schema": "Person"}, "boss": {"schema": "Person", "required": true}})
        )[0]
        .contains(
            "link boss is new and required, and an instance the live version accepts holds none"
        ));
        publish(
            &engine,
            &tasks(
                json!({"owner": {"schema": "Person"}, "related": {"schema": "Task", "pinned": true}}),
            ),
        );
        publish(
            &engine,
            &tasks(json!({"owner": {"schema": "Person"}, "related": {"schema": "Task"}})),
        );
        assert_eq!(
            change(json!({"related": {"schema": "Task"}})),
            ["behavior Links on type Task cannot change its config from {\"links\":{\"owner\":{\"schema\":\"Person\"},\"related\":{\"schema\":\"Task\"}}} to {\"links\":{\"related\":{\"schema\":\"Task\"}}}: link owner is gone, and its instances may hold it"]
        );
        assert!(
            change(json!({"owner": {"schema": "Task"}, "related": {"schema": "Task"}}))[0]
                .contains("link owner points at Person, not Task, in the instances that hold it")
        );
        let removed = refusal(define(&engine, &schema("Task", json!([]))));
        assert!(
            removed.message().contains("behavior Links cannot be removed from type Task, which has instances: the links and references its instances hold would stay behind"),
            "{removed}"
        );
    }
}

#[test]
fn with_no_instance_the_config_may_change_in_any_way_and_the_first_instance_holds_it_again() {
    for target in targets("links_config_free") {
        let engine = open(&target, |_| {});
        publish(&engine, &schema("Person", json!([])));
        publish(
            &engine,
            &tasks(json!({"owner": {"schema": "Person"}, "related": {"schema": "Task"}})),
        );
        // A link made required, a new required one, a link gone and a link
        // pointed at another schema: no instance holds a link or lacks one.
        publish(
            &engine,
            &tasks(
                json!({"owner": {"schema": "Person", "required": true}, "boss": {"schema": "Person", "required": true}}),
            ),
        );
        assert_eq!(
            publish(
                &engine,
                &tasks(
                    json!({"owner": {"schema": "Task"}, "boss": {"schema": "Person", "required": true}})
                )
            ),
            3
        );
        create(&engine, &alice(), "Person", Some("p1"), None).expect("p1");
        create(
            &engine,
            &alice(),
            "Task",
            Some("t1"),
            Some(json!({"Links": {"boss": "p1"}})),
        )
        .expect("t1");
        let change = |links: Value| changes(&refusal(define(&engine, &tasks(links))));
        assert!(change(
            json!({"owner": {"schema": "Task", "required": true}, "boss": {"schema": "Person", "required": true}})
        )[0]
        .contains("link owner becomes required"));
        assert!(
            change(json!({"boss": {"schema": "Person", "required": true}}))[0]
                .contains("link owner is gone, and its instances may hold it")
        );
        // Deleting the last instance frees the config again.
        assert!(delete(&engine, &alice(), "Task", "t1").expect("delete"));
        publish(
            &engine,
            &tasks(json!({"boss": {"schema": "Person", "required": true}})),
        );
    }
}

#[test]
fn a_create_gives_links_by_name_and_they_hold_from_its_event() {
    for target in targets("links_create") {
        let engine = world(&target, |_| {});
        update(&engine, "Spec", "s1", json!({"title": "s1, revised"}));
        let created = create(
            &engine,
            &alice(),
            "Task",
            Some("t4"),
            Some(
                json!({"Links": {"owner": "p1", "spec": {"id": "s1", "revision": 1}, "related": {"id": "t1"}}}),
            ),
        )
        .expect("create");
        let links = json!({
            "owner": {"schema": "Person", "id": "p1"},
            "related": {"schema": "Task", "id": "t1"},
            "spec": {"schema": "Spec", "id": "s1", "revision": 1, "latest": 2, "stale": true}
        });
        assert_eq!(
            json!([created.data, created.behaviors]),
            json!([{"title": "t4"}, {"Links": {"targets": links}}])
        );
        // One event: the create, which carries the links.
        let made: Vec<Value> = events(&engine, "Task", "t4")
            .iter()
            .map(|event| json!([event.kind, event.change]))
            .collect();
        assert_eq!(
            made,
            [
                json!(["create", {"data": {"title": "t4"}, "behaviors": {"Links": {"targets": links}}}])
            ]
        );
        // A pinned link gets the target's latest revision when the create
        // names none.
        create(
            &engine,
            &alice(),
            "Task",
            Some("t5"),
            Some(json!({"Links": {"owner": "p2", "spec": "s1"}})),
        )
        .expect("create");
        assert_eq!(
            links_of(&engine, "t5").expect("links")["spec"],
            json!({"schema": "Spec", "id": "s1", "revision": 2, "latest": 2, "stale": false})
        );
        // They are references as link's are: the required owner holds p1,
        // and t1's delete unlinks related on t4.
        assert_eq!(veto(delete(&engine, &alice(), "Person", "p1")).0, "Links");
        link(&engine, "t1", json!({"name": "owner", "id": "p2"})).expect("move");
        assert!(delete(&engine, &alice(), "Task", "t1").expect("delete"));
        assert_eq!(links_of(&engine, "t4").expect("links").get("related"), None);
        assert_eq!(
            events(&engine, "Task", "t4")
                .last()
                .expect("an event")
                .change,
            json!({
                "behavior": "Links",
                "operation": "unlink",
                "params": {"name": "related"},
                "patch": {"behaviors": {"Links": {"targets": {"related": null}}}}
            })
        );
        assert_eq!(veto(delete(&engine, &alice(), "Person", "p1")).0, "Links");
    }
}

#[test]
fn a_required_link_is_one_every_create_gives_and_a_creates_links_meet_links_checks() {
    for target in targets("links_create_checks") {
        let engine = world(&target, |_| {});
        let issues = |behaviors: Option<Value>| {
            issues_of(&refusal(create(
                &engine,
                &alice(),
                "Task",
                Some("t9"),
                behaviors,
            )))
        };
        let required = [issue(
            "/behaviors/Links",
            "link owner is required, so a create of Task gives it",
        )];
        assert_eq!(issues(None), required);
        assert_eq!(issues(Some(json!({"Links": {}}))), required);
        assert_eq!(
            issues(Some(json!({"Links": {"owner": "p1", "boss": "p2"}}))),
            [issue(
                "/behaviors/Links/boss",
                "Task has no link boss (its links: owner, related, spec)"
            )]
        );
        assert_eq!(
            issues(Some(json!({"Links": {"owner": "p9"}}))),
            [issue("/behaviors/Links/owner", "Person p9 does not exist")]
        );
        assert_eq!(
            issues(Some(json!({"Links": {"owner": {"id": "p9"}}}))),
            [issue(
                "/behaviors/Links/owner/id",
                "Person p9 does not exist"
            )]
        );
        assert_eq!(
            issues(Some(
                json!({"Links": {"owner": {"id": "p1", "revision": 1}}})
            )),
            [issue(
                "/behaviors/Links/owner/revision",
                "link owner is not pinned, so it records no revision"
            )]
        );
        assert_eq!(
            issues(Some(
                json!({"Links": {"owner": "p1", "spec": {"id": "s1", "revision": 2}}})
            )),
            [issue(
                "/behaviors/Links/spec/revision",
                "Spec s1 has revisions 1 to 1, not 2"
            )]
        );
        // Its createParamsSchema: a name is camelCase, a target an id or
        // { id, revision? }, or a list of them.
        assert_eq!(
            issues(Some(json!({"Links": {"owner": 7}}))),
            [issue(
                "/behaviors/Links/owner",
                "must be string,object,array"
            )]
        );
        let paths: Vec<String> = issues(Some(json!({"Links": {"owner": {"id": "p1", "pin": 1}}})))
            .into_iter()
            .map(|(path, _)| path)
            .collect();
        assert_eq!(paths, ["/behaviors/Links/owner"]);
        assert!(
            issues(Some(json!({"Links": {"Owner": "p1"}})))
                .iter()
                .any(|(path, message)| path == "/behaviors/Links"
                    && message.contains("property name"))
        );
        // A refused create leaves nothing: no instance, no link, no event.
        assert!(get(&engine, "Task", "t9").is_none());
        assert!(events(&engine, "Task", "t9").is_empty());
        assert!(delete(&engine, &alice(), "Spec", "s2").expect("delete"));
    }
}

#[test]
fn a_creates_pinned_link_to_a_target_with_no_revision_is_vetoed_at_its_entry() {
    for target in targets("links_create_pinned") {
        let engine = open(&target, policy(|request| request.schema() == Some("Task")));
        publish(&engine, &schema("Spec", json!([])));
        publish(
            &engine,
            &schema(
                "Task",
                json!([{"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}}}}]),
            ),
        );
        create(&engine, &alice(), "Spec", Some("s1"), None).expect("s1");
        // Revisions added to a schema with instances: s1 has none until it
        // changes.
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        let (behavior, action, reason, code, details) = veto(create(
            &engine,
            &alice(),
            "Task",
            None,
            Some(json!({"Links": {"spec": "s1"}})),
        ));
        // A create's link is vetoed with link's declared code, and its
        // entry.
        assert_eq!(
            json!([behavior, action, reason, code, details]),
            json!([
                "Links",
                "create",
                "Spec s1 has no revision to pin yet",
                "no_revision",
                {"path": "/behaviors/Links/spec"}
            ])
        );
        update(&engine, "Spec", "s1", json!({"title": "s1, revised"}));
        assert_eq!(
            refusal(create(
                &engine,
                &bob(),
                "Task",
                None,
                Some(json!({"Links": {"spec": "s1"}}))
            ))
            .code(),
            EngineErrorCode::Forbidden
        );
        assert!(engine
            .instances()
            .list(&alice(), "Task", Default::default())
            .expect("list")
            .items
            .is_empty());
        assert_eq!(
            Value::Object(
                create(&engine, &bob(), "Task", Some("t1"), None)
                    .expect("create")
                    .data
            ),
            json!({"title": "t1"})
        );
    }
}

#[test]
fn a_required_link_may_become_optional_and_then_be_unlinked() {
    for target in targets("links_optional") {
        let engine = world(&target, |_| {});
        publish(
            &engine,
            &tasks(task_links_with(json!({"owner": {"schema": "Person"}}))),
        );
        unlink(&engine, "t1", json!({"name": "owner"})).expect("unlink");
        assert_eq!(links_of(&engine, "t1"), None);
        assert_eq!(
            Value::Object(get(&engine, "Task", "t1").expect("t1").behaviors),
            json!({"Links": {}}),
            "with no link, Links has an entry and no targets"
        );
        assert_eq!(
            Value::Object(
                create(&engine, &alice(), "Task", Some("t4"), None)
                    .expect("create")
                    .data
            ),
            json!({"title": "t4"})
        );
    }
}

#[test]
fn what_a_link_pins_may_change_and_a_row_pinned_to_the_other_kind_records_no_pin_of_the_new() {
    for target in targets("links_pin_change") {
        let engine = world(&target, |_| {});
        let spec_of = || links_of(&engine, "t1").expect("links")["spec"].clone();
        link(&engine, "t1", json!({"name": "spec", "id": "s1"})).expect("link");
        publish(
            &engine,
            &tasks(task_links_with(
                json!({"spec": {"schema": "Spec", "pinned": "revision"}}),
            )),
        );
        assert_eq!(
            spec_of(),
            json!({"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false})
        );
        publish(
            &engine,
            &tasks(task_links_with(json!({"spec": {"schema": "Spec"}}))),
        );
        assert_eq!(spec_of(), json!({"schema": "Spec", "id": "s1"}));
        publish(
            &engine,
            &tasks(task_links_with(
                json!({"spec": {"schema": "Spec", "pinned": "release"}}),
            )),
        );
        assert_eq!(spec_of(), json!({"schema": "Spec", "id": "s1"}));
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1"})
            ))),
            [issue(
                "/name",
                "link spec is pinned to a release, but Spec does not compose Branches"
            )]
        );
        // A release for a link pinned to a revision is refused at its
        // parameter, as a revision for one pinned to a release is.
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1", "revision": 1})
            ))),
            [issue(
                "/revision",
                "link spec is pinned to a release, so it records no revision"
            )]
        );
        // Back to a revision pin, the row's revision reads again.
        publish(&engine, &tasks(task_links()));
        assert_eq!(
            spec_of(),
            json!({"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false})
        );
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t1",
                json!({"name": "spec", "id": "s1", "release": 1})
            ))),
            [issue(
                "/release",
                "link spec is pinned to a revision, so it records no release"
            )]
        );
        let refused = refusal(define(
            &engine,
            &tasks(task_links_with(
                json!({"spec": {"schema": "Spec", "pinned": "tag"}}),
            )),
        ));
        assert!(refused
            .issues()
            .iter()
            .any(|issue| issue.path == "/types/Task/behaviors/0/config/links/spec/pinned"));
    }
}

// A Recipe composes Branches, with a step kind; a Cook's recipe link pins
// its release.
fn recipe_schema() -> Value {
    let mut document = schema_document(
        "Recipe",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({"types": {"Step": {"name": "Step", "role": "EmbeddedStruct", "fields": [
            {"name": "instruction", "typeRef": {"name": "string"}, "required": true},
            {"name": "position", "typeRef": {"name": "Int"}, "required": true}
        ]}}}),
    );
    document["types"]["Recipe"]["behaviors"] = json!([{"name": "Branches", "config": {"kinds": {"step": {"type": "Step", "order": "position"}}}}]);
    document
}

fn cooks(links: Value) -> Value {
    schema(
        "Cook",
        json!([{"name": "Links", "config": {"links": links}}]),
    )
}

fn pinned_cooks() -> Value {
    cooks(json!({"recipe": {"schema": "Recipe", "pinned": "release"}}))
}

// kitchen opens an engine with Branches beside Links, and the meta-schema
// that lets a test behavior through, publishes Recipe and `cook`, the Cook
// schema, and creates soup, c1 and c2.
fn kitchen(target: &Target, more: BehaviorSet, cook: Value) -> Engine {
    let engine = target.open_with(behaviors().with(Branches).extend(more), |_| {});
    publish(&engine, &recipe_schema());
    publish(&engine, &cook);
    create(&engine, &alice(), "Recipe", Some("soup"), None).expect("soup");
    for id in ["c1", "c2"] {
        create(&engine, &alice(), "Cook", Some(id), None).expect("cook");
    }
    engine
}

// release writes a step on a draft of soup's primary line, merges it in
// tagged and releases the merge, at the release field's version; it
// returns the commit released.
fn release(engine: &Engine) -> Value {
    let soup = |operation: &str, params: Value| {
        invoke(engine, &alice(), "Recipe", "soup", operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    };
    let main = soup("refs", json!({}))["items"][0].clone();
    let n = soup("releases", json!({}))["items"]
        .as_array()
        .map_or(0, Vec::len)
        + 1;
    let draft = soup(
        "branch",
        json!({"fromRef": main["id"], "name": format!("change{n}")}),
    );
    let saved = soup(
        "save",
        json!({"ref": draft["id"], "version": draft["version"], "edits": {"step": {"upsert": [{"instruction": format!("Step {n}"), "position": n}]}}}),
    );
    let committed = soup(
        "commit",
        json!({"ref": saved["ref"]["id"], "version": saved["ref"]["version"]}),
    );
    let merged = soup(
        "merge",
        json!({"source": committed["ref"]["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
    );
    let released = get(engine, "Recipe", "soup")
        .expect("soup")
        .behavior_field("Branches", "release")
        .and_then(Value::as_i64)
        .unwrap_or(0);
    soup(
        "releaseCommit",
        json!({"commit": merged["commit"]["id"], "version": released}),
    );
    merged["commit"].clone()
}

fn cook(engine: &Engine, id: &str, params: Value) -> Result<Value> {
    invoke(engine, &alice(), "Cook", id, "link", params)
}

fn recipe_of(engine: &Engine, id: &str) -> Value {
    get(engine, "Cook", id)
        .expect("the cook")
        .behavior_field("Links", "targets")
        .map_or(Value::Null, |targets| targets["recipe"].clone())
}

#[test]
fn a_link_pinned_to_a_release_records_the_targets_release_and_is_stale_once_it_is_released_again() {
    for target in targets("links_release") {
        let engine = kitchen(&target, BehaviorSet::new(), pinned_cooks());
        let (behavior, action, reason, code, _) =
            veto(cook(&engine, "c1", json!({"name": "recipe", "id": "soup"})));
        assert_eq!(
            json!([behavior, action, reason, code]),
            json!([
                "Links",
                "link",
                "Recipe soup has no release to pin yet",
                "no_release"
            ])
        );
        release(&engine);
        assert_eq!(
            get(&engine, "Recipe", "soup")
                .expect("soup")
                .behavior_field("Branches", "release"),
            Some(&json!(1))
        );
        assert_eq!(
            cook(&engine, "c1", json!({"name": "recipe", "id": "soup"})).expect("link"),
            json!({"name": "recipe", "schema": "Recipe", "id": "soup", "release": 1})
        );
        assert_eq!(
            recipe_of(&engine, "c1"),
            json!({"schema": "Recipe", "id": "soup", "release": 1, "latest": 1, "stale": false})
        );
        release(&engine);
        assert_eq!(
            recipe_of(&engine, "c1"),
            json!({"schema": "Recipe", "id": "soup", "release": 1, "latest": 2, "stale": true})
        );
        assert_eq!(
            get(&engine, "Cook", "c1").expect("c1").seq,
            2,
            "read when read: no event on the cook"
        );
        assert_eq!(
            cook(
                &engine,
                "c2",
                json!({"name": "recipe", "id": "soup", "release": 1})
            )
            .expect("link"),
            json!({"name": "recipe", "schema": "Recipe", "id": "soup", "release": 1})
        );
        cook(&engine, "c1", json!({"name": "recipe", "id": "soup"})).expect("link");
        assert_eq!(
            recipe_of(&engine, "c1"),
            json!({"schema": "Recipe", "id": "soup", "release": 2, "latest": 2, "stale": false})
        );
        assert_eq!(
            issues_of(&refusal(cook(
                &engine,
                "c1",
                json!({"name": "recipe", "id": "soup", "release": 3})
            ))),
            [issue("/release", "Recipe soup has releases 1 to 2, not 3")]
        );
        assert_eq!(
            issues_of(&refusal(cook(
                &engine,
                "c1",
                json!({"name": "recipe", "id": "soup", "revision": 1})
            ))),
            [issue(
                "/revision",
                "link recipe is pinned to a release, so it records no revision"
            )]
        );
        // listLinked gives each pin and the latest, and with stale only the
        // ones the target has been released past.
        assert_eq!(
            listed(&engine, "Cook", json!({"name": "recipe", "id": "soup"})).expect("listLinked"),
            json!({"items": [
                {"id": "c1", "release": 2, "latest": 2, "stale": false},
                {"id": "c2", "release": 1, "latest": 2, "stale": true}
            ], "next": null})
        );
        assert_eq!(
            listed(
                &engine,
                "Cook",
                json!({"name": "recipe", "id": "soup", "stale": true})
            )
            .expect("listLinked"),
            json!({"items": [{"id": "c2", "release": 1, "latest": 2, "stale": true}], "next": null})
        );
        // unlink returns the pin.
        assert_eq!(
            invoke(
                &engine,
                &alice(),
                "Cook",
                "c2",
                "unlink",
                json!({"name": "recipe"})
            )
            .expect("unlink"),
            json!({"name": "recipe", "schema": "Recipe", "id": "soup", "release": 1})
        );
    }
}

#[test]
fn a_release_pin_needs_a_target_schema_that_composes_branches_and_a_create_gives_it_as_id_and_release(
) {
    for target in targets("links_release_create") {
        let engine = kitchen(&target, BehaviorSet::new(), pinned_cooks());
        publish(&engine, &schema("Note", json!([])));
        publish(
            &engine,
            &cooks(json!({
                "recipe": {"schema": "Recipe", "pinned": "release"},
                "note": {"schema": "Note", "pinned": "release"}
            })),
        );
        create(&engine, &alice(), "Note", Some("n1"), None).expect("n1");
        assert_eq!(
            issues_of(&refusal(cook(
                &engine,
                "c1",
                json!({"name": "note", "id": "n1"})
            ))),
            [issue(
                "/name",
                "link note is pinned to a release, but Note does not compose Branches"
            )]
        );
        let (_, action, _, code, details) = veto(create(
            &engine,
            &alice(),
            "Cook",
            None,
            Some(json!({"Links": {"recipe": "soup"}})),
        ));
        assert_eq!(
            json!([action, code, details]),
            json!(["create", "no_release", {"path": "/behaviors/Links/recipe"}])
        );
        release(&engine);
        release(&engine);
        let created = create(
            &engine,
            &alice(),
            "Cook",
            Some("c3"),
            Some(json!({"Links": {"recipe": {"id": "soup", "release": 1}}})),
        )
        .expect("c3");
        assert_eq!(
            created.behavior_field("Links", "targets"),
            Some(
                &json!({"recipe": {"schema": "Recipe", "id": "soup", "release": 1, "latest": 2, "stale": true}})
            )
        );
        let refused = refusal(create(
            &engine,
            &alice(),
            "Cook",
            None,
            Some(json!({"Links": {"recipe": {"id": "soup", "release": 3}}})),
        ));
        assert_eq!(
            issues_of(&refused),
            [issue(
                "/behaviors/Links/recipe/release",
                "Recipe soup has releases 1 to 2, not 3"
            )]
        );
    }
}

#[test]
fn a_release_of_the_target_moves_on_the_instances_pinned_to_an_earlier_one_and_its_other_operations_move_none(
) {
    for target in targets("links_release_moved") {
        let engine = kitchen(
            &target,
            BehaviorSet::new().with(Mover),
            schema(
                "Cook",
                json!([
                    {"name": "Links", "config": {"links": {"recipe": {"schema": "Recipe", "pinned": "release"}, "about": {"schema": "Recipe"}}}},
                    {"name": "test.Mover"}
                ]),
            ),
        );
        let first = release(&engine);
        for id in ["c1", "c2"] {
            cook(&engine, id, json!({"name": "recipe", "id": "soup"})).expect("link");
            cook(&engine, id, json!({"name": "about", "id": "soup"})).expect("link");
        }
        let second = release(&engine);
        // c2 pins the new release since; c1 still pins the first.
        cook(&engine, "c2", json!({"name": "recipe", "id": "soup"})).expect("link");
        let soup_events = events(&engine, "Recipe", "soup");
        let ask = |name: &str, event: &EngineEvent| {
            engine
                .instances()
                .invoke_schema(
                    &alice(),
                    "Cook",
                    "moved",
                    &json!({"link": name, "event": serde_json::to_value(event).expect("JSON")}),
                    Default::default(),
                )
                .expect("moved")
        };
        let released = soup_events.last().expect("the release");
        assert_eq!(released.change["operation"], "releaseCommit");
        // A link that pins releases moves on the instances the target has
        // been released past; one that pins nothing, every one linked to it.
        assert_eq!(
            ask("recipe", released),
            json!({"move": {"release": 2, "commit": second["id"]}, "instances": ["c1"]})
        );
        assert_eq!(
            ask("about", released),
            json!({"move": {"release": 2, "commit": second["id"]}, "instances": ["c1", "c2"]})
        );
        // The first release moved on nothing pinned before it.
        let first_release = soup_events
            .iter()
            .find(|event| {
                event.change["operation"] == "releaseCommit"
                    && event.change["params"]["commit"] == first["id"]
            })
            .expect("the first release");
        assert_eq!(
            ask("recipe", first_release)["move"],
            json!({"release": 1, "commit": first["id"]})
        );
        // Branches' other operations are no move.
        for event in &soup_events {
            let operation = event.change["operation"].as_str().unwrap_or_default();
            if operation != "releaseCommit" {
                assert_eq!(ask("recipe", event), Value::Null, "{operation}");
            }
        }
    }
}

// test.Mover (this file's): asks `revised` what an event of a link's
// target moved on, from a schema whose link it names.
struct Mover;

const MOVER: &str = r#"{
    "name": "test.Mover",
    "operations": [
        {"name": "moved", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["link", "event"], "properties": {"link": {"type": "string"}, "event": {"type": "object"}}}, "resultSchema": true}
    ]
}"#;

// event_of reads the event a test passes back from its JSON.
fn event_of(value: &Value) -> EngineEvent {
    EngineEvent {
        cursor: value["cursor"].as_i64().unwrap_or_default(),
        kind: value["kind"].as_str().unwrap_or_default().to_owned(),
        namespace: value["namespace"].as_str().unwrap_or_default().to_owned(),
        schema: value["schema"].as_str().unwrap_or_default().to_owned(),
        instance_id: value["instanceId"].as_str().map(str::to_owned),
        seq: value["seq"].as_i64(),
        beside: value["beside"] == true,
        version: value["version"].as_i64(),
        actor: value["actor"].as_str().unwrap_or_default().to_owned(),
        service: None,
        for_subject: None,
        at: value["at"].as_i64().unwrap_or_default(),
        change: value["change"].clone(),
        cause: None,
        value_refs: None,
    }
}

fn moved(scope: &Scope<'_, Value>, params: &Value) -> Result<Value> {
    let name = params["link"].as_str().unwrap_or_default();
    let links = scope
        .schemas()
        .config(scope.schema(), "Links")?
        .unwrap_or(Value::Null);
    let link = &links["links"][name];
    let schema = link["schema"].as_str().unwrap_or_default();
    let event = event_of(&params["event"]);
    let Some(moved) = target_move(scope, "test.Mover", "moved", name, schema, &event)? else {
        return Ok(Value::Null);
    };
    let what = match &moved {
        TargetMove::Revision { revision } => json!({"revision": revision}),
        TargetMove::Release { release, commit } => json!({"release": release, "commit": commit}),
    };
    let id = event.instance_id.as_deref().unwrap_or_default();
    Ok(json!({
        "move": what,
        "instances": moved_on(scope, name, link_pin(link), id, &moved)?
    }))
}

impl Behavior for Mover {
    type Config = Value;

    fn declaration(&self) -> &str {
        MOVER
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            schema_operations: vec![("moved", |_, cx, params| moved(cx, params))],
            ..Hooks::default()
        }
    }
}

#[test]
fn a_move_of_a_links_target_moves_on_the_instances_linked_to_it_the_stale_ones_where_it_pins() {
    for target in targets("links_revised") {
        let engine = target.open_with(behaviors().with(Mover), |_| {});
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        publish(&engine, &schema("Person", json!([])));
        publish(
            &engine,
            &schema(
                "Task",
                json!([
                    {"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}, "about": {"schema": "Spec"}, "owner": {"schema": "Person"}}}},
                    {"name": "test.Mover"}
                ]),
            ),
        );
        create(&engine, &alice(), "Spec", Some("s1"), None).expect("s1");
        create(&engine, &alice(), "Person", Some("p1"), None).expect("p1");
        for id in ["t1", "t2"] {
            create(
                &engine,
                &alice(),
                "Task",
                Some(id),
                Some(json!({"Links": {"spec": "s1", "about": "s1"}})),
            )
            .expect("task");
        }
        update(&engine, "Spec", "s1", json!({"title": "s1, revised"}));
        // t2 pins the new revision since; t1 still pins the first.
        link(&engine, "t2", json!({"name": "spec", "id": "s1"})).expect("link");
        let last = |schema: &str, id: &str| {
            serde_json::to_value(events(&engine, schema, id).last().expect("an event"))
                .expect("JSON")
        };
        let ask = |name: &str, event: Value| {
            engine
                .instances()
                .invoke_schema(
                    &alice(),
                    "Task",
                    "moved",
                    &json!({"link": name, "event": event}),
                    Default::default(),
                )
                .expect("moved")
        };
        let revised = last("Spec", "s1");
        // A link that pins revisions moves on the instances the target has
        // moved past; one that pins nothing, every one linked to it.
        assert_eq!(
            ask("spec", revised.clone()),
            json!({"move": {"revision": 2}, "instances": ["t1"]})
        );
        assert_eq!(
            ask("about", revised),
            json!({"move": {"revision": 2}, "instances": ["t1", "t2"]})
        );
        // A create is no move, nor an event of another schema.
        let created = serde_json::to_value(&events(&engine, "Spec", "s1")[0]).expect("JSON");
        assert_eq!(ask("spec", created), Value::Null);
        assert_eq!(ask("spec", last("Task", "t2")), Value::Null);
        // A link to a schema that composes neither Revisions nor Branches
        // is a defect of the config that names it.
        update(&engine, "Person", "p1", json!({"title": "p1, renamed"}));
        let error = engine
            .instances()
            .invoke_schema(
                &alice(),
                "Task",
                "moved",
                &json!({"link": "owner", "event": last("Person", "p1")}),
                Default::default(),
            )
            .expect_err("a defect");
        match error {
            Error::Behavior(defect) => assert_eq!(
                defect.to_string(),
                "behavior test.Mover: moved names link owner to Person, which composes neither Revisions nor Branches, so it gains no revision or release"
            ),
            other => panic!("expected a BehaviorError, got {other:?}"),
        }
    }
}

#[test]
fn a_link_is_also_a_reference_that_hears_the_targets_delete_alone() {
    for target in targets("links_references") {
        let engine = world(&target, |_| {});
        link(&engine, "t1", json!({"name": "related", "id": "t2"})).expect("link");
        // A change of the target asks the linking instance nothing: no
        // event on t1.
        let before = get(&engine, "Task", "t1").expect("t1").seq;
        update(&engine, "Task", "t2", json!({"title": "t2, renamed"}));
        assert_eq!(get(&engine, "Task", "t1").expect("t1").seq, before);
        // Deleting the linking instance deletes its links: listLinked finds
        // none.
        assert!(delete(&engine, &alice(), "Task", "t1").expect("delete"));
        assert_eq!(
            listed(&engine, "Task", json!({"name": "related", "id": "t2"})).expect("listLinked"),
            json!({"items": [], "next": null})
        );
        assert_eq!(
            listed(&engine, "Task", json!({"name": "owner", "id": "p1"})).expect("listLinked"),
            json!({"items": [], "next": null})
        );
    }
}
