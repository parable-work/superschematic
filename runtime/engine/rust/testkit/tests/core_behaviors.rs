//! The core's behaviors with no extension: the cases of
//! `test/core-behaviors.test.ts` (an engine opened with them registers all
//! fifteen and no one else can take their names; the core's meta-schema
//! admits a work-queue behavior, which an engine without the work queue
//! refuses; the steps document the core binary builds, and the documents
//! one, with Workflow, Comments and Revisions on one type; the tasks,
//! projects and numbered invoices documents, whose projects compose
//! Rollups; the project tree document, whose projects Reactions starts,
//! finishes and fails; and the recipes one, whose recipes are version
//! graphs of Branches), on SQLite and, with the variable set, on Postgres.
//! The notes document runs in the Search file; the expenses, requests and
//! invoices documents in the Approvals, Timers and StateRules files.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine, Error, Hooks,
    Principal, Result, SchemaTarget, WriteOptions,
};
use support::{alice, refusal, repository, targets, usage};

fn writer() -> Principal {
    Principal::new("wes", Vec::<String>::new())
}

// An implementation that takes Workflow's name.
struct Impostor;

impl Behavior for Impostor {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Workflow"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn an_engine_opened_with_the_core_registers_it_and_no_one_else_can_take_its_names() {
    for target in targets("core_names") {
        let engine = target.open_core();
        let mut ported: Vec<&str> = superschematic_engine_behaviors::NAMES.to_vec();
        ported.sort_unstable();
        assert_eq!(engine.behaviors().names(), ported);
        let fields: Vec<Value> = engine
            .behaviors()
            .declaration("Workflow")
            .expect("Workflow's declaration")["fields"]
            .as_array()
            .expect("fields")
            .iter()
            .map(|field| field["name"].clone())
            .collect();
        assert_eq!(fields, [json!("status"), json!("enteredAt")]);
        assert_eq!(
            usage(engine.behaviors().register(Impostor)),
            "behavior Workflow is already registered with this engine"
        );
        let opened = Engine::open({
            let mut options = target.core_options();
            options.behaviors =
                std::mem::take(&mut options.behaviors).extend(BehaviorSet::new().with(Impostor));
            options
        });
        assert!(
            usage(opened).contains("already registered"),
            "an open that names a core behavior again"
        );
    }
}

#[test]
fn the_core_also_declares_the_work_queue_behaviors_which_this_engine_runs_only_once_a_deployment_registers_them(
) {
    for target in targets("core_work_queue") {
        let engine = target.open_core();
        let mut document = fixture("fixture-search-json", "note.schema.json");
        document["types"]["Note"]["behaviors"] =
            json!([{"name": "Lease", "config": {"ttlMs": 30000}}]);
        // The core meta-schema admits Lease, so the loader passes it; the
        // engine has no implementation of it.
        let refused = refusal(engine.schemas().define(
            &alice(),
            &document,
            DefineOptions::default(),
        ));
        assert_eq!(refused.code().as_str(), "invalid_schema");
        assert!(
            refused
                .message()
                .contains("behavior Lease on type Note: no implementation registered"),
            "{}",
            refused.message()
        );
    }
}

