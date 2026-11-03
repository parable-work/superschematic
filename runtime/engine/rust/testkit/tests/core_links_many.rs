//! Links' many-valued links (D56): the cases of
//! `test/core-links-many.test.ts` (a link with many and a max holds a list
//! of targets, in the order they were linked; link adds a target or pins
//! again one it holds, unlink removes the one it names, a create gives a
//! list, each target once, and a deleted target leaves the lists that hold
//! it; a required one keeps at least one target; the table Links' second
//! migration left is built again, its rows kept), on SQLite and, with the
//! variable set, on Postgres. Its guidance and create parameters, which the
//! TypeScript file reads through the describe document (milestone 6), are
//! read through the behavior's hooks. What reads one target of a link,
//! Reactions' rules among them, refuses a many-valued link, at define and
//! on the runner; Rollups and Reactions' revised read across the list.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{DescribeTarget, DescribedTypeOperation, OperationScope};
use superschematic_engine::storage::{Dialect, Lock, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, CreateOptions, DefineOptions, Engine, EngineError, EngineEvent,
    EngineOptions, InstanceRecord, Principal, ReadEvents, Result, RunnerOptions, SchemaTarget,
    SubscriptionState, WriteOptions,
};
use superschematic_engine_behaviors::{
    LinkPin, LinkSpec, Links, LinksConfig, Reactions, Revisions, Rollups, Workflow,
};
use support::{alice, issue, issues_of, refusal, schema_document, targets, veto, Target};

fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
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
        "owner": {"schema": "Person"},
        "reviewers": {"schema": "Person", "many": true, "max": 3},
        "specs": {"schema": "Spec", "many": true, "max": 2, "pinned": true}
    })
}

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