/// The steps document the core binary loads with no extension linked
/// (`fixture-variants-json`).
fn steps() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the steps document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_steps_document_a_steps_kind_stays_and_its_result_has_the_kinds_shape() {
    for target in targets("core_steps") {
        let engine = target.open_core();
        engine
            .schemas()
            .define(&alice(), steps(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Step", SchemaTarget::default())
            .expect("publish");
        let check = engine
            .instances()
            .create(
                &writer(),
                "Step",
                &json!({"title": "Lint", "kind": "verify", "result": {"passed": true, "checks": [{"name": "eslint", "ok": true}]}}),
                CreateOptions::default(),
            )
            .expect("create");
        assert_eq!(check.seq, 1);
        let update = |patch: Value| -> Result<Value, Error> {
            engine
                .instances()
                .update(
                    &writer(),
                    "Step",
                    &check.id,
                    &patch,
                    WriteOptions::default(),
                )
                .map(|record| Value::Object(record.data))
        };
        assert_eq!(
            refusal(update(json!({"kind": "note", "result": null}))).message(),
            "Step in namespace default (version 1): kind: kind is a constant of Step: its create sets it and nothing changes it after"
        );
        assert_eq!(
            refusal(update(json!({"result": {"passed": "yes"}})))
                .code()
                .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            refusal(engine.instances().create(
                &writer(),
                "Step",
                &json!({"title": "Idea", "kind": "note", "result": {"passed": true}}),
                CreateOptions::default(),
            ))
            .code()
            .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            update(json!({"result": {"passed": false}})).expect("update"),
            json!({"title": "Lint", "kind": "verify", "result": {"passed": false, "checks": [{"name": "eslint", "ok": true}]}})
        );
    }
}

/// The documents document the core binary loads with no extension linked
/// (`fixture-behaviors-json`): Workflow, Comments and Revisions on one
/// type.
fn documents() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-behaviors-json/src/document.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the documents document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_document_the_core_binary_builds_with_all_three_composed_on_one_type() {
    use superschematic_engine::instances::patch::merge_patch;
    use superschematic_engine::{ReadEvents, ValidateOptions};

    let reviewer = Principal::new("rae", ["documents.review"]);
    let publisher = Principal::new("pat", ["documents"]);
    for target in targets("core_documents") {
        let engine = target.open_core();
        engine
            .schemas()
            .define(&alice(), documents(), DefineOptions::default())
            .expect("define");
        let published = engine
            .schemas()
            .publish(&alice(), "documents", SchemaTarget::default())
            .expect("publish");
        assert_eq!((published.version, published.published), (1, true));
        let composed: Vec<String> = engine
            .schemas()
            .behaviors(&alice(), "documents", ValidateOptions::default())
            .expect("behaviors")
            .into_iter()
            .map(|behavior| behavior.name)
            .collect();
        assert_eq!(composed, ["Workflow", "Comments", "Revisions"]);

        let created = engine
            .instances()
            .create(
                &writer(),
                "documents",
                &json!({"title": "Launch plan"}),
                CreateOptions {
                    id: Some("doc-1"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            Value::Object(created.data.clone()),
            json!({"title": "Launch plan"})
        );
        assert_eq!(
            Value::Object(created.behaviors.clone()),
            json!({
                "Workflow": {"status": "draft", "enteredAt": created.created_at},
                "Comments": {"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": []},
                "Revisions": {"revision": 1, "pendingProposals": 0}
            })
        );

        let invoke = |principal: &Principal, operation: &str, params: Value| {
            engine.instances().invoke(
                principal,
                "documents",
                "doc-1",
                operation,
                &params,
                WriteOptions::default(),
            )
        };
        invoke(&writer(), "post", json!({"body": "First pass is up."})).expect("post");
        invoke(&writer(), "transition", json!({"to": "review"})).expect("transition");
        invoke(
            &writer(),
            "propose",
            json!({"patch": {"body": "The plan, in full."}, "note": "Adds the body."}),
        )
        .expect("propose");
        assert_eq!(
            refusal(invoke(&writer(), "approve", json!({"proposal": 1})))
                .code()
                .as_str(),
            "forbidden"
        );
        invoke(&reviewer, "approve", json!({"proposal": 1})).expect("approve");
        invoke(
            &reviewer,
            "post",
            json!({"body": "Approved the body.", "thread": 1}),
        )
        .expect("reply");
        assert_eq!(
            refusal(invoke(&reviewer, "transition", json!({"to": "published"})))
                .code()
                .as_str(),
            "forbidden"
        );
        invoke(&publisher, "transition", json!({"to": "published"})).expect("publish");

        let read = |id: &str| {
            engine
                .instances()
                .get(&alice(), "documents", id, Default::default())
                .expect("get")
                .expect("the document")
        };
        let events = |id: &str| {
            engine
                .events()
                .read(
                    &alice(),
                    &ReadEvents {
                        schema: Some("documents"),
                        instance_id: Some(id),
                        ..ReadEvents::default()
                    },
                )
                .expect("events")
                .events
        };
        let record = read("doc-1");
        let log = events("doc-1");
        assert_eq!(
            Value::Object(record.data.clone()),
            json!({"title": "Launch plan", "body": "The plan, in full."})
        );
        // The status was last entered by the last transition, the seventh
        // event; the posts are beside the instance, and took no sequence.
        assert_eq!(
            Value::Object(record.behaviors.clone()),
            json!({
                "Workflow": {"status": "published", "enteredAt": log[6].at},
                "Comments": {"messages": 2, "lastMessageAt": log[5].at, "threads": {"open": 1, "resolved": 0}, "composing": []},
                "Revisions": {"revision": 2, "pendingProposals": 0}
            })
        );
        assert_eq!(record.seq, 5);
        let changes: Vec<Value> = log
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.actor,
                    if event.beside {
                        json!({"beside": event.change["operation"], "seq": event.seq})
                    } else if event.kind == "operation" {
                        event.change["patch"].clone()
                    } else {
                        event.change.clone()
                    }
                ])
            })
            .collect();
        assert_eq!(
            changes,
            [
                json!(["create", "wes", {
                    "data": {"title": "Launch plan"},
                    "behaviors": {"Workflow": {"status": "draft", "enteredAt": log[0].at}, "Comments": {"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": []}, "Revisions": {"revision": 1, "pendingProposals": 0}}
                }]),
                json!(["operation", "wes", {"beside": "post", "seq": null}]),
                json!(["operation", "wes", {"behaviors": {"Workflow": {"status": "review", "enteredAt": log[2].at}}}]),
                json!(["operation", "wes", {"behaviors": {"Revisions": {"pendingProposals": 1}}}]),
                json!(["operation", "rae", {"data": {"body": "The plan, in full."}, "behaviors": {"Revisions": {"revision": 2, "pendingProposals": 0}}}]),
                json!(["operation", "rae", {"beside": "post", "seq": null}]),
                json!(["operation", "pat", {"behaviors": {"Workflow": {"status": "published", "enteredAt": log[6].at}}}]),
            ]
        );
        // The log replays, a merge patch at a time, to the instance a read
        // returns, but for the volatile fields beside events move.
        let mut replayed = json!({});
        for event in log.iter().filter(|event| !event.beside) {
            let patch = if event.kind == "create" {
                &event.change
            } else {
                &event.change["patch"]
            };
            replayed = merge_patch(&replayed, patch);
        }
        let mut behaviors = record.behaviors.clone();
        behaviors.insert(
            "Comments".to_owned(),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": []}),
        );
        assert_eq!(
            replayed,
            json!({"data": record.data, "behaviors": behaviors})
        );

        let revisions = invoke(&alice(), "listRevisions", json!({})).expect("listRevisions");
        let summary: Vec<Value> = revisions["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| json!([item["revision"], item["data"], item.get("proposal")]))
            .collect();
        assert_eq!(
            summary,
            [
                json!([1, {"title": "Launch plan"}, null]),
                json!([2, {"title": "Launch plan", "body": "The plan, in full."}, 1]),
            ]
        );
        let threads: Vec<Value> = [json!({}), json!({"thread": 1})]
            .into_iter()
            .flat_map(|params| {
                invoke(&alice(), "listMessages", params).expect("listMessages")["items"]
                    .as_array()
                    .expect("items")
                    .clone()
            })
            .map(|item| json!([item.get("thread"), item["author"]]))
            .collect();
        assert_eq!(threads, [json!([null, "wes"]), json!([1, "rae"])]);

        // A new version adds an optional field and a state; the instance
        // keeps its history.
        let mut next = documents();
        next["types"]["Document"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "summary", "typeRef": {"name": "string"}}));
        let flow = &mut next["types"]["Document"]["behaviors"][0]["config"];
        flow["states"]
            .as_array_mut()
            .expect("states")
            .push(json!("retracted"));
        flow["transitions"]
            .as_array_mut()
            .expect("transitions")
            .push(json!({"from": "published", "to": "retracted"}));
        engine
            .schemas()
            .define(&alice(), &next, DefineOptions::default())
            .expect("define");
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "documents", SchemaTarget::default())
                .expect("publish")
                .version,
            2
        );
        invoke(&writer(), "transition", json!({"to": "retracted"})).expect("retract");
        engine
            .instances()
            .update(
                &writer(),
                "documents",
                "doc-1",
                &json!({"summary": "Withdrawn."}),
                WriteOptions::default(),
            )
            .expect("update");
        let retracted = read("doc-1");
        assert_eq!(
            Value::Object(retracted.data.clone()),
            json!({"title": "Launch plan", "body": "The plan, in full.", "summary": "Withdrawn."})
        );
        assert_eq!(
            Value::Object(retracted.behaviors),
            json!({
                "Workflow": {"status": "retracted", "enteredAt": events("doc-1")[7].at},
                "Comments": {"messages": 2, "lastMessageAt": log[5].at, "threads": {"open": 1, "resolved": 0}, "composing": []},
                "Revisions": {"revision": 3, "pendingProposals": 0}
            })
        );
    }
}

/// A document of the fixtures the core binary loads with no extension
/// linked, by its service's directory and file.
fn fixture(service: &str, file: &str) -> Value {
    let path = repository().join(format!(
        "internal/loader/testdata/services/{service}/src/{file}"
    ));
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the fixture document"))
        .expect("JSON")
}

/// The tasks document (`fixture-cross-instance-json`): Workflow,
/// Dependencies and Links on one type.
fn tasks() -> Value {
    fixture("fixture-cross-instance-json", "task.schema.json")
}

/// The projects document (`fixture-rollups-json`): a project rolls up the
/// tasks that point at it.
fn projects() -> Value {
    fixture("fixture-rollups-json", "project.schema.json")
}

fn publish_all(engine: &Engine, documents: &[Value]) {
    for document in documents {
        engine
            .schemas()
            .define(&alice(), document, DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(
                &alice(),
                document["name"].as_str().expect("a name"),
                SchemaTarget::default(),
            )
            .expect("publish");
    }
}

fn composed(engine: &Engine, schema: &str) -> Vec<String> {
    engine
        .schemas()
        .behaviors(
            &alice(),
            schema,
            superschematic_engine::ValidateOptions::default(),
        )
        .expect("behaviors")
        .into_iter()
        .map(|behavior| behavior.name)
        .collect()
}

fn create_as(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: Option<&str>,
    data: Value,
    behaviors: Option<Value>,
) -> Result<superschematic_engine::InstanceRecord, Error> {
    engine.instances().create(
        principal,
        schema,
        &data,
        CreateOptions {
            id,
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

fn invoke_as(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn read_as(engine: &Engine, schema: &str, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance")
}

#[test]
fn it_runs_the_tasks_document_beside_the_documents_one_blockers_of_both_and_links_to_both_and_the_project(
) {
    use superschematic_engine::{EventsAfter, ReadEvents};

    let publisher = Principal::new("pat", ["documents"]);
    for target in targets("core_tasks") {
        let engine = target.open_core();
        publish_all(&engine, &[documents(), tasks(), projects()]);
        assert_eq!(
            composed(&engine, "tasks"),
            ["Workflow", "Dependencies", "Links"]
        );
        create_as(
            &engine,
            &writer(),
            "documents",
            Some("doc-1"),
            json!({"title": "Design"}),
            None,
        )
        .expect("create");
        create_as(
            &engine,
            &writer(),
            "projects",
            Some("launch"),
            json!({"title": "Launch"}),
            None,
        )
        .expect("create");
        // A task's project is required: its create gives it.
        assert_eq!(
            refusal(create_as(
                &engine,
                &writer(),
                "tasks",
                None,
                json!({"title": "Stray"}),
                None
            ))
            .code()
            .as_str(),
            "invalid_argument"
        );
        let project = json!({"Links": {"project": "launch"}});
        create_as(
            &engine,
            &writer(),
            "tasks",
            Some("plan"),
            json!({"title": "Plan"}),
            Some(project.clone()),
        )
        .expect("create");
        let created = create_as(
            &engine,
            &writer(),
            "tasks",
            Some("build"),
            json!({"title": "Build"}),
            Some(project),
        )
        .expect("create");
        assert_eq!(
            json!([created.data, created.behaviors]),
            json!([
                {"title": "Build"},
                {
                    "Workflow": {"status": "todo", "enteredAt": created.created_at},
                    "Dependencies": {"blocked": false},
                    "Links": {"targets": {"project": {"schema": "projects", "id": "launch"}}}
                }
            ])
        );

        // build waits on plan and on the design, implements the design at
        // its first revision, and belongs to plan.
        let task = |id: &str, operation: &str, params: Value| {
            invoke_as(&engine, &writer(), "tasks", id, operation, params)
        };
        task("build", "addBlocker", json!({"id": "plan"})).expect("addBlocker");
        task(
            "build",
            "addBlocker",
            json!({"schema": "documents", "id": "doc-1"}),
        )
        .expect("addBlocker");
        task("build", "link", json!({"name": "spec", "id": "doc-1"})).expect("link");
        task("build", "link", json!({"name": "parent", "id": "plan"})).expect("link");
        task("build", "transition", json!({"to": "doing"})).expect("doing");
        let doing = read_as(&engine, "tasks", "build");
        assert_eq!(
            Value::Object(doing.behaviors.clone()),
            json!({
                "Workflow": {"status": "doing", "enteredAt": doing.updated_at},
                "Dependencies": {"blocked": true},
                "Links": {"targets": {
                    "parent": {"schema": "tasks", "id": "plan"},
                    "project": {"schema": "projects", "id": "launch"},
                    "spec": {"schema": "documents", "id": "doc-1", "revision": 1, "latest": 1, "stale": false}
                }}
            })
        );
        // A create gives the same edges and links in one event: test waits on
        // plan.
        let test = create_as(
            &engine,
            &writer(),
            "tasks",
            Some("test"),
            json!({"title": "Test"}),
            Some(json!({
                "Links": {"project": "launch", "parent": "plan"},
                "Dependencies": {"blockers": [{"id": "plan"}]}
            })),
        )
        .expect("create");
        let linked: Vec<&str> = test.behaviors["Links"]["targets"]
            .as_object()
            .expect("targets")
            .keys()
            .map(String::as_str)
            .collect();
        assert_eq!(
            (
                test.seq,
                test.behaviors["Dependencies"]["blocked"].clone(),
                linked
            ),
            (1, json!(true), vec!["parent", "project"])
        );
        engine
            .instances()
            .delete(&writer(), "tasks", "test", WriteOptions::default())
            .expect("delete");
        assert_eq!(
            refusal(task("build", "transition", json!({"to": "done"})))
                .code()
                .as_str(),
            "vetoed"
        );

        // The design is revised, then archived, a terminal state; plan is
        // done.
        engine
            .instances()
            .update(
                &writer(),
                "documents",
                "doc-1",
                &json!({"body": "Revised."}),
                WriteOptions::default(),
            )
            .expect("update");
        let document = |principal: &Principal, to: &str| {
            invoke_as(
                &engine,
                principal,
                "documents",
                "doc-1",
                "transition",
                json!({ "to": to }),
            )
            .expect("transition")
        };
        document(&writer(), "review");
        document(&publisher, "published");
        document(&writer(), "archived");
        task("plan", "transition", json!({"to": "doing"})).expect("doing");
        task("plan", "transition", json!({"to": "done"})).expect("done");
        let build = read_as(&engine, "tasks", "build");
        assert_eq!(
            json!([
                build.behaviors["Dependencies"]["blocked"],
                build.behaviors["Links"]["targets"]["spec"]
            ]),
            json!([false, {"schema": "documents", "id": "doc-1", "revision": 1, "latest": 2, "stale": true}])
        );
        assert_eq!(
            engine
                .instances()
                .invoke_schema(
                    &alice(),
                    "tasks",
                    "listLinked",
                    &json!({"name": "spec", "id": "doc-1", "stale": true}),
                    None
                )
                .expect("listLinked"),
            json!({"items": [{"id": "build", "revision": 1, "latest": 2, "stale": true}], "next": null})
        );
        assert_eq!(
            task("build", "transition", json!({"to": "done"})).expect("done"),
            json!({"from": "doing", "to": "done"})
        );
        let entered_done = read_as(&engine, "tasks", "build").updated_at;

        // launch is the tasks' required project, so it stays; the design can
        // go, which removes build's edge to it and clears its spec link.
        assert_eq!(
            refusal(engine.instances().delete(
                &writer(),
                "projects",
                "launch",
                WriteOptions::default()
            ))
            .code()
            .as_str(),
            "vetoed"
        );
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
        assert!(engine
            .instances()
            .delete(&writer(), "documents", "doc-1", WriteOptions::default())
            .expect("delete"));
        let since: Vec<Value> = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Cursor(after)),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.schema,
                    event.instance_id,
                    event.change.get("operation")
                ])
            })
            .collect();
        assert_eq!(
            since,
            [
                json!(["delete", "documents", "doc-1", null]),
                json!(["operation", "tasks", "build", "removeBlocker"]),
                json!(["operation", "tasks", "build", "unlink"])
            ]
        );
        let done = read_as(&engine, "tasks", "build");
        assert_eq!(
            json!([done.data, done.behaviors]),
            json!([
                {"title": "Build"},
                {
                    "Workflow": {"status": "done", "enteredAt": entered_done},
                    "Dependencies": {"blocked": false},
                    "Links": {"targets": {
                        "parent": {"schema": "tasks", "id": "plan"},
                        "project": {"schema": "projects", "id": "launch"}
                    }}
                }
            ])
        );
    }
}