// A review's approvers are required: it holds at least one.
fn review_links() -> Value {
    json!({"approvers": {"schema": "Person", "many": true, "max": 2, "required": true}})
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create(
    engine: &Engine,
    schema: &str,
    id: Option<&str>,
    behaviors: Option<Value>,
) -> Result<InstanceRecord> {
    engine.instances().create(
        &alice(),
        schema,
        &json!({"title": id.unwrap_or("untitled")}),
        CreateOptions {
            id,
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

// People p1 to p4; specs s1 and s2, which keep revisions; tasks t1 to t3,
// with no link; reviews are published, with none.
fn world(target: &Target) -> Engine {
    let engine = open(target, |_| {});
    publish(&engine, &schema("Person", json!([])));
    publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
    publish(&engine, &tasks(task_links()));
    publish(
        &engine,
        &schema(
            "Review",
            json!([{"name": "Links", "config": {"links": review_links()}}]),
        ),
    );
    for id in ["p1", "p2", "p3", "p4"] {
        create(&engine, "Person", Some(id), None).expect("person");
    }
    for id in ["s1", "s2"] {
        create(&engine, "Spec", Some(id), None).expect("spec");
    }
    for id in ["t1", "t2", "t3"] {
        create(&engine, "Task", Some(id), None).expect("task");
    }
    engine
}

fn invoke(
    engine: &Engine,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn link(engine: &Engine, id: &str, params: Value) -> Result<Value> {
    invoke(engine, "Task", id, "link", params)
}

fn unlink(engine: &Engine, id: &str, params: Value) -> Result<Value> {
    invoke(engine, "Task", id, "unlink", params)
}

fn links_of(engine: &Engine, schema: &str, id: &str) -> Option<Value> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance")
        .behavior_field("Links", "targets")
        .cloned()
}

fn person(id: &str) -> Value {
    json!({"schema": "Person", "id": id})
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

fn last_change(engine: &Engine, schema: &str, id: &str) -> Value {
    events(engine, schema, id)
        .last()
        .expect("an event")
        .change
        .clone()
}

fn listed(engine: &Engine, params: Value) -> Value {
    engine
        .instances()
        .invoke_schema(&alice(), "Task", "listLinked", &params, None)
        .expect("listLinked")
}

fn delete(engine: &Engine, schema: &str, id: &str) -> Result<bool> {
    engine
        .instances()
        .delete(&alice(), schema, id, WriteOptions::default())
}

fn changes(error: &EngineError) -> Vec<String> {
    error
        .changes()
        .iter()
        .map(|change| change.message.clone())
        .collect()
}

#[test]
fn a_many_valued_link_holds_a_list_in_link_order_and_link_adds_or_pins_again_up_to_max() {
    for target in targets("links_many_list") {
        let engine = world(&target);
        assert_eq!(
            link(&engine, "t1", json!({"name": "reviewers", "id": "p2"})).expect("link"),
            json!({"name": "reviewers", "schema": "Person", "id": "p2"})
        );
        link(&engine, "t1", json!({"name": "reviewers", "id": "p1"})).expect("link");
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link");
        assert_eq!(
            links_of(&engine, "Task", "t1"),
            Some(json!({"owner": person("p1"), "reviewers": [person("p2"), person("p1")]}))
        );
        // The event carries the whole list, as a merge patch replaces one.
        link(&engine, "t1", json!({"name": "reviewers", "id": "p3"})).expect("link");
        assert_eq!(
            last_change(&engine, "Task", "t1"),
            json!({
                "behavior": "Links",
                "operation": "link",
                "params": {"name": "reviewers", "id": "p3"},
                "patch": {"behaviors": {"Links": {"targets": {"reviewers": [person("p2"), person("p1"), person("p3")]}}}}
            })
        );
        // A target it holds keeps its place; a new one past max is refused.
        link(&engine, "t1", json!({"name": "reviewers", "id": "p2"})).expect("link");
        let (behavior, action, reason, code, _) = veto(link(
            &engine,
            "t1",
            json!({"name": "reviewers", "id": "p4"}),
        ));
        assert_eq!(
            json!([behavior, action, reason, code]),
            json!([
                "Links",
                "link",
                "link reviewers holds 3 targets already, its max",
                "too_many"
            ])
        );
        assert_eq!(
            links_of(&engine, "Task", "t1").expect("links")["reviewers"],
            json!([person("p2"), person("p1"), person("p3")])
        );
        // link's other checks hold for a list as for one target.
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t2",
                json!({"name": "reviewers", "id": "p9"})
            ))),
            [issue("/id", "Person p9 does not exist")]
        );
        assert_eq!(
            issues_of(&refusal(link(
                &engine,
                "t2",
                json!({"name": "reviewers", "id": "p1", "revision": 1})
            ))),
            [issue(
                "/revision",
                "link reviewers is not pinned, so it records no revision"
            )]
        );
    }
}

#[test]
fn unlink_removes_the_target_it_names_and_one_the_link_does_not_hold_is_not_linked() {
    for target in targets("links_many_unlink") {
        let engine = world(&target);
        for id in ["p1", "p2", "p3"] {
            link(&engine, "t1", json!({"name": "reviewers", "id": id})).expect("link");
        }
        assert_eq!(
            issues_of(&refusal(unlink(
                &engine,
                "t1",
                json!({"name": "reviewers"})
            ))),
            [issue(
                "/id",
                "link reviewers holds a list of targets, so unlink names the one it removes"
            )]
        );
        assert_eq!(
            unlink(&engine, "t1", json!({"name": "reviewers", "id": "p2"})).expect("unlink"),
            json!({"name": "reviewers", "schema": "Person", "id": "p2"})
        );
        assert_eq!(
            last_change(&engine, "Task", "t1"),
            json!({
                "behavior": "Links",
                "operation": "unlink",
                "params": {"name": "reviewers", "id": "p2"},
                "patch": {"behaviors": {"Links": {"targets": {"reviewers": [person("p1"), person("p3")]}}}}
            })
        );
        let (_, action, reason, code, _) = veto(unlink(
            &engine,
            "t1",
            json!({"name": "reviewers", "id": "p2"}),
        ));
        assert_eq!(
            json!([action, reason, code]),
            json!([
                "unlink",
                "link reviewers does not hold Person p2",
                "not_linked"
            ])
        );
        unlink(&engine, "t1", json!({"name": "reviewers", "id": "p1"})).expect("unlink");
        unlink(&engine, "t1", json!({"name": "reviewers", "id": "p3"})).expect("unlink");
        assert_eq!(links_of(&engine, "Task", "t1"), None);
        assert_eq!(
            last_change(&engine, "Task", "t1")["patch"],
            json!({"behaviors": {"Links": {"targets": null}}})
        );
        assert_eq!(
            veto(unlink(
                &engine,
                "t1",
                json!({"name": "reviewers", "id": "p1"})
            ))
            .3,
            Some("not_linked".to_owned())
        );
        // A link that holds one target is cleared only when it holds the
        // one named.
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("link");
        assert_eq!(
            veto(unlink(&engine, "t1", json!({"name": "owner", "id": "p2"}))).2,
            "link owner does not hold Person p2"
        );
        assert_eq!(
            unlink(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("unlink"),
            json!({"name": "owner", "schema": "Person", "id": "p1"})
        );
    }
}

#[test]
fn a_pinned_many_valued_link_records_each_targets_revision_and_pinning_again_keeps_its_place() {
    for target in targets("links_many_pinned") {
        let engine = world(&target);
        assert_eq!(
            link(&engine, "t1", json!({"name": "specs", "id": "s1"})).expect("link"),
            json!({"name": "specs", "schema": "Spec", "id": "s1", "revision": 1})
        );
        link(&engine, "t1", json!({"name": "specs", "id": "s2"})).expect("link");
        let specs_of = || links_of(&engine, "Task", "t1").expect("links")["specs"].clone();
        engine
            .instances()
            .update(
                &alice(),
                "Spec",
                "s1",
                &json!({"title": "s1, revised"}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            specs_of(),
            json!([
                {"schema": "Spec", "id": "s1", "revision": 1, "latest": 2, "stale": true},
                {"schema": "Spec", "id": "s2", "revision": 1, "latest": 1, "stale": false}
            ])
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Task", "t1", Default::default())
                .expect("get")
                .expect("t1")
                .seq,
            3,
            "read when read: no event on the task"
        );
        link(&engine, "t1", json!({"name": "specs", "id": "s1"})).expect("link");
        assert_eq!(
            specs_of(),
            json!([
                {"schema": "Spec", "id": "s1", "revision": 2, "latest": 2, "stale": false},
                {"schema": "Spec", "id": "s2", "revision": 1, "latest": 1, "stale": false}
            ])
        );
        link(
            &engine,
            "t1",
            json!({"name": "specs", "id": "s1", "revision": 1}),
        )
        .expect("link");
        assert_eq!(
            listed(&engine, json!({"name": "specs", "id": "s1", "stale": true})),
            json!({"items": [{"id": "t1", "revision": 1, "latest": 2, "stale": true}], "next": null})
        );
        assert_eq!(
            unlink(&engine, "t1", json!({"name": "specs", "id": "s1"})).expect("unlink"),
            json!({"name": "specs", "schema": "Spec", "id": "s1", "revision": 1})
        );
    }
}

#[test]
fn list_linked_lists_the_instances_whose_many_valued_link_holds_a_target_in_link_order() {
    for target in targets("links_many_listed") {
        let engine = world(&target);
        link(&engine, "t1", json!({"name": "reviewers", "id": "p1"})).expect("link");
        link(&engine, "t1", json!({"name": "reviewers", "id": "p2"})).expect("link");
        link(&engine, "t2", json!({"name": "reviewers", "id": "p2"})).expect("link");
        link(&engine, "t3", json!({"name": "reviewers", "id": "p1"})).expect("link");
        assert_eq!(
            listed(&engine, json!({"name": "reviewers", "id": "p2"})),
            json!({"items": [{"id": "t1"}, {"id": "t2"}], "next": null})
        );
        let first = listed(
            &engine,
            json!({"name": "reviewers", "id": "p1", "limit": 1}),
        );
        assert_eq!(first["items"], json!([{"id": "t1"}]));
        assert_eq!(
            listed(
                &engine,
                json!({"name": "reviewers", "id": "p1", "cursor": first["next"]})
            ),
            json!({"items": [{"id": "t3"}], "next": null})
        );
    }
}

#[test]
fn a_deleted_target_leaves_only_the_lists_that_hold_it_and_a_required_list_keeps_its_last() {
    for target in targets("links_many_delete") {
        let engine = world(&target);
        link(&engine, "t1", json!({"name": "reviewers", "id": "p1"})).expect("link");
        link(&engine, "t1", json!({"name": "reviewers", "id": "p2"})).expect("link");
        link(&engine, "t2", json!({"name": "reviewers", "id": "p1"})).expect("link");
        link(&engine, "t2", json!({"name": "owner", "id": "p1"})).expect("link");
        let after = engine
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
            .map_or(0, |event| event.cursor);
        assert!(delete(&engine, "Person", "p1").expect("delete"));
        let seen: Vec<Value> = engine
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
            .iter()
            .map(|event| json!([event.kind, event.instance_id, event.change.get("params")]))
            .collect();
        assert_eq!(
            seen,
            [
                json!(["delete", "p1", null]),
                json!(["operation", "t1", {"name": "reviewers", "id": "p1"}]),
                json!(["operation", "t2", {"name": "reviewers", "id": "p1"}]),
                json!(["operation", "t2", {"name": "owner"}]),
            ]
        );
        assert_eq!(
            links_of(&engine, "Task", "t1"),
            Some(json!({"reviewers": [person("p2")]}))
        );
        assert_eq!(links_of(&engine, "Task", "t2"), None);

        let review = create(
            &engine,
            "Review",
            Some("r1"),
            Some(json!({"Links": {"approvers": ["p2", "p3"]}})),
        )
        .expect("create");
        assert_eq!(
            review.behaviors["Links"]["targets"],
            json!({"approvers": [person("p2"), person("p3")]})
        );
        assert!(delete(&engine, "Person", "p3").expect("delete"));
        let refused = refusal(delete(&engine, "Person", "p2"));
        assert_eq!(
            refused.message(),
            "behavior Links vetoes delete of Person p2: an instance of Review links to it through its required link approvers, as its last target"
        );
        assert_eq!(
            veto(delete(&engine, "Person", "p2")).3,
            Some("required_target".to_owned())
        );
        let (_, _, reason, code, _) = veto(invoke(
            &engine,
            "Review",
            "r1",
            "unlink",
            json!({"name": "approvers", "id": "p2"}),
        ));
        assert_eq!(
            json!([reason, code]),
            json!([
                "link approvers is required and Person p2 is its last target: link another before unlinking it",
                "required_link"
            ])
        );
        // Another target first frees it.
        invoke(
            &engine,
            "Review",
            "r1",
            "link",
            json!({"name": "approvers", "id": "p4"}),
        )
        .expect("link");
        assert!(delete(&engine, "Person", "p2").expect("delete"));
        assert_eq!(
            links_of(&engine, "Review", "r1"),
            Some(json!({"approvers": [person("p4")]}))
        );
        assert_eq!(links_of(&engine, "Task", "t1"), None);
    }
}

#[test]
fn a_create_gives_a_many_valued_link_a_list_in_order_each_target_once_and_at_most_max() {
    for target in targets("links_many_create") {
        let engine = world(&target);
        let created = create(
            &engine,
            "Task",
            Some("t4"),
            Some(
                json!({"Links": {"owner": "p3", "reviewers": ["p2", {"id": "p1"}], "specs": [{"id": "s1", "revision": 1}]}}),
            ),
        )
        .expect("create");
        let links = json!({
            "owner": person("p3"),
            "reviewers": [person("p2"), person("p1")],
            "specs": [{"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false}]
        });
        assert_eq!(created.behaviors["Links"]["targets"], links);
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
        // An empty list of an optional link holds none.
        assert_eq!(
            create(
                &engine,
                "Task",
                Some("t5"),
                Some(json!({"Links": {"reviewers": []}}))
            )
            .expect("create")
            .behavior_field("Links", "targets"),
            None
        );

        let issues = |schema: &str, behaviors: Option<Value>| {
            issues_of(&refusal(create(&engine, schema, Some("x9"), behaviors)))
        };
        for (given, expected) in [
            (
                json!({"reviewers": "p1"}),
                issue(
                    "/behaviors/Links/reviewers",
                    "link reviewers holds a list of targets, so a create gives a list",
                ),
            ),
            (
                json!({"owner": ["p1"]}),
                issue(
                    "/behaviors/Links/owner",
                    "link owner holds one target, so a create gives one, not a list",
                ),
            ),
            (
                json!({"reviewers": ["p1", "p2", "p3", "p4"]}),
                issue(
                    "/behaviors/Links/reviewers",
                    "link reviewers holds at most 3 targets, and the create gives 4",
                ),
            ),
            (
                json!({"reviewers": ["p1", {"id": "p1"}]}),
                issue(
                    "/behaviors/Links/reviewers/1",
                    "link reviewers holds each target once, and the create gives Person p1 again",
                ),
            ),
            (
                json!({"reviewers": ["p1", "p9"]}),
                issue("/behaviors/Links/reviewers/1", "Person p9 does not exist"),
            ),
            (
                json!({"reviewers": [{"id": "p9"}]}),
                issue(
                    "/behaviors/Links/reviewers/0/id",
                    "Person p9 does not exist",
                ),
            ),
            (
                json!({"specs": ["s2", {"id": "s1", "revision": 2}]}),
                issue(
                    "/behaviors/Links/specs/1/revision",
                    "Spec s1 has revisions 1 to 1, not 2",
                ),
            ),
            (
                json!({"reviewers": [7]}),
                issue("/behaviors/Links/reviewers/0", "must be string,object"),
            ),
        ] {
            assert_eq!(
                issues("Task", Some(json!({ "Links": given }))),
                [expected],
                "{given}"
            );
        }
        assert_eq!(
            issues("Review", Some(json!({"Links": {"approvers": []}}))),
            [issue(
                "/behaviors/Links/approvers",
                "link approvers is required, so a create of Review gives at least one target"
            )]
        );
        assert_eq!(
            issues("Review", None),
            [issue(
                "/behaviors/Links",
                "link approvers is required, so a create of Review gives it"
            )]
        );
        // A refused create leaves nothing.
        assert!(engine
            .instances()
            .get(&alice(), "Task", "x9", Default::default())
            .expect("get")
            .is_none());
        assert_eq!(
            listed(&engine, json!({"name": "reviewers", "id": "p4"})),
            json!({"items": [], "next": null})
        );
    }
}

#[test]
fn a_creates_pinned_target_with_no_revision_is_vetoed_at_its_item_of_the_list() {
    for target in targets("links_many_create_pinned") {
        let engine = open(&target, |_| {});
        publish(&engine, &schema("Spec", json!([])));
        publish(
            &engine,
            &schema(
                "Task",
                json!([{"name": "Links", "config": {"links": {"specs": {"schema": "Spec", "many": true, "max": 2, "pinned": true}}}}]),
            ),
        );
        create(&engine, "Spec", Some("s1"), None).expect("s1");
        create(&engine, "Spec", Some("s2"), None).expect("s2");
        // Revisions added to a schema with instances: s1 and s2 have none
        // until they change.
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        engine
            .instances()
            .update(
                &alice(),
                "Spec",
                "s1",
                &json!({"title": "s1, revised"}),
                WriteOptions::default(),
            )
            .expect("update");
        let (behavior, action, reason, code, details) = veto(create(
            &engine,
            "Task",
            None,
            Some(json!({"Links": {"specs": ["s1", "s2"]}})),
        ));
        assert_eq!(
            json!([behavior, action, reason, code, details]),
            json!([
                "Links",
                "create",
                "Spec s2 has no revision to pin yet",
                "no_revision",
                {"path": "/behaviors/Links/specs/1"}
            ])
        );
    }
}

#[test]
fn its_config_many_takes_a_max_from_1_to_100_and_max_needs_many() {
    for target in targets("links_many_config") {
        let engine = open(&target, |_| {});
        for reviewers in [
            json!({"schema": "Person", "many": true}),
            json!({"schema": "Person", "many": true, "max": 0}),
            json!({"schema": "Person", "many": true, "max": 101}),
            json!({"schema": "Person", "max": 3}),
            json!({"schema": "Person", "many": false, "max": 3}),
        ] {
            let paths: Vec<String> =
                refusal(define(&engine, &tasks(json!({ "reviewers": reviewers }))))
                    .issues()
                    .iter()
                    .map(|issue| issue.path.clone())
                    .collect();
            assert!(
                paths
                    .iter()
                    .any(|path| path.starts_with("/types/Task/behaviors/0")),
                "{reviewers}: {paths:?}"
            );
        }
        define(
            &engine,
            &tasks(json!({"reviewers": {"schema": "Person", "many": false}})),
        )
        .expect("define");
    }
}

#[test]
fn while_the_schema_has_instances_a_link_keeps_whether_it_holds_a_list_and_its_max_or_more() {
    for target in targets("links_many_versions") {
        let engine = open(&target, |_| {});
        publish(&engine, &schema("Person", json!([])));
        publish(&engine, &tasks(task_links()));
        // No instance: a link may become many-valued, or stop being, and max
        // may fall.
        publish(
            &engine,
            &tasks(task_links_with(
                json!({"owner": {"schema": "Person", "many": true, "max": 2}}),
            )),
        );
        publish(
            &engine,
            &tasks(task_links_with(
                json!({"reviewers": {"schema": "Person", "many": true, "max": 1}}),
            )),
        );
        publish(&engine, &tasks(task_links()));
        create(&engine, "Task", Some("t1"), None).expect("t1");
        let change =
            |members: Value| changes(&refusal(define(&engine, &tasks(task_links_with(members)))));
        assert!(
            change(json!({"owner": {"schema": "Person", "many": true, "max": 2}}))[0]
                .contains("link owner holds one target in the instances that hold it, not a list")
        );
        assert!(
            change(json!({"reviewers": {"schema": "Person"}}))[0].contains(
                "link reviewers holds a list of targets in the instances that hold it, not one"
            )
        );
        assert!(
            change(json!({"reviewers": {"schema": "Person", "many": true, "max": 2}}))[0].contains(
                "link reviewers may hold 3 targets in the instances that hold it, more than 2"
            )
        );
        assert!(change(
            json!({"approvers": {"schema": "Person", "many": true, "max": 2, "required": true}})
        )[0]
        .contains(
            "link approvers is new and required, and an instance the live version accepts holds none"
        ));
        // max may rise, and an optional many-valued link may be added.
        publish(
            &engine,
            &tasks(task_links_with(json!({
                "reviewers": {"schema": "Person", "many": true, "max": 5},
                "watchers": {"schema": "Person", "many": true, "max": 10}
            }))),
        );
    }
}

fn describe_target(schema: &str, operations: &[&str]) -> DescribeTarget {
    DescribeTarget {
        schema: schema.to_owned(),
        type_name: schema.to_owned(),
        behaviors: vec!["Links".to_owned()],
        configs: serde_json::Map::new(),
        operations: operations
            .iter()
            .map(|name| DescribedTypeOperation {
                name: (*name).to_owned(),
                behavior: Some("Links".to_owned()),
                writes: true,
                beside: false,
                scope: OperationScope::Instance,
            })
            .collect(),
    }
}

fn spec(schema: &str, many: Option<i64>, required: bool, pin: Option<LinkPin>) -> LinkSpec {
    LinkSpec {
        schema: schema.to_owned(),
        required,
        pin,
        many: many.is_some(),
        max: many,
    }
}

#[test]
fn its_guidance_and_create_parameters_say_which_links_hold_a_list_and_of_how_many() {
    let hooks = Links.hooks();
    let guidance = hooks.guidance.expect("guidance");
    let create_params = hooks.create_params_schema.expect("createParamsSchema");
    let task = LinksConfig {
        links: vec![
            ("owner".to_owned(), spec("Person", None, false, None)),
            ("reviewers".to_owned(), spec("Person", Some(3), false, None)),
            (
                "specs".to_owned(),
                spec("Spec", Some(2), false, Some(LinkPin::Revision)),
            ),
        ],
    };
    let review = LinksConfig {
        links: vec![("approvers".to_owned(), spec("Person", Some(2), true, None))],
    };
    let task_target = describe_target("Task", &["link", "unlink"]);
    let review_target = describe_target("Review", &["link", "unlink"]);

    let params = create_params(&Links, &task, &task_target);
    let reviewers = &params["properties"]["reviewers"];
    assert_eq!(
        json!([
            reviewers["type"],
            reviewers["maxItems"],
            reviewers.get("minItems")
        ]),
        json!(["array", 3, null])
    );
    let keys: Vec<&String> = params["properties"]["specs"]["items"]["properties"]
        .as_object()
        .expect("properties")
        .keys()
        .collect();
    assert_eq!(keys, ["id", "revision"]);
    let approvers = &create_params(&Links, &review, &review_target)["properties"]["approvers"];
    assert_eq!(
        json!([approvers["minItems"], approvers["maxItems"]]),
        json!([1, 2])
    );

    let task_guidance = guidance(&Links, &task, &task_target);
    let review_guidance = guidance(&Links, &review, &review_target);
    assert!(task_guidance.summary.contains("by name: owner to Person, reviewers to Person (a list of at most 3) and specs to Spec (pinned to a revision, a list of at most 2)."), "{}", task_guidance.summary);
    assert!(task_guidance.summary.contains("reviewers and specs hold a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names."));
    assert!(review_guidance.summary.contains("Every create gives at least one target of approvers, which keeps one: the last target can be neither unlinked nor deleted."));
    let operation = |said: &superschematic_engine::behaviors::BehaviorGuidance, name: &str| {
        said.operations
            .iter()
            .find(|(operation, _)| operation == name)
            .map(|(_, guidance)| guidance.clone())
            .expect("the operation's guidance")
    };
    let codes =
        |said: &superschematic_engine::behaviors::BehaviorGuidance, name: &str| -> Vec<String> {
            operation(said, name)
                .errors
                .iter()
                .map(|error| error.code.clone())
                .collect()
        };
    assert_eq!(codes(&task_guidance, "link"), ["no_revision", "too_many"]);
    assert_eq!(codes(&task_guidance, "unlink"), ["not_linked"]);
    assert_eq!(
        codes(&review_guidance, "unlink"),
        ["required_link", "not_linked"]
    );
    assert_eq!(
        operation(&task_guidance, "unlink").use_when.as_deref(),
        Some("Use to clear owner. Use with the target's id to remove one target of reviewers or specs.")
    );
    assert_eq!(
        operation(&review_guidance, "unlink")
            .do_not_use_when
            .as_deref(),
        Some("Do not remove the last target of approvers: a required link keeps one.")
    );
    assert!(operation(&task_guidance, "create")
        .use_when
        .unwrap_or_default()
        .contains(
            "Give reviewers a list of up to 3 and specs a list of up to 2, each target once."
        ));
}

#[test]
fn a_file_links_second_migration_left_is_built_again_keyed_by_target_its_rows_kept() {
    for target in targets("links_many_migration") {
        let columns = "row, namespace, schema, id, name, target_schema, target_id, revision, created_by, created_at, release";
        let links =
            json!({"owner": {"schema": "Person"}, "spec": {"schema": "Spec", "pinned": true}});
        let rows = {
            let engine = open(&target, |_| {});
            publish(&engine, &schema("Person", json!([])));
            publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
            publish(&engine, &tasks(links.clone()));
            for id in ["p1", "p2"] {
                create(&engine, "Person", Some(id), None).expect("person");
            }
            create(&engine, "Spec", Some("s1"), None).expect("spec");
            create(
                &engine,
                "Task",
                Some("t1"),
                Some(json!({"Links": {"owner": "p2", "spec": "s1"}})),
            )
            .expect("t1");
            create(
                &engine,
                "Task",
                Some("t2"),
                Some(json!({"Links": {"owner": "p1"}})),
            )
            .expect("t2");
            create(
                &engine,
                "Task",
                Some("t3"),
                Some(json!({"Links": {"owner": "p2"}})),
            )
            .expect("t3");
            engine
                .db()
                .read(|tx| {
                    tx.all(
                        format!("SELECT {columns} FROM bhv_links__links ORDER BY row"),
                        &[],
                    )
                })
                .expect("rows")
                .into_iter()
                .map(|row| row.into_values())
                .collect::<Vec<Vec<SqlValue>>>()
        };

        // The table as Links' migrations 1 and 2 left it, with the rows the
        // engine wrote, and the ledger without migration 3.
        let db = target.db();
        db.write(Lock::Catalog, |tx| {
            tx.exec("DROP TABLE bhv_links__links")?;
            match tx.dialect() {
                Dialect::Sqlite => {
                    tx.exec(
                        "CREATE TABLE bhv_links__links (
                           row           INTEGER PRIMARY KEY,
                           namespace     TEXT    NOT NULL,
                           schema        TEXT    NOT NULL,
                           id            TEXT    NOT NULL,
                           name          TEXT    NOT NULL,
                           target_schema TEXT    NOT NULL,
                           target_id     TEXT    NOT NULL,
                           revision      INTEGER,
                           created_by    TEXT    NOT NULL,
                           created_at    INTEGER NOT NULL,
                           UNIQUE (namespace, schema, id, name)
                         ) STRICT",
                    )?;
                    tx.exec("CREATE INDEX bhv_links__links_by_target ON bhv_links__links (namespace, schema, name, target_id, row)")?;
                    tx.exec("ALTER TABLE bhv_links__links ADD COLUMN release INTEGER")?;
                }
                Dialect::Postgres => {
                    tx.exec(
                        "CREATE TABLE bhv_links__links (
                           row           BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
                           namespace     TEXT   NOT NULL,
                           schema        TEXT   NOT NULL,
                           id            TEXT   NOT NULL,
                           name          TEXT   NOT NULL,
                           target_schema TEXT   NOT NULL,
                           target_id     TEXT   NOT NULL,
                           revision      BIGINT,
                           created_by    TEXT   NOT NULL,
                           created_at    BIGINT NOT NULL
                         )",
                    )?;
                    tx.exec("CREATE UNIQUE INDEX bhv_links__links_key ON bhv_links__links (namespace, schema, id, name)")?;
                    tx.exec("CREATE INDEX bhv_links__links_by_target ON bhv_links__links (namespace, schema, name, target_id, row)")?;
                    tx.exec("ALTER TABLE bhv_links__links ADD COLUMN release BIGINT")?;
                }
            }
            for row in &rows {
                tx.run(
                    format!("INSERT INTO bhv_links__links ({columns}) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
                    row,
                )?;
            }
            if tx.dialect() == Dialect::Postgres {
                // The identity goes on past the rows, as it had.
                tx.exec(format!(
                    "ALTER TABLE bhv_links__links ALTER COLUMN row RESTART WITH {}",
                    rows.len() + 1
                ))?;
            }
            tx.run(
                "DELETE FROM engine_migrations WHERE owner = 'Links' AND version = 3",
                &[],
            )?;
            Ok::<_, superschematic_engine::storage::Error>(())
        })
        .expect("the table as version 2 left it");
        drop(db);

        let engine = open(&target, |_| {});
        let applied = support::texts(
            &engine,
            "SELECT name FROM engine_migrations WHERE owner = 'Links' ORDER BY version",
            &[],
        );
        assert_eq!(applied, ["links", "release pins", "many-valued links"]);
        let kept = engine
            .db()
            .read(|tx| {
                tx.all(
                    format!("SELECT {columns} FROM bhv_links__links ORDER BY row"),
                    &[],
                )
            })
            .expect("rows")
            .into_iter()
            .map(|row| row.into_values())
            .collect::<Vec<Vec<SqlValue>>>();
        assert_eq!(kept, rows);
        let indexes = match engine.dialect() {
            Dialect::Sqlite => support::texts(
                &engine,
                "SELECT type || ' ' || name FROM sqlite_master WHERE tbl_name = 'bhv_links__links' ORDER BY name",
                &[],
            ),
            Dialect::Postgres => support::texts(
                &engine,
                "SELECT 'index ' || indexname::text FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'bhv_links__links' ORDER BY indexname COLLATE \"C\"",
                &[],
            ),
        };
        let expected: &[&str] = match engine.dialect() {
            Dialect::Sqlite => &[
                "table bhv_links__links",
                "index bhv_links__links_by_target",
                "index sqlite_autoindex_bhv_links__links_1",
            ],
            Dialect::Postgres => &[
                "index bhv_links__links_by_target",
                "index bhv_links__links_pkey",
                "index bhv_links__links_target_key",
            ],
        };
        assert_eq!(indexes, expected);
        assert_eq!(
            links_of(&engine, "Task", "t1"),
            Some(
                json!({"owner": person("p2"), "spec": {"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false}})
            )
        );
        assert_eq!(
            listed(&engine, json!({"name": "owner", "id": "p2"})),
            json!({"items": [{"id": "t1"}, {"id": "t3"}], "next": null})
        );
        // A move of a link that holds one stays in its row.
        link(&engine, "t1", json!({"name": "owner", "id": "p1"})).expect("move");
        assert_eq!(
            listed(&engine, json!({"name": "owner", "id": "p1"})),
            json!({"items": [{"id": "t1"}, {"id": "t2"}], "next": null})
        );
        // The references stay: p1's delete unlinks the owners that point at
        // it.
        assert!(delete(&engine, "Person", "p1").expect("delete"));
        assert_eq!(
            links_of(&engine, "Task", "t1"),
            Some(
                json!({"spec": {"schema": "Spec", "id": "s1", "revision": 1, "latest": 1, "stale": false}})
            )
        );
        // A many-valued link a new version adds holds a row per target.
        let mut more = links.clone();
        more["reviewers"] = json!({"schema": "Person", "many": true, "max": 2});
        publish(&engine, &tasks(more));
        create(&engine, "Person", Some("p3"), None).expect("p3");
        link(&engine, "t2", json!({"name": "reviewers", "id": "p2"})).expect("link");
        link(&engine, "t2", json!({"name": "reviewers", "id": "p3"})).expect("link");
        assert_eq!(
            links_of(&engine, "Task", "t2"),
            Some(json!({"reviewers": [person("p2"), person("p3")]}))
        );
    }
}

/// Links with Rollups and Reactions, which read a many-valued link: Rollups
/// and a revised rule across its list, the other rules not at all.
fn with_reactions() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Revisions)
        .with(Links)
        .with(Rollups)
        .with(Reactions)
}

fn open_with_reactions(target: &Target, max_attempts: Option<i64>) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = with_reactions();
        options.runner = Some(RunnerOptions {
            max_attempts,
            ..RunnerOptions::new(Principal::new("runner", Vec::<String>::new()))
        });
    })
}

fn todo_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["todo", "doing", "done"],
            "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]
        }
    })
}