#[test]
fn it_runs_the_projects_document_beside_the_tasks_one_a_project_rolls_up_its_tasks_and_waits_for_them(
) {
    for target in targets("core_projects") {
        let engine = target.open_core();
        publish_all(&engine, &[documents(), tasks(), projects()]);
        assert_eq!(composed(&engine, "projects"), ["Workflow", "Rollups"]);
        let created = create_as(
            &engine,
            &writer(),
            "projects",
            Some("launch"),
            json!({"title": "Launch"}),
            None,
        )
        .expect("create");
        assert_eq!(
            json!([created.data, created.behaviors]),
            json!([
                {"title": "Launch"},
                {
                    "Workflow": {"status": "active", "enteredAt": created.created_at},
                    "Rollups": {"values": {"tasks": 0, "tasksByStatus": {}, "tasksFinished": true}}
                }
            ])
        );
        for id in ["plan", "build"] {
            create_as(
                &engine,
                &writer(),
                "tasks",
                Some(id),
                json!({ "title": id }),
                Some(json!({"Links": {"project": "launch"}})),
            )
            .expect("create");
        }
        let task = |id: &str, to: &str| {
            invoke_as(
                &engine,
                &writer(),
                "tasks",
                id,
                "transition",
                json!({ "to": to }),
            )
            .expect("transition")
        };
        task("plan", "doing");
        assert_eq!(
            read_as(&engine, "projects", "launch").behaviors["Rollups"]["values"],
            json!({"tasks": 2, "tasksByStatus": {"doing": 1, "todo": 1}, "tasksFinished": false})
        );
        let finish = || {
            invoke_as(
                &engine,
                &writer(),
                "projects",
                "launch",
                "transition",
                json!({"to": "done"}),
            )
        };
        assert_eq!(refusal(finish()).code().as_str(), "vetoed");
        task("plan", "done");
        task("build", "dropped");
        let read = read_as(&engine, "projects", "launch");
        assert_eq!(
            json!([read.seq, read.behaviors["Rollups"]["values"]]),
            json!([1, {"tasks": 2, "tasksByStatus": {"done": 1, "dropped": 1}, "tasksFinished": true}])
        );
        assert_eq!(
            finish().expect("done"),
            json!({"from": "active", "to": "done"})
        );
    }
}

/// The project tree document (`fixture-reactions-json`): a project links to
/// its parent, and Reactions moves the parent as its children start,
/// finish and fail.
fn project_tree() -> Value {
    fixture("fixture-reactions-json", "project.schema.json")
}

#[test]
fn it_runs_the_project_tree_document_a_project_starts_finishes_and_fails_its_parent_after_the_commit(
) {
    use superschematic_engine::{ReadEvents, RunnerOptions};

    for target in targets("core_project_tree") {
        let engine = target.open_core_with(|options| {
            options.runner = Some(RunnerOptions::new(Principal::new(
                "runner",
                Vec::<String>::new(),
            )));
        });
        publish_all(&engine, &[project_tree()]);
        for id in ["launch", "design", "build"] {
            create_as(
                &engine,
                &writer(),
                "projects",
                Some(id),
                json!({ "title": id }),
                None,
            )
            .expect("create");
        }
        let link = |id: &str, parent: &str| {
            invoke_as(
                &engine,
                &writer(),
                "projects",
                id,
                "link",
                json!({"name": "parent", "id": parent}),
            )
            .expect("link");
        };
        let move_to = |id: &str, to: &str| {
            invoke_as(
                &engine,
                &writer(),
                "projects",
                id,
                "transition",
                json!({ "to": to }),
            )
            .expect("transition");
        };
        let statuses = |ids: &[&str]| -> Vec<Value> {
            ids.iter()
                .map(|id| read_as(&engine, "projects", id).behaviors["Workflow"]["status"].clone())
                .collect()
        };
        for id in ["design", "build"] {
            link(id, "launch");
        }
        let tree = ["launch", "design", "build"];

        move_to("design", "doing");
        assert_eq!(
            statuses(&tree),
            [json!("todo"), json!("doing"), json!("todo")]
        );
        engine.runner().run_due().expect("run due");
        assert_eq!(
            statuses(&tree),
            [json!("doing"), json!("doing"), json!("todo")]
        );
        move_to("design", "done");
        move_to("build", "doing");
        move_to("build", "done");
        engine.runner().run_due().expect("run due");
        assert_eq!(
            statuses(&tree),
            [json!("done"), json!("done"), json!("done")]
        );
        let launch = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("projects"),
                    instance_id: Some("launch"),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        let moves: Vec<(String, Value, Option<String>, Option<i64>)> = launch[1..]
            .iter()
            .map(|event| {
                (
                    event.actor.clone(),
                    event.change["params"].clone(),
                    event.cause.as_ref().map(|cause| cause.behavior.clone()),
                    event.cause.as_ref().map(|cause| cause.depth),
                )
            })
            .collect();
        assert_eq!(
            moves,
            [
                (
                    "runner".to_owned(),
                    json!({"to": "doing"}),
                    Some("Reactions".to_owned()),
                    Some(1)
                ),
                (
                    "runner".to_owned(),
                    json!({"to": "done"}),
                    Some("Reactions".to_owned()),
                    Some(1)
                )
            ]
        );

        // A tree with a failed project fails its parent, and is never done.
        for id in ["release", "docs", "site"] {
            create_as(
                &engine,
                &writer(),
                "projects",
                Some(id),
                json!({ "title": id }),
                None,
            )
            .expect("create");
        }
        for id in ["docs", "site"] {
            link(id, "release");
            move_to(id, "doing");
        }
        move_to("docs", "failed");
        move_to("site", "done");
        engine.runner().run_due().expect("run due");
        assert_eq!(
            statuses(&["release", "docs", "site"]),
            [json!("failed"), json!("failed"), json!("done")]
        );
    }
}