fn reactions(rules: Value) -> Value {
    json!({"name": "Reactions", "config": {"rules": rules}})
}

fn many_projects() -> Value {
    json!({"name": "Links", "config": {"links": {"projects": {"schema": "Project", "many": true, "max": 3}}}})
}

// refused defines a document and returns its refusal's issues' messages,
// joined.
fn refused(engine: &Engine, document: &Value) -> String {
    issues_of(&refusal(define(engine, document)))
        .into_iter()
        .map(|(_, message)| message)
        .collect::<Vec<_>>()
        .join("; ")
}

#[test]
fn reactions_refuses_a_then_link_and_an_all_terminal_any_terminal_or_holds_link_that_holds_a_list()
{
    for target in targets("links_many_reactions_config") {
        let engine = open_with_reactions(&target, None);
        let prefix = |type_name: &str| format!("type {type_name}: behavior Reactions config: ");
        publish(&engine, &schema("Project", json!([todo_flow()])));
        assert_eq!(
            refused(
                &engine,
                &schema(
                    "Task",
                    json!([
                        todo_flow(),
                        many_projects(),
                        reactions(json!([{"when": {"enters": "doing"}, "then": {"link": "projects", "transition": "doing"}}]))
                    ])
                )
            ),
            format!("{}rule 1: then.link projects holds a list of targets, and the rule reads the one instance it points at", prefix("Task"))
        );
        let parents = json!({"name": "Links", "config": {"links": {"parents": {"schema": "Task", "many": true, "max": 2}}}});
        assert_eq!(
            refused(
                &engine,
                &schema(
                    "Task",
                    json!([
                        todo_flow(),
                        parents,
                        reactions(json!([{"when": {"allTerminal": {"schema": "Task", "link": "parents"}}, "then": {"transition": "done"}}]))
                    ])
                )
            ),
            format!("{}rule 1: when.allTerminal names link parents of Task, which holds a list of targets, and the rule reads the one instance it points at", prefix("Task"))
        );
        // A linked schema's live version is read when the schema is
        // defined.
        publish(
            &engine,
            &schema("Task", json!([todo_flow(), many_projects()])),
        );
        assert_eq!(
            refused(
                &engine,
                &schema(
                    "Project",
                    json!([
                        todo_flow(),
                        reactions(json!([{"when": {"anyTerminal": {"schema": "Task", "link": "projects", "outcomes": ["success"]}}, "then": {"transition": "done"}}]))
                    ])
                )
            ),
            format!("{}rule 1: when.anyTerminal names link projects of Task, which holds a list of targets, and the rule reads the one instance it points at", prefix("Project"))
        );
        // Rollups reads across the list; a holds rule on it reads one
        // instance.
        let rollups = json!({"name": "Rollups", "config": {"rollups": {"finished": {"schema": "Task", "link": "projects", "function": "all"}}}});
        define(
            &engine,
            &schema("Project", json!([todo_flow(), rollups.clone()])),
        )
        .expect("a rollup across a list");
        assert_eq!(
            refused(
                &engine,
                &schema(
                    "Project",
                    json!([
                        todo_flow(),
                        rollups,
                        reactions(json!([{"when": {"holds": "finished"}, "then": {"transition": "done"}}]))
                    ])
                )
            ),
            format!("{}rule 1: when.holds names rollup finished, whose link projects of Task holds a list of targets, and the rule reads the one instance it points at", prefix("Project"))
        );
    }
}