#[test]
fn it_runs_the_invoices_document_beside_the_projects_one_an_invoice_is_numbered_in_its_projects_count(
) {
    use superschematic_engine::{Clock, ListOptions};

    // Date.UTC(2026, 11, 31) and Date.UTC(2027, 0, 1).
    const DECEMBER_31: i64 = 1_798_675_200_000;
    const JANUARY_1: i64 = 1_798_761_600_000;
    for target in targets("core_numbered_invoices") {
        let engine = target.open_core_with(|options| {
            options.clock = Clock::Manual(DECEMBER_31);
        });
        publish_all(
            &engine,
            &[
                documents(),
                tasks(),
                projects(),
                fixture("fixture-numbering-json", "invoice.schema.json"),
            ],
        );
        assert_eq!(composed(&engine, "invoices"), ["Links", "Numbering"]);
        for id in ["launch", "site"] {
            create_as(
                &engine,
                &writer(),
                "projects",
                Some(id),
                json!({ "title": id }),
                None,
            )
            .expect("create");
        }
        let bill = |id: &str, project: &str| {
            create_as(
                &engine,
                &writer(),
                "invoices",
                Some(id),
                json!({"title": id, "amount": 100}),
                Some(json!({"Links": {"project": project}})),
            )
            .expect("create")
            .behaviors["Numbering"]
                .clone()
        };
        assert_eq!(
            bill("a", "launch"),
            json!({"seq": 1, "label": "INV-2026-0001"})
        );
        assert_eq!(
            bill("b", "launch"),
            json!({"seq": 2, "label": "INV-2026-0002"})
        );
        assert_eq!(
            bill("c", "site"),
            json!({"seq": 1, "label": "INV-2026-0001"})
        );
        engine.clock().set(JANUARY_1).expect("a manual clock");
        assert_eq!(
            bill("d", "launch"),
            json!({"seq": 1, "label": "INV-2027-0001"})
        );
        let filter = json!({"Numbering.label": "INV-2026-0001"});
        let found: Vec<Value> = engine
            .instances()
            .list(
                &alice(),
                "invoices",
                ListOptions {
                    filter: Some(&filter),
                    ..ListOptions::default()
                },
            )
            .expect("list")
            .items
            .iter()
            .map(|item| json!([item.id, item.behaviors["Links"]["targets"]]))
            .collect();
        assert_eq!(
            found,
            [
                json!(["a", {"project": {"schema": "projects", "id": "launch"}}]),
                json!(["c", {"project": {"schema": "projects", "id": "site"}}])
            ]
        );
    }
}