#[test]
fn a_linked_schema_whose_link_holds_a_list_since_the_rule_was_defined_halts_the_subscription() {
    for target in targets("links_many_reactions_halt") {
        let engine = open_with_reactions(&target, Some(1));
        publish(
            &engine,
            &schema(
                "Project",
                json!([
                    todo_flow(),
                    reactions(
                        json!([{"when": {"allTerminal": {"schema": "Task", "link": "projects"}}, "then": {"transition": "done"}}])
                    )
                ]),
            ),
        );
        publish(
            &engine,
            &schema("Task", json!([todo_flow(), many_projects()])),
        );
        create(&engine, "Project", Some("p1"), None).expect("project");
        create(
            &engine,
            "Task",
            Some("t1"),
            Some(json!({"Links": {"projects": ["p1"]}})),
        )
        .expect("task");
        engine.runner().run_due().expect("run due");
        let project = engine
            .runner()
            .status()
            .expect("the runner's status")
            .subscriptions
            .into_iter()
            .find(|subscription| subscription.schema == "Project")
            .expect("Project's subscription");
        assert_eq!(
            (project.state, project.failure.map(|failure| failure.error)),
            (
                SubscriptionState::Halted,
                Some("BehaviorError: behavior Reactions: allTerminal names link projects of Task, which holds a list of targets, and the rule reads the one instance it points at".to_owned())
            )
        );
    }
}

#[test]
fn rollups_counts_an_instance_in_every_target_its_list_holds_and_revised_hears_each_target() {
    for target in targets("links_many_rollups_revised") {
        let engine = open_with_reactions(&target, None);
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        publish(&engine, &schema("Project", json!([])));
        let stale = json!({"name": "Workflow", "config": {"states": ["ready", "stale"], "transitions": [{"from": "ready", "to": "stale"}]}});
        publish(
            &engine,
            &schema(
                "Task",
                json!([
                    stale,
                    {"name": "Links", "config": {"links": {
                        "projects": {"schema": "Project", "many": true, "max": 3},
                        "specs": {"schema": "Spec", "many": true, "max": 2, "pinned": true}
                    }}},
                    reactions(json!([{"when": {"revised": {"link": "specs"}}, "then": {"transition": "stale"}}]))
                ]),
            ),
        );
        publish(
            &engine,
            &schema(
                "Project",
                json!([{"name": "Rollups", "config": {"rollups": {"tasks": {"schema": "Task", "link": "projects", "function": "count"}}}}]),
            ),
        );
        for id in ["p1", "p2"] {
            create(&engine, "Project", Some(id), None).expect("project");
        }
        for id in ["s1", "s2"] {
            create(&engine, "Spec", Some(id), None).expect("spec");
        }
        create(
            &engine,
            "Task",
            Some("t1"),
            Some(json!({"Links": {"projects": ["p1", "p2"], "specs": ["s1", "s2"]}})),
        )
        .expect("task");
        create(
            &engine,
            "Task",
            Some("t2"),
            Some(json!({"Links": {"projects": ["p1"], "specs": ["s2"]}})),
        )
        .expect("task");
        let read = |schema: &str, id: &str| {
            engine
                .instances()
                .get(&alice(), schema, id, Default::default())
                .expect("get")
                .expect("the instance")
        };
        let rollup = |id: &str| read("Project", id).behaviors["Rollups"]["values"]["tasks"].clone();
        assert_eq!([rollup("p1"), rollup("p2")], [json!(2), json!(1)]);
        engine.runner().run_due().expect("run due");
        engine
            .instances()
            .update(
                &alice(),
                "Spec",
                "s1",
                &json!({"title": "s1, revised"}),
                WriteOptions::default(),
            )
            .expect("update");
        engine.runner().run_due().expect("run due");
        let status = |id: &str| read("Task", id).behaviors["Workflow"]["status"].clone();
        assert_eq!(
            [status("t1"), status("t2")],
            [json!("stale"), json!("ready")]
        );
    }
}