/// The recipes document the core binary builds (`fixture-branches-json`):
/// each recipe a version graph's root.
fn recipes() -> Value {
    fixture("fixture-branches-json", "recipe.schema.json")
}

#[test]
fn it_runs_the_recipes_document_whose_drafts_merge_into_the_primary_line_and_a_tagged_commit_is_released(
) {
    use superschematic_engine::RunnerOptions;

    for target in targets("core_recipes") {
        let engine = target.open_core_with(|options| {
            options.runner = Some(RunnerOptions::new(Principal::new(
                "runner",
                Vec::<String>::new(),
            )));
        });
        engine
            .schemas()
            .define(&alice(), recipes(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Recipe", SchemaTarget::default())
            .expect("publish");
        engine
            .instances()
            .create(
                &writer(),
                "Recipe",
                &json!({"title": "Soup"}),
                CreateOptions {
                    id: Some("soup"),
                    ..Default::default()
                },
            )
            .expect("create");
        let invoke = |operation: &str, params: Value| {
            engine
                .instances()
                .invoke(
                    &writer(),
                    "Recipe",
                    "soup",
                    operation,
                    &params,
                    WriteOptions::default(),
                )
                .unwrap_or_else(|error| panic!("{operation}: {error}"))
        };
        let main = invoke("refs", json!({}))["items"][0].clone();
        let draft = invoke("branch", json!({"fromRef": main["id"], "name": "first"}));
        let saved = invoke(
            "save",
            json!({
                "ref": draft["id"],
                "version": draft["version"],
                "edits": {"step": {"upsert": [{"instruction": "Boil", "position": 1}]}, "cover": {"upsert": [{"photoUrl": "soup.jpg"}]}}
            }),
        );
        let version = saved["ref"]["version"].as_i64().expect("a version");
        invoke(
            "save",
            json!({
                "ref": draft["id"],
                "version": version,
                "edits": {"ingredient": {"upsert": [{"stepKey": saved["saved"]["step"][0]["entity_key"], "quantity": "1 l"}]}}
            }),
        );
        let committed = invoke(
            "commit",
            json!({"ref": draft["id"], "version": version + 1}),
        );
        let merged = invoke(
            "merge",
            json!({"source": draft["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
        );
        assert_eq!(merged["commit"]["sequence"], 1);
        assert_eq!(
            invoke(
                "releaseCommit",
                json!({"commit": merged["commit"]["id"], "version": 0})
            ),
            json!({"commit": merged["commit"]["id"], "version": 1})
        );
        let released = invoke("released", json!({}));
        let counts: Vec<(String, usize)> = released["tree"]
            .as_object()
            .expect("a tree")
            .iter()
            .map(|(kind, rows)| (kind.clone(), rows.as_array().map_or(0, Vec::len)))
            .collect();
        assert_eq!(
            counts,
            [
                ("cover".to_owned(), 1),
                ("ingredient".to_owned(), 1),
                ("step".to_owned(), 1)
            ]
        );
        assert_eq!(committed["ref"]["parent"], main["id"]);
        // The sweep is on for the schema, at the interval its config gives.
        engine.runner().run_due().expect("run");
        let schedules: Vec<(String, Option<i64>)> = engine
            .runner()
            .status()
            .expect("status")
            .schedules
            .into_iter()
            .filter(|schedule| schedule.behavior == "Branches")
            .map(|schedule| {
                (
                    serde_json::to_value(schedule.state)
                        .expect("a state")
                        .as_str()
                        .unwrap_or_default()
                        .to_owned(),
                    schedule.every_ms,
                )
            })
            .collect();
        assert_eq!(schedules, [("active".to_owned(), Some(3_600_000))]);
    }
}
